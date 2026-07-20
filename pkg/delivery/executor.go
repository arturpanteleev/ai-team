package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/process"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const maxCommandOutput = 256 << 10
const maxStateSize = 64 << 20
const deliveryStateSchemaVersion = 4

const (
	StepPassed  = "passed"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

type Request struct {
	TargetDir string
	Feature   string
	Plan      Plan
}

type StepResult struct {
	Step       string        `json:"step"`
	Command    []string      `json:"command,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration_ns"`
	ExitCode   int           `json:"exit_code"`
	Status     string        `json:"status"`
	Stdout     string        `json:"stdout,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	Truncated  bool          `json:"truncated,omitempty"`
}

type Result struct {
	PlanHash  string       `json:"plan_hash"`
	StatePath string       `json:"state_path"`
	CommitSHA string       `json:"commit_sha,omitempty"`
	PRURL     string       `json:"pr_url,omitempty"`
	Steps     []StepResult `json:"steps"`
}

type Service interface {
	Execute(context.Context, Request) (Result, error)
}

type CommandRunner interface {
	Run(context.Context, string, string, ...string) StepResult
}

type Controller struct {
	Runner CommandRunner
}

func NewController() *Controller { return &Controller{Runner: ExecRunner{}} }

// Prepare persists the approved-effect candidate before an interactive stop,
// so retry-from delivery can resume without rediscovering unrelated git dirt.
func Prepare(targetDir, feature string, plan Plan) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	target, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}
	planHash, err := plan.Hash()
	if err != nil {
		return "", err
	}
	statePath, err := deliveryStatePath(target, feature)
	if err != nil {
		return "", err
	}
	_, err = loadOrCreateState(statePath, planHash, plan)
	return statePath, err
}

func LoadPreparedPlan(targetDir, feature string) (Plan, bool, error) {
	target, err := filepath.Abs(targetDir)
	if err != nil {
		return Plan{}, false, err
	}
	statePath, err := deliveryStatePath(target, feature)
	if err != nil {
		return Plan{}, false, err
	}
	data, err := safeio.ReadRegularFile(statePath, maxStateSize)
	if errors.Is(err, fs.ErrNotExist) {
		return Plan{}, false, nil
	}
	if err != nil {
		return Plan{}, false, err
	}
	loaded, err := parseState(data)
	if err != nil {
		return Plan{}, false, err
	}
	return loaded.Plan, true, nil
}

type state struct {
	SchemaVersion  int          `json:"schema_version"`
	PlanHash       string       `json:"plan_hash"`
	Plan           Plan         `json:"plan"`
	CommitSHA      string       `json:"commit_sha,omitempty"`
	CommitVerified bool         `json:"commit_verified,omitempty"`
	Pushed         bool         `json:"pushed,omitempty"`
	PRURL          string       `json:"pr_url,omitempty"`
	Steps          []StepResult `json:"steps,omitempty"`
}

func (c *Controller) Execute(ctx context.Context, request Request) (Result, error) {
	if c.Runner == nil {
		c.Runner = ExecRunner{}
	}
	if err := request.Plan.Validate(); err != nil {
		return Result{}, err
	}
	if request.Plan.Branch != "ai-team/"+request.Feature {
		return Result{}, fmt.Errorf("delivery: branch %q должна точно совпадать с ai-team/%s", request.Plan.Branch, request.Feature)
	}
	target, err := filepath.Abs(request.TargetDir)
	if err != nil {
		return Result{}, err
	}
	planHash, err := request.Plan.Hash()
	if err != nil {
		return Result{}, err
	}
	statePath, err := deliveryStatePath(target, request.Feature)
	if err != nil {
		return Result{}, err
	}
	currentState, err := loadOrCreateState(statePath, planHash, request.Plan)
	if err != nil {
		return Result{}, err
	}
	result := func() Result {
		return Result{PlanHash: planHash, StatePath: statePath, CommitSHA: currentState.CommitSHA, PRURL: currentState.PRURL, Steps: append([]StepResult(nil), currentState.Steps...)}
	}
	record := func(step StepResult) error {
		currentState.Steps = append(currentState.Steps, step)
		return writeState(statePath, currentState)
	}
	run := func(step, name string, args ...string) (StepResult, error) {
		commandResult := c.Runner.Run(ctx, target, name, args...)
		commandResult.Step = step
		if err := record(commandResult); err != nil {
			return commandResult, err
		}
		if commandResult.Status != StepPassed {
			return commandResult, fmt.Errorf("delivery %s failed: %s", step, commandResult.Reason)
		}
		return commandResult, nil
	}
	skip := func(step, reason string) error {
		now := time.Now().UTC()
		return record(StepResult{Step: step, StartedAt: now, FinishedAt: now, ExitCode: 0, Status: StepSkipped, Reason: reason})
	}

	currentBranchResult, err := run("inspect_branch", "git", "branch", "--show-current")
	if err != nil {
		return result(), err
	}
	currentBranch := strings.TrimSpace(currentBranchResult.Stdout)
	if currentBranch != request.Plan.Branch {
		if currentBranch != request.Plan.BaseBranch && currentBranch != "main" && currentBranch != "master" {
			return result(), fmt.Errorf("delivery: текущая ветка %q не совпадает с plan branch %q или protected base", currentBranch, request.Plan.Branch)
		}
		probe := c.Runner.Run(ctx, target, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+request.Plan.Branch)
		probe.Step = "inspect_target_branch"
		if probe.ExitCode == 1 {
			probe.Status = StepSkipped
			probe.Reason = "target branch does not exist"
		}
		if err := record(probe); err != nil {
			return result(), err
		}
		if probe.Status == StepPassed {
			branchHead, branchErr := run("inspect_target_branch_head", "git", "rev-parse", "refs/heads/"+request.Plan.Branch)
			if branchErr != nil {
				return result(), branchErr
			}
			if strings.TrimSpace(branchHead.Stdout) != request.Plan.BaselineHead {
				return result(), fmt.Errorf("delivery: существующая branch %q имеет tip %s вместо approved baseline %s", request.Plan.Branch, strings.TrimSpace(branchHead.Stdout), request.Plan.BaselineHead)
			}
			if _, err := run("switch_branch", "git", "switch", request.Plan.Branch); err != nil {
				return result(), err
			}
		} else if probe.ExitCode == 1 {
			if _, err := run("create_branch", "git", "switch", "-c", request.Plan.Branch); err != nil {
				return result(), err
			}
		} else {
			return result(), fmt.Errorf("delivery inspect_target_branch failed: %s", probe.Reason)
		}
	} else if err := skip("switch_branch", "already on planned branch"); err != nil {
		return result(), err
	}
	branchHead, err := run("verify_branch_head", "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return result(), err
	}
	actualHead := strings.TrimSpace(branchHead.Stdout)
	if currentState.CommitSHA == "" && actualHead != request.Plan.BaselineHead {
		// The process may have crashed after git commit succeeded but before its
		// identity was durably recorded. Recover only an exact approved commit;
		// any unrelated branch advance remains fail-closed.
		if err := verifyCommittedChange(ctx, c.Runner, target, request.Plan, actualHead, record); err != nil {
			return result(), fmt.Errorf("delivery: unrecorded HEAD is not the approved commit: %w", err)
		}
		currentState.CommitSHA = actualHead
		currentState.CommitVerified = true
		if err := record(successStep("recover_commit", "recovered exact approved commit "+actualHead)); err != nil {
			return result(), err
		}
	}
	expectedHead := request.Plan.BaselineHead
	if currentState.CommitSHA != "" {
		expectedHead = currentState.CommitSHA
	}
	if actualHead != expectedHead {
		return result(), fmt.Errorf("delivery: branch HEAD %s не совпадает с approved state %s", strings.TrimSpace(branchHead.Stdout), expectedHead)
	}
	if err := verifyApprovedWorkspace(ctx, target, request.Plan); err != nil {
		return result(), err
	}

	if currentState.CommitSHA == "" {
		head, headErr := run("inspect_head", "git", "rev-parse", "--verify", "HEAD")
		if headErr != nil || strings.TrimSpace(head.Stdout) == "" {
			return result(), errors.Join(fmt.Errorf("delivery требует существующий HEAD"), headErr)
		}
		staged, stagedErr := run("inspect_staged", "git", "diff", "--cached", "--name-only", "-z")
		if stagedErr != nil {
			return result(), stagedErr
		}
		if len(splitNUL(staged.Stdout)) > 0 {
			return result(), fmt.Errorf("delivery: index уже содержит staged files; отказ, чтобы не захватить чужие изменения")
		}
		for _, file := range request.Plan.Files {
			fullPath := filepath.Join(target, filepath.FromSlash(file))
			if info, statErr := os.Lstat(fullPath); statErr == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
				return result(), fmt.Errorf("delivery: plan file %q должен быть обычным файлом без symlink или deletion", file)
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return result(), fmt.Errorf("delivery: stat %q: %w", file, statErr)
			}
		}
		attributeArgs := append([]string{"check-attr", "-z", "filter", "--"}, request.Plan.Files...)
		attributes, attributeErr := run("verify_no_clean_filters", "git", attributeArgs...)
		if attributeErr != nil {
			return result(), attributeErr
		}
		if filtered := configuredFilterPaths(attributes.Stdout); len(filtered) > 0 {
			return result(), fmt.Errorf("delivery: clean/smudge filters запрещены для approved files: %s", strings.Join(filtered, ", "))
		}
		addArgs := append([]string{"add", "--"}, request.Plan.Files...)
		if _, err := run("stage_exact_files", "git", addArgs...); err != nil {
			return result(), err
		}
		staged, stagedErr = run("verify_staged_files", "git", "diff", "--cached", "--name-only", "-z")
		if stagedErr != nil {
			return result(), stagedErr
		}
		actual := splitNUL(staged.Stdout)
		if !samePaths(actual, request.Plan.Files) {
			resetArgs := append([]string{"reset", "HEAD", "--"}, request.Plan.Files...)
			_, _ = run("rollback_staging", "git", resetArgs...)
			return result(), fmt.Errorf("delivery: staged set %v не совпадает с approved plan %v", actual, request.Plan.Files)
		}
		if err := verifyStagedChange(ctx, target, request.Plan, record); err != nil {
			resetArgs := append([]string{"reset", "HEAD", "--"}, request.Plan.Files...)
			_, _ = run("rollback_staging", "git", resetArgs...)
			return result(), err
		}
		if _, err := run("commit", "git", "-c", "core.hooksPath=/dev/null", "commit", "--no-verify", "-m", request.Plan.CommitMessage); err != nil {
			return result(), err
		}
		commit, err := run("record_commit", "git", "rev-parse", "HEAD")
		if err != nil {
			return result(), err
		}
		commitSHA := strings.TrimSpace(commit.Stdout)
		if err := verifyCommittedChange(ctx, c.Runner, target, request.Plan, commitSHA, record); err != nil {
			return result(), err
		}
		currentState.CommitSHA = commitSHA
		currentState.CommitVerified = true
		if err := writeState(statePath, currentState); err != nil {
			return result(), err
		}
	} else {
		head, err := run("verify_resume_head", "git", "rev-parse", "HEAD")
		if err != nil {
			return result(), err
		}
		if strings.TrimSpace(head.Stdout) != currentState.CommitSHA {
			return result(), fmt.Errorf("delivery resume: HEAD не совпадает с сохранённым commit %s", currentState.CommitSHA)
		}
		if err := skip("commit", "commit already recorded: "+currentState.CommitSHA); err != nil {
			return result(), err
		}
	}

	if !currentState.Pushed {
		if _, err := run("push", "git", "push", "-u", request.Plan.Remote, request.Plan.Branch); err != nil {
			return result(), err
		}
		currentState.Pushed = true
		if err := writeState(statePath, currentState); err != nil {
			return result(), err
		}
	} else if err := skip("push", "branch already recorded as pushed"); err != nil {
		return result(), err
	}
	remoteHead, err := run("verify_remote_head", "git", "ls-remote", request.Plan.Remote, "refs/heads/"+request.Plan.Branch)
	if err != nil {
		return result(), err
	}
	if remoteObjectID(remoteHead.Stdout) != currentState.CommitSHA {
		return result(), fmt.Errorf("delivery: remote branch не указывает на approved commit %s", currentState.CommitSHA)
	}

	if currentState.PRURL == "" {
		view := c.Runner.Run(ctx, target, "gh", "pr", "view", request.Plan.Branch, "--json", "url,state,baseRefName,headRefName,headRefOid")
		view.Step = "find_pull_request"
		if view.ExitCode == 1 {
			view.Status = StepSkipped
			view.Reason = "pull request does not exist"
		}
		if err := record(view); err != nil {
			return result(), err
		}
		if view.Status == StepPassed && strings.TrimSpace(view.Stdout) != "" {
			prURL, verifyErr := verifyPullRequest(view.Stdout, request.Plan, currentState.CommitSHA)
			if verifyErr != nil {
				return result(), verifyErr
			}
			currentState.PRURL = prURL
		} else {
			created, err := run("create_pull_request", "gh", "pr", "create",
				"--base", request.Plan.BaseBranch, "--head", request.Plan.Branch,
				"--title", request.Plan.PRTitle, "--body", request.Plan.PRBody)
			if err != nil {
				return result(), err
			}
			if strings.TrimSpace(created.Stdout) == "" {
				return result(), fmt.Errorf("delivery: gh pr create не вернул URL")
			}
			verified, verifyErr := run("verify_pull_request", "gh", "pr", "view", request.Plan.Branch, "--json", "url,state,baseRefName,headRefName,headRefOid")
			if verifyErr != nil {
				return result(), verifyErr
			}
			currentState.PRURL, verifyErr = verifyPullRequest(verified.Stdout, request.Plan, currentState.CommitSHA)
			if verifyErr != nil {
				return result(), verifyErr
			}
		}
		if err := writeState(statePath, currentState); err != nil {
			return result(), err
		}
	} else {
		verified, verifyErr := run("verify_pull_request", "gh", "pr", "view", request.Plan.Branch, "--json", "url,state,baseRefName,headRefName,headRefOid")
		if verifyErr != nil {
			return result(), verifyErr
		}
		prURL, verifyErr := verifyPullRequest(verified.Stdout, request.Plan, currentState.CommitSHA)
		if verifyErr != nil {
			return result(), verifyErr
		}
		if prURL != currentState.PRURL {
			return result(), fmt.Errorf("delivery resume: PR URL %s не совпадает с сохранённым %s", prURL, currentState.PRURL)
		}
	}

	return result(), nil
}

func verifyApprovedWorkspace(ctx context.Context, target string, plan Plan) error {
	digest, err := checks.WorkspaceDigest(target)
	if err != nil {
		return fmt.Errorf("delivery: workspace digest: %w", err)
	}
	if digest != plan.VerifiedWorkspaceDigest {
		return fmt.Errorf("delivery: workspace bytes изменились после проверки: approved=%s current=%s", plan.VerifiedWorkspaceDigest, digest)
	}
	if err := VerifyPreparedWorkspace(target, plan, digest); err != nil {
		return err
	}
	for _, file := range plan.Files {
		actual, digestErr := workspaceFileDigest(ctx, target, plan.BaselineHead, file)
		if digestErr != nil {
			return digestErr
		}
		if actual != plan.FileDigests[file] {
			return fmt.Errorf("delivery: file %q изменился после проверки: approved=%s current=%s", file, plan.FileDigests[file], actual)
		}
	}
	return nil
}

func verifyCommittedChange(
	ctx context.Context,
	runner CommandRunner,
	target string,
	plan Plan,
	commitSHA string,
	record func(StepResult) error,
) error {
	run := func(step string, args ...string) (StepResult, error) {
		result := runner.Run(ctx, target, "git", args...)
		result.Step = step
		if err := record(result); err != nil {
			return result, err
		}
		if result.Status != StepPassed {
			return result, fmt.Errorf("delivery %s failed: %s", step, result.Reason)
		}
		return result, nil
	}
	message, err := run("verify_commit_message", "show", "-s", "--format=%B", commitSHA)
	if err != nil {
		return err
	}
	if strings.TrimRight(message.Stdout, "\r\n") != plan.CommitMessage {
		return fmt.Errorf("delivery: commit message не совпадает с approved plan")
	}
	parent, err := run("verify_commit_parent", "rev-parse", commitSHA+"^")
	if err != nil {
		return err
	}
	if strings.TrimSpace(parent.Stdout) != plan.BaselineHead {
		return fmt.Errorf("delivery: commit parent %s не совпадает с approved baseline %s", strings.TrimSpace(parent.Stdout), plan.BaselineHead)
	}
	changed, err := run("verify_commit_paths", "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", commitSHA)
	if err != nil {
		return err
	}
	if actual := splitNUL(changed.Stdout); !samePaths(actual, plan.Files) {
		return fmt.Errorf("delivery: committed paths %v не совпадают с approved plan %v", actual, plan.Files)
	}
	tree, treeErr := gitTreeEntries(ctx, target, commitSHA, plan.Files)
	if treeErr != nil {
		return treeErr
	}
	for index, file := range plan.Files {
		stepName := fmt.Sprintf("verify_commit_blob_%03d", index+1)
		if plan.FileDigests[file] == DeletedDigest {
			if _, exists := tree[file]; exists {
				return fmt.Errorf("delivery: deleted file %q всё ещё присутствует в commit", file)
			}
			if err := record(successStep(stepName, "approved deletion absent from commit")); err != nil {
				return err
			}
			continue
		}
		entry, exists := tree[file]
		if !exists || entry.Mode != plan.FileModes[file] {
			return fmt.Errorf("delivery: committed mode %q is %q instead of approved %q", file, entry.Mode, plan.FileModes[file])
		}
		digest, err := hashGitBlob(ctx, target, commitSHA+":"+file)
		if err != nil {
			return err
		}
		if digest != plan.FileDigests[file] {
			return fmt.Errorf("delivery: committed bytes %q имеют sha256 %s вместо approved %s", file, digest, plan.FileDigests[file])
		}
		if err := record(successStep(stepName, "sha256:"+digest+" mode:"+entry.Mode)); err != nil {
			return err
		}
	}
	return nil
}

type gitTreeEntry struct {
	Mode string
}

func verifyStagedChange(ctx context.Context, target string, plan Plan, record func(StepResult) error) error {
	started := time.Now().UTC()
	args := append([]string{"ls-files", "--stage", "-z", "--"}, plan.Files...)
	output, err := boundedGitOutput(ctx, target, args...)
	if err != nil {
		return err
	}
	entries, err := parseGitEntries(output, true)
	if err != nil {
		return err
	}
	for _, file := range plan.Files {
		entry, exists := entries[file]
		if plan.FileDigests[file] == DeletedDigest {
			if exists {
				return fmt.Errorf("delivery: approved deletion %q присутствует в staged index", file)
			}
			continue
		}
		if !exists || entry.Mode != plan.FileModes[file] {
			return fmt.Errorf("delivery: staged mode %q is %q instead of approved %q", file, entry.Mode, plan.FileModes[file])
		}
		digest, hashErr := hashGitBlob(ctx, target, ":"+file)
		if hashErr != nil {
			return hashErr
		}
		if digest != plan.FileDigests[file] {
			return fmt.Errorf("delivery: staged bytes %q имеют sha256 %s вместо approved %s", file, digest, plan.FileDigests[file])
		}
	}
	step := successStep("verify_staged_content", fmt.Sprintf("%d exact paths, blobs and modes", len(plan.Files)))
	step.StartedAt, step.FinishedAt = started, time.Now().UTC()
	step.Duration = step.FinishedAt.Sub(step.StartedAt)
	return record(step)
}

func gitTreeEntries(ctx context.Context, target, treeish string, files []string) (map[string]gitTreeEntry, error) {
	args := append([]string{"ls-tree", "-rz", treeish, "--"}, files...)
	output, err := boundedGitOutput(ctx, target, args...)
	if err != nil {
		return nil, err
	}
	return parseGitEntries(output, false)
}

func parseGitEntries(output []byte, index bool) (map[string]gitTreeEntry, error) {
	result := make(map[string]gitTreeEntry)
	for _, record := range splitNULOrdered(string(output)) {
		metadata, file, ok := strings.Cut(record, "\t")
		if !ok || file == "" {
			return nil, fmt.Errorf("delivery: malformed git tree/index record")
		}
		fields := strings.Fields(metadata)
		if index {
			if len(fields) != 3 || fields[2] != "0" {
				return nil, fmt.Errorf("delivery: malformed/unmerged git index record for %q", file)
			}
		} else if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("delivery: malformed/non-blob git tree record for %q", file)
		}
		result[filepath.ToSlash(file)] = gitTreeEntry{Mode: fields[0]}
	}
	return result, nil
}

func boundedGitOutput(ctx context.Context, target string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = target
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	if len(output) > 4<<20 {
		return nil, fmt.Errorf("git %s output exceeds 4 MiB", strings.Join(args, " "))
	}
	return output, nil
}

func hashGitBlob(ctx context.Context, target, object string) (string, error) {
	command := exec.CommandContext(ctx, "git", "cat-file", "blob", object)
	command.Dir = target
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr boundedBuffer
	stderr.limit = 64 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, stdout)
	waitErr := command.Wait()
	if copyErr != nil {
		return "", fmt.Errorf("read git blob %s: %w", object, copyErr)
	}
	if waitErr != nil {
		return "", fmt.Errorf("git cat-file %s: %w: %s", object, waitErr, stderr.String())
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func successStep(name, reason string) StepResult {
	now := time.Now().UTC()
	return StepResult{Step: name, StartedAt: now, FinishedAt: now, ExitCode: 0, Status: StepPassed, Reason: reason}
}

func configuredFilterPaths(output string) []string {
	parts := splitNULOrdered(output)
	if len(parts)%3 != 0 {
		return []string{"<malformed-check-attr-output>"}
	}
	var filtered []string
	for index := 0; index+2 < len(parts); index += 3 {
		if parts[index+1] != "filter" {
			return []string{"<malformed-check-attr-output>"}
		}
		if parts[index+2] != "unspecified" && parts[index+2] != "unset" {
			filtered = append(filtered, filepath.ToSlash(parts[index]))
		}
	}
	sort.Strings(filtered)
	return filtered
}

func splitNULOrdered(value string) []string {
	var result []string
	for _, part := range strings.Split(value, "\x00") {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func remoteObjectID(output string) string {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func verifyPullRequest(output string, plan Plan, commitSHA string) (string, error) {
	var response struct {
		URL         string `json:"url"`
		State       string `json:"state"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		HeadRefOID  string `json:"headRefOid"`
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return "", fmt.Errorf("delivery: invalid gh pr view JSON: %w", err)
	}
	if response.URL == "" || response.State != "OPEN" || response.BaseRefName != plan.BaseBranch ||
		response.HeadRefName != plan.Branch || response.HeadRefOID != commitSHA {
		return "", fmt.Errorf("delivery: PR не совпадает с approved state: url=%q state=%q base=%q head=%q oid=%q", response.URL, response.State, response.BaseRefName, response.HeadRefName, response.HeadRefOID)
	}
	return response.URL, nil
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, name string, args ...string) StepResult {
	result := StepResult{Command: append([]string{name}, args...), ExitCode: -1, Status: StepFailed, StartedAt: time.Now().UTC()}
	stdout, stderr := &boundedBuffer{limit: maxCommandOutput}, &boundedBuffer{limit: maxCommandOutput}
	command := exec.Command(name, args...)
	command.Dir, command.Stdout, command.Stderr = dir, stdout, stderr
	err := process.Run(ctx, command)
	result.FinishedAt = time.Now().UTC()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	result.Truncated = stdout.truncated || stderr.truncated
	if err == nil {
		result.ExitCode, result.Status = 0, StepPassed
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		result.Reason = ctx.Err().Error()
	} else {
		result.Reason = err.Error()
	}
	return result
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}

func (b *boundedBuffer) String() string { return b.buffer.String() }

func splitNUL(value string) []string {
	var result []string
	for _, part := range strings.Split(value, "\x00") {
		if part != "" {
			result = append(result, filepath.ToSlash(part))
		}
	}
	sort.Strings(result)
	return result
}

func samePaths(first, second []string) bool {
	first, second = append([]string(nil), first...), append([]string(nil), second...)
	sort.Strings(first)
	sort.Strings(second)
	if len(first) != len(second) {
		return false
	}
	for i := range first {
		if filepath.ToSlash(first[i]) != filepath.ToSlash(second[i]) {
			return false
		}
	}
	return true
}

func deliveryStatePath(target, feature string) (string, error) {
	if feature == "" || feature == "." || feature == ".." || strings.ContainsAny(feature, `/\\`) || strings.Contains(feature, "..") {
		return "", fmt.Errorf("delivery: невалидный feature %q", feature)
	}
	dir, err := safeio.EnsureDir(target, ".ai-team", "delivery")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, feature+".json"), nil
}

func loadOrCreateState(path, planHash string, plan Plan) (*state, error) {
	data, err := safeio.ReadRegularFile(path, maxStateSize)
	if errors.Is(err, fs.ErrNotExist) {
		initial := &state{SchemaVersion: deliveryStateSchemaVersion, PlanHash: planHash, Plan: plan}
		if err := writeState(path, initial); err != nil {
			return nil, err
		}
		return initial, nil
	}
	if err != nil {
		return nil, err
	}
	loaded, err := parseState(data)
	if err != nil {
		return nil, err
	}
	if loaded.PlanHash != planHash {
		if loaded.PRURL == "" {
			return nil, fmt.Errorf("delivery resume: незавершённый сохранённый plan не совпадает с текущим approved plan")
		}
		archivePath := strings.TrimSuffix(path, filepath.Ext(path)) + "." + loaded.PlanHash + ".completed.json"
		if err := archiveCompletedState(archivePath, data); err != nil {
			return nil, err
		}
		initial := &state{SchemaVersion: deliveryStateSchemaVersion, PlanHash: planHash, Plan: plan}
		if err := writeState(path, initial); err != nil {
			return nil, err
		}
		return initial, nil
	}
	return loaded, nil
}

func parseState(data []byte) (*state, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var loaded state
	if err := decoder.Decode(&loaded); err != nil {
		return nil, fmt.Errorf("delivery state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("delivery state: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("delivery state: trailing data: %w", err)
	}
	if loaded.SchemaVersion != deliveryStateSchemaVersion {
		return nil, fmt.Errorf("delivery state schema %d не поддерживается", loaded.SchemaVersion)
	}
	if err := loaded.Plan.Validate(); err != nil {
		return nil, err
	}
	actualHash, err := loaded.Plan.Hash()
	if err != nil {
		return nil, err
	}
	if loaded.PlanHash != actualHash {
		return nil, fmt.Errorf("delivery state: plan hash не соответствует canonical plan")
	}
	if loaded.CommitSHA != "" && !gitHashPattern.MatchString(loaded.CommitSHA) {
		return nil, fmt.Errorf("delivery state: commit_sha невалиден")
	}
	if loaded.CommitVerified != (loaded.CommitSHA != "") || loaded.Pushed && !loaded.CommitVerified || loaded.PRURL != "" && !loaded.Pushed {
		return nil, fmt.Errorf("delivery state: невалидная последовательность эффектов")
	}
	return &loaded, nil
}

func archiveCompletedState(path string, data []byte) error {
	existing, err := safeio.ReadRegularFile(path, maxStateSize)
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("delivery state archive collision: %s", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func writeState(path string, value *state) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		_ = temp.Close()
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
