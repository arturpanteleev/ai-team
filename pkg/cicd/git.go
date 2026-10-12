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

// MergeRequest identifies the controller-created pull request to deploy.
type MergeRequest struct {
	RepositoryDir string
	Remote        string
	Branch        string
	PRURL         string
	// PRHead is the commit the controller pushed for the pull request.
	PRHead string
}

type pullRequestMergeView struct {
	State       string `json:"state"`
	BaseRefName string `json:"baseRefName"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

// ResolveMergedDeploy decides what to deploy for a merged pull request.
// GitHub only points at the merge commit; Git decides whether it is valid:
//
//  1. the merge commit exists and is reachable from the fetched branch tip;
//  2. the pull request content is in it: either the PR head is an ancestor of
//     the merge commit (merge commit or fast-forward), or the merge commit's
//     own change equals the PR change against its merge base (squash, or a
//     rebase of a single commit).
//
// The deploy target is the branch tip, never the PR head. An open pull request
// returns ErrNotMerged; a closed one or any failed proof returns
// ErrDeployRejected. Git and gh diagnostics are not copied into errors.
func ResolveMergedDeploy(ctx context.Context, runner delivery.CommandRunner, request MergeRequest) (MergeProof, error) {
	if runner == nil {
		runner = delivery.ExecRunner{}
	}
	if request.Remote == "" {
		request.Remote = "origin"
	}
	if !ValidCommitSHA(request.PRHead) {
		return MergeProof{}, fmt.Errorf("%w: pull request head SHA is invalid", ErrDeployRejected)
	}
	if !ValidBranch(request.Branch) || !ValidBranch(request.Remote) {
		return MergeProof{}, fmt.Errorf("%w: deploy branch or remote is invalid", ErrDeployRejected)
	}
	if parsed, err := url.Parse(request.PRURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return MergeProof{}, fmt.Errorf("%w: pull request URL is invalid", ErrDeployRejected)
	}
	view := runner.Run(ctx, request.RepositoryDir, "gh", "pr", "view", request.PRURL, "--json", "state,baseRefName,mergeCommit")
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

	tracking := "refs/remotes/" + request.Remote + "/" + request.Branch
	fetch := runner.Run(ctx, request.RepositoryDir, "git", "fetch", "--no-tags", "--quiet", request.Remote, "+refs/heads/"+request.Branch+":"+tracking)
	if fetch.Status != delivery.StepPassed {
		if err := ctx.Err(); err != nil {
			return MergeProof{}, err
		}
		return MergeProof{}, &TransientError{Err: fmt.Errorf("git fetch of deploy branch %s failed (exit %d)", request.Branch, fetch.ExitCode)}
	}
	git := gitQuery{ctx: ctx, runner: runner, dir: request.RepositoryDir}
	tip, err := git.commit(tracking)
	if err != nil {
		return MergeProof{}, fmt.Errorf("%w: deploy branch tip: %v", ErrDeployRejected, err)
	}
	if resolved, err := git.commit(mergeCommit); err != nil || resolved != mergeCommit {
		return MergeProof{}, fmt.Errorf("%w: merge commit %s is not present after fetch", ErrDeployRejected, mergeCommit)
	}
	if resolved, err := git.commit(request.PRHead); err != nil || resolved != request.PRHead {
		return MergeProof{}, fmt.Errorf("%w: pull request head %s is not present locally", ErrDeployRejected, request.PRHead)
	}
	if ok, err := git.ancestor(mergeCommit, tip); err != nil || !ok {
		return MergeProof{}, fmt.Errorf("%w: merge commit %s is not in %s/%s", ErrDeployRejected, mergeCommit, request.Remote, request.Branch)
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

// WaitForMerge polls ResolveMergedDeploy while the pull request is open or a
// transient gh/git failure occurs, until timeout or cancellation.
func WaitForMerge(ctx context.Context, runner delivery.CommandRunner, request MergeRequest, options WaitOptions) (MergeProof, error) {
	clock := options.Clock
	if clock == nil {
		clock = SystemClock()
	}
	poll := options.PollInterval
	if poll <= 0 {
		poll = 15 * time.Second
	}
	deadline := clock.Now().Add(options.Timeout)
	for {
		proof, err := ResolveMergedDeploy(ctx, runner, request)
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
	prDiff, err := g.run("diff", "--no-color", "--no-ext-diff", "--binary", base, prHead)
	if err != nil {
		return err
	}
	mergeDiff, err := g.run("diff", "--no-color", "--no-ext-diff", "--binary", parent, mergeCommit)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prDiff) == "" || normalizeDiff(prDiff) != normalizeDiff(mergeDiff) {
		return fmt.Errorf("merge commit %s does not contain exactly the pull request change", mergeCommit)
	}
	return nil
}

var hunkHeader = regexp.MustCompile(`^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@`)

func normalizeDiff(diff string) string {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
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
// named variables (the CI/CD token) from the child environment.
type EnvFilteredRunner struct {
	Drop []string
}

const filteredRunnerOutputLimit = 4 << 20

func (r EnvFilteredRunner) Run(ctx context.Context, dir, name string, args ...string) delivery.StepResult {
	result := delivery.StepResult{Command: append([]string{name}, args...), ExitCode: -1, Status: delivery.StepFailed, StartedAt: time.Now().UTC()}
	stdout, stderr := &limitedWriter{limit: filteredRunnerOutputLimit}, &limitedWriter{limit: filteredRunnerOutputLimit}
	command := exec.Command(name, args...)
	command.Dir, command.Stdout, command.Stderr = dir, stdout, stderr
	command.Env = delivery.NonInteractiveEnv(filterEnv(os.Environ(), r.Drop))
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
