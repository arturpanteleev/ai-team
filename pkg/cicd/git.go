package cicd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/process"
)

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// ValidBranch accepts a conservative subset of Git branch names that cannot be
// parsed as an option or a revision expression.
func ValidBranch(value string) bool {
	return branchPattern.MatchString(value) && !strings.Contains(value, "..") &&
		!strings.Contains(value, "//") && !strings.HasSuffix(value, "/") &&
		!strings.HasSuffix(value, ".lock") && !strings.HasSuffix(value, ".")
}

// MergeProof is the result of proving, with local Git, that a pull request
// landed in the deploy branch.
type MergeProof struct {
	// MergeCommit is the commit GitHub reports for the merged pull request
	// (merge commit, fast-forward head or squash commit).
	MergeCommit string
	// DeploySHA is the fetched tip of the deploy branch. It contains
	// MergeCommit and everything merged after it, so deploying it can never
	// roll back changes that already reached the branch.
	DeploySHA string
}

// DefaultCredentialHelper authenticates the isolated deploy fetch with the
// controller's gh login. It is passed explicitly; credential helpers from the
// checkout or the user's Git configuration are never used.
const DefaultCredentialHelper = "!gh auth git-credential"

// MergeRequest identifies the controller-created pull request to deploy.
type MergeRequest struct {
	// Repository is the "host/owner/repo" identity captured at run start,
	// before any agent could change the checkout's Git configuration.
	Repository string
	// FetchURL is the URL fetched for the deploy branch. The controller derives
	// it from Repository; it never comes from the checkout's remotes.
	FetchURL string
	Branch   string
	PRURL    string
	// PRHead is the commit the controller pushed for the pull request.
	PRHead string
	// CredentialHelper overrides DefaultCredentialHelper (tests).
	CredentialHelper string
	// DropEnv names controller variables (the CI/CD token) that Git must not
	// inherit.
	DropEnv []string
	// GitRunner replaces the isolated Git runner (tests only).
	GitRunner delivery.CommandRunner
}

type pullRequestMergeView struct {
	State       string `json:"state"`
	BaseRefName string `json:"baseRefName"`
	HeadRefOID  string `json:"headRefOid"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

// RepositoryFetchURL derives the HTTPS fetch URL from a captured
// "host/owner/repo" identity.
func RepositoryFetchURL(repository string) (string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || strings.ContainsAny(repository, ":@?#\\ ") {
		return "", fmt.Errorf("%w: task repository identity is invalid", ErrDeployRejected)
	}
	return "https://" + repository + ".git", nil
}

// inheritedGitVariables are removed from the controller environment before
// isolated Git runs: they could point Git at another repository or inject
// configuration.
var inheritedGitVariables = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_REPLACE_REF_BASE",
	// A template directory could plant hooks or configuration into the
	// private repository at init time.
	"GIT_TEMPLATE_DIR",
}

// Fetch stall limits: a transfer slower than gitLowSpeedLimit bytes per
// second for gitLowSpeedTime seconds is aborted instead of hanging until the
// stage budget ends.
const (
	gitLowSpeedLimit = "1000"
	gitLowSpeedTime  = "60"
)

// DefaultMergeCheckTimeout bounds one merge check (gh calls, fetch and the Git
// proof). It is below config.DeployStageMargin, so the last check started
// before merge_timeout cannot eat the build wait.
const DefaultMergeCheckTimeout = 3 * time.Minute

// isolatedGitEnv runs Git against a private bare repository: no system,
// global or checkout configuration, no replace refs or grafts, no
// commit-graph, and exactly one explicit credential helper. The empty
// credential.helper entry resets any helper list before the explicit one.
func isolatedGitEnv(gitDir, credentialHelper string) []string {
	config := [][2]string{
		{"credential.helper", ""},
		{"credential.helper", credentialHelper},
		{"core.commitGraph", "false"},
		{"core.hooksPath", os.DevNull},
		{"http.lowSpeedLimit", gitLowSpeedLimit},
		{"http.lowSpeedTime", gitLowSpeedTime},
	}
	env := []string{
		"GIT_DIR=" + gitDir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + os.DevNull,
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(config)),
	}
	for index, entry := range config {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, entry[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, entry[1]))
	}
	return env
}

var pullNumberPath = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/([1-9][0-9]*)/?$`)

// ResolveMergedDeploy decides what to deploy for a merged pull request.
// GitHub names the merge commit and the branch tip; Git decides whether they
// are valid. All Git work runs in a private temporary bare repository that
// fetches the deploy branch and the PR head from the captured repository URL,
// so nothing in the agent-writable checkout (.git/config, attributes,
// credential helpers, replace refs, grafts, commit-graph, objects) is read:
//
//  1. the pull request belongs to the captured repository, targets the deploy
//     branch and its head is the commit the controller pushed;
//  2. the deploy target is the branch tip reported by the GitHub API; the
//     fetch must deliver exactly that object;
//  3. the merge commit is reachable from that tip;
//  4. the pull request content is in it: either the PR head is an ancestor of
//     the merge commit (merge commit or fast-forward), or the merge commit's
//     own change equals the PR change against its merge base (squash, or a
//     rebase of a single commit), compared without textconv, external diff
//     or ignored submodules.
//
// An open pull request returns ErrNotMerged; a closed one or any failed proof
// returns ErrDeployRejected. Git and gh diagnostics are not copied into errors.
func ResolveMergedDeploy(ctx context.Context, runner delivery.CommandRunner, request MergeRequest) (MergeProof, error) {
	if runner == nil {
		runner = EnvFilteredRunner{Drop: request.DropEnv}
	}
	if !ValidCommitSHA(request.PRHead) {
		return MergeProof{}, fmt.Errorf("%w: pull request head SHA is invalid", ErrDeployRejected)
	}
	if !ValidBranch(request.Branch) {
		return MergeProof{}, fmt.Errorf("%w: deploy branch is invalid", ErrDeployRejected)
	}
	repoParts := strings.Split(request.Repository, "/")
	if _, err := RepositoryFetchURL(request.Repository); err != nil {
		return MergeProof{}, err
	}
	if strings.TrimSpace(request.FetchURL) == "" || strings.HasPrefix(request.FetchURL, "-") {
		return MergeProof{}, fmt.Errorf("%w: deploy fetch URL is invalid", ErrDeployRejected)
	}
	parsedPR, err := url.Parse(request.PRURL)
	var prPath []string
	if err == nil {
		prPath = pullNumberPath.FindStringSubmatch(parsedPR.Path)
	}
	if err != nil || parsedPR.Scheme != "https" || parsedPR.User != nil || parsedPR.RawQuery != "" || parsedPR.Fragment != "" ||
		!strings.EqualFold(parsedPR.Host, repoParts[0]) || prPath == nil ||
		!strings.EqualFold(prPath[1], repoParts[1]) || !strings.EqualFold(prPath[2], repoParts[2]) {
		return MergeProof{}, fmt.Errorf("%w: pull request URL is not in the task repository", ErrDeployRejected)
	}
	pullNumber := prPath[3]
	// Every child process runs in a private empty directory, never in the
	// controller's working directory, which may be the agent-writable checkout
	// whose Git configuration gh would otherwise read.
	workDir, err := os.MkdirTemp("", "ai-team-deploy-*")
	if err != nil {
		return MergeProof{}, fmt.Errorf("deploy verification directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	gitDir := filepath.Join(workDir, "repo.git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		return MergeProof{}, fmt.Errorf("deploy verification repository: %w", err)
	}
	ghDir := filepath.Join(workDir, "gh")
	if err := os.Mkdir(ghDir, 0o700); err != nil {
		return MergeProof{}, fmt.Errorf("deploy verification directory: %w", err)
	}
	view := runner.Run(ctx, ghDir, "gh", "pr", "view", request.PRURL, "--json", "state,baseRefName,headRefOid,mergeCommit")
	if view.Status != delivery.StepPassed || view.Truncated {
		if err := ctx.Err(); err != nil {
			return MergeProof{}, err
		}
		return MergeProof{}, &TransientError{Err: fmt.Errorf("gh pr view failed (exit %d)", view.ExitCode)}
	}
	var pr pullRequestMergeView
	decoder := json.NewDecoder(strings.NewReader(view.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pr); err != nil {
		return MergeProof{}, errors.New("gh pr view returned invalid JSON")
	}
	if !strings.EqualFold(pr.HeadRefOID, request.PRHead) {
		return MergeProof{}, fmt.Errorf("%w: pull request head is not the commit published by the controller", ErrDeployRejected)
	}
	switch pr.State {
	case "MERGED":
	case "OPEN":
		return MergeProof{}, ErrNotMerged
	default:
		return MergeProof{}, fmt.Errorf("%w: pull request state is %q", ErrDeployRejected, pr.State)
	}
	if pr.BaseRefName != request.Branch {
		return MergeProof{}, fmt.Errorf("%w: pull request was merged into %q, deploy branch is %q", ErrDeployRejected, pr.BaseRefName, request.Branch)
	}
	if pr.MergeCommit == nil || !ValidCommitSHA(strings.ToLower(pr.MergeCommit.OID)) {
		return MergeProof{}, fmt.Errorf("%w: merged pull request has no merge commit", ErrDeployRejected)
	}
	mergeCommit := strings.ToLower(pr.MergeCommit.OID)

	tip, err := githubBranchTip(ctx, runner, ghDir, repoParts, request.Branch)
	if err != nil {
		return MergeProof{}, err
	}

	gitRunner := request.GitRunner
	if gitRunner == nil {
		helper := request.CredentialHelper
		if helper == "" {
			helper = DefaultCredentialHelper
		}
		gitRunner = isolatedGitRunner(gitDir, helper, request.DropEnv)
	}
	git := gitQuery{ctx: ctx, runner: gitRunner, dir: gitDir}
	if _, err := git.run(isolatedInitArgs...); err != nil {
		return MergeProof{}, fmt.Errorf("deploy verification repository: %w", err)
	}
	fetch := gitRunner.Run(ctx, gitDir, "git", "fetch", "--no-tags", "--quiet", "--end-of-options", request.FetchURL,
		"+refs/heads/"+request.Branch+":refs/deploy/tip", "+refs/pull/"+pullNumber+"/head:refs/deploy/pull")
	if fetch.Status != delivery.StepPassed {
		if err := ctx.Err(); err != nil {
			return MergeProof{}, err
		}
		return MergeProof{}, &TransientError{Err: fmt.Errorf("git fetch of deploy branch %s failed (exit %d)", request.Branch, fetch.ExitCode)}
	}
	if resolved, err := git.commit(tip); err != nil || resolved != tip {
		// The fetch did not deliver the branch tip GitHub reports; the branch
		// may have moved between the two calls.
		return MergeProof{}, &TransientError{Err: fmt.Errorf("deploy branch tip %s reported by GitHub is not present after fetch", tip)}
	}
	if resolved, err := git.commit(mergeCommit); err != nil || resolved != mergeCommit {
		return MergeProof{}, fmt.Errorf("%w: merge commit %s is not present after fetch", ErrDeployRejected, mergeCommit)
	}
	if resolved, err := git.commit(request.PRHead); err != nil || resolved != request.PRHead {
		return MergeProof{}, fmt.Errorf("%w: pull request head %s is not present after fetch", ErrDeployRejected, request.PRHead)
	}
	if ok, err := git.ancestor(mergeCommit, tip); err != nil || !ok {
		return MergeProof{}, fmt.Errorf("%w: merge commit %s is not in branch %s", ErrDeployRejected, mergeCommit, request.Branch)
	}
	if ok, err := git.ancestor(request.PRHead, mergeCommit); err != nil {
		return MergeProof{}, fmt.Errorf("%w: %v", ErrDeployRejected, err)
	} else if !ok {
		if err := git.sameChange(request.PRHead, mergeCommit); err != nil {
			return MergeProof{}, fmt.Errorf("%w: %v", ErrDeployRejected, err)
		}
	}
	return MergeProof{MergeCommit: mergeCommit, DeploySHA: tip}, nil
}

// isolatedInitArgs create the private repository without any template: no
// hooks, info files or configuration are copied from a template directory.
var isolatedInitArgs = []string{"init", "--bare", "--quiet", "--template="}

// isolatedGitRunner runs Git with the isolated environment and without the
// controller's Git location, configuration and template variables.
func isolatedGitRunner(gitDir, credentialHelper string, dropEnv []string) EnvFilteredRunner {
	return EnvFilteredRunner{Drop: append(append([]string(nil), dropEnv...), inheritedGitVariables...),
		Env: isolatedGitEnv(gitDir, credentialHelper)}
}

// githubBranchTip asks the GitHub API for the branch head of the captured
// repository. Unlike a fetched ref it does not depend on the checkout's Git
// configuration.
func githubBranchTip(ctx context.Context, runner delivery.CommandRunner, dir string, repo []string, branch string) (string, error) {
	endpoint := "repos/" + url.PathEscape(repo[1]) + "/" + url.PathEscape(repo[2]) + "/branches/" + url.PathEscape(branch)
	result := runner.Run(ctx, dir, "gh", "api", "--hostname", repo[0], endpoint)
	if result.Status != delivery.StepPassed || result.Truncated {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", &TransientError{Err: fmt.Errorf("gh api branch %s failed (exit %d)", branch, result.ExitCode)}
	}
	var response struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &response); err != nil {
		return "", errors.New("gh api returned invalid branch JSON")
	}
	tip := strings.ToLower(response.Commit.SHA)
	if response.Name != branch || !ValidCommitSHA(tip) {
		return "", fmt.Errorf("%w: GitHub returned no head for branch %s", ErrDeployRejected, branch)
	}
	return tip, nil
}

// WaitForMerge polls ResolveMergedDeploy while the pull request is open or a
// transient gh/git failure occurs, until timeout or cancellation. Each check
// has its own deadline (options.AttemptTimeout, DefaultMergeCheckTimeout when
// zero); a check that exceeds it is a transient failure, not a hang.
func WaitForMerge(ctx context.Context, runner delivery.CommandRunner, request MergeRequest, options WaitOptions) (MergeProof, error) {
	clock := options.Clock
	if clock == nil {
		clock = SystemClock()
	}
	poll := options.PollInterval
	if poll <= 0 {
		poll = 15 * time.Second
	}
	attemptTimeout := options.AttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = DefaultMergeCheckTimeout
	}
	deadline := clock.Now().Add(options.Timeout)
	for {
		proof, err := resolveMergedDeployWithin(ctx, runner, request, attemptTimeout)
		if err == nil || (!errors.Is(err, ErrNotMerged) && !IsTransient(err)) {
			return proof, err
		}
		if !clock.Now().Before(deadline) {
			return MergeProof{}, fmt.Errorf("%w: waited %s for the pull request merge: %v", ErrDeployTimeout, options.Timeout, err)
		}
		select {
		case <-ctx.Done():
			return MergeProof{}, fmt.Errorf("wait for pull request merge: %w", ctx.Err())
		case <-clock.After(poll):
		}
	}
}

// resolveMergedDeployWithin runs one merge check under its own deadline. When
// only that deadline (not the parent context) expired, the check is reported
// as a transient failure so the merge wait can try again.
func resolveMergedDeployWithin(ctx context.Context, runner delivery.CommandRunner, request MergeRequest, timeout time.Duration) (MergeProof, error) {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	proof, err := ResolveMergedDeploy(checkCtx, runner, request)
	if err != nil && ctx.Err() == nil && checkCtx.Err() != nil {
		return MergeProof{}, &TransientError{Err: fmt.Errorf("merge check exceeded %s", timeout)}
	}
	return proof, err
}

type gitQuery struct {
	ctx    context.Context
	runner delivery.CommandRunner
	dir    string
}

func (g gitQuery) run(args ...string) (string, error) {
	result := g.runner.Run(g.ctx, g.dir, "git", args...)
	if result.Status != delivery.StepPassed {
		if err := g.ctx.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("git %s failed (exit %d)", args[0], result.ExitCode)
	}
	if result.Truncated {
		return "", fmt.Errorf("git %s output exceeded the limit", args[0])
	}
	return result.Stdout, nil
}

func (g gitQuery) commit(revision string) (string, error) {
	output, err := g.run("rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (g gitQuery) ancestor(ancestor, descendant string) (bool, error) {
	result := g.runner.Run(g.ctx, g.dir, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	switch {
	case result.Status == delivery.StepPassed:
		return true, nil
	case result.ExitCode == 1:
		return false, nil
	default:
		if err := g.ctx.Err(); err != nil {
			return false, err
		}
		return false, fmt.Errorf("git merge-base failed (exit %d)", result.ExitCode)
	}
}

// sameChange proves that a squash (or single-commit rebase) merge commit
// introduces exactly the pull request change. The PR change is taken against
// the merge base of the PR head and the merge commit's first parent, like
// GitHub's squash. Diffs are compared ignoring hunk line numbers and blob
// index lines, the same normalization as git patch-id.
func (g gitQuery) sameChange(prHead, mergeCommit string) error {
	parent, err := g.commit(mergeCommit + "^1")
	if err != nil {
		return fmt.Errorf("merge commit %s has no parent", mergeCommit)
	}
	baseOutput, err := g.run("merge-base", prHead, parent)
	if err != nil {
		return fmt.Errorf("pull request head and deploy branch have no merge base")
	}
	base := strings.TrimSpace(baseOutput)
	prDiff, err := g.run(append(diffArgs, base, prHead)...)
	if err != nil {
		return err
	}
	mergeDiff, err := g.run(append(diffArgs, parent, mergeCommit)...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prDiff) == "" || normalizeDiff(prDiff) != normalizeDiff(mergeDiff) {
		return fmt.Errorf("merge commit %s does not contain exactly the pull request change", mergeCommit)
	}
	return nil
}

// diffArgs pins every diff option a checkout could change through config or
// attributes: textconv and external drivers are disabled, binary content is
// shown in full and the context, rename and path settings are explicit.
var diffArgs = []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--binary", "--full-index",
	"--no-renames", "--no-relative", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", "-U3", "--diff-algorithm=myers"}

var hunkHeader = regexp.MustCompile(`^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@`)

func normalizeDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "index ") {
			continue
		}
		if location := hunkHeader.FindStringIndex(line); location != nil {
			line = "@@" + line[location[1]:]
		}
		normalized = append(normalized, line)
	}
	return strings.Join(normalized, "\n")
}

// EnvFilteredRunner runs git and gh like delivery.ExecRunner, but removes the
// named variables (the CI/CD token) from the child environment and appends
// Env.
type EnvFilteredRunner struct {
	Drop []string
	Env  []string
}

const filteredRunnerOutputLimit = 4 << 20

func (r EnvFilteredRunner) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	result := delivery.StepResult{Command: append([]string{name}, args...), ExitCode: -1, Status: delivery.StepFailed, StartedAt: time.Now().UTC()}
	stdout, stderr := &limitedWriter{limit: filteredRunnerOutputLimit}, &limitedWriter{limit: filteredRunnerOutputLimit}
	command := exec.Command(name, args...)
	command.Dir, command.Stdout, command.Stderr = dir, stdout, stderr
	command.Env = append(delivery.NonInteractiveEnv(filterEnv(os.Environ(), r.Drop)), r.Env...)
	err := process.Run(ctx, command)
	result.FinishedAt = time.Now().UTC()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	result.Stdout, result.Stderr = stdout.buffer.String(), stderr.buffer.String()
	result.Truncated = stdout.truncated || stderr.truncated
	if err == nil {
		result.ExitCode, result.Status = 0, delivery.StepPassed
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

func filterEnv(environment, drop []string) []string {
	blocked := make(map[string]bool, len(drop))
	for _, name := range drop {
		if name != "" {
			blocked[name] = true
		}
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

type limitedWriter struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		w.truncated = true
		_, _ = w.buffer.Write(data[:remaining])
		return len(data), nil
	}
	return w.buffer.Write(data)
}

var _ io.Writer = (*limitedWriter)(nil)
