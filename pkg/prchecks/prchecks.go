// Package prchecks implements controller-side checks that bind a pull request
// to immutable GitHub commit identities before inspecting its diff or running
// project checks in a detached worktree.
package prchecks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/gate"
	"gopkg.in/yaml.v3"
)

const maxDiffBytes = 8 << 20

var positiveNumber = regexp.MustCompile(`^[1-9][0-9]*$`)
var fullSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

// PullRequest is the GitHub identity used to pin review and check work.
type PullRequest struct {
	URL         string `json:"url"`
	State       string `json:"state"`
	BaseRefName string `json:"baseRefName"`
	BaseRefOID  string `json:"baseRefOid"`
	HeadRefName string `json:"headRefName"`
	HeadRefOID  string `json:"headRefOid"`
}

// Runner uses gh and git through the same non-interactive command boundary as
// delivery. A nil command runner uses delivery.ExecRunner.
type Runner struct {
	Commands delivery.CommandRunner
	// AllowedHosts contains trusted GitHub or GitHub Enterprise host names.
	// The default is github.com; callers must opt in to any enterprise host.
	AllowedHosts []string
	// SandboxFactory exists to inject a deterministic test sandbox. Production
	// callers leave it nil and require the platform's enforced implementation.
	SandboxFactory func(candidateDir, trustedRepoDir string) (checks.CommandSandbox, error)
}

func (r Runner) commands() delivery.CommandRunner {
	if r.Commands != nil {
		return r.Commands
	}
	return delivery.ExecRunner{}
}

// Resolve reads and validates the exact pull request identity from gh.
func (r Runner) Resolve(ctx context.Context, targetDir, reference string) (PullRequest, error) {
	requestedNumber, err := pullNumber(reference)
	if err != nil {
		return PullRequest{}, err
	}
	requestedHost, err := r.referenceHost(reference)
	if err != nil {
		return PullRequest{}, err
	}
	result := r.commands().Run(ctx, targetDir, "gh", "pr", "view", reference, "--json",
		"url,state,baseRefName,baseRefOid,headRefName,headRefOid")
	if result.Status != delivery.StepPassed {
		return PullRequest{}, commandError("gh pr view", result)
	}
	if result.Truncated {
		return PullRequest{}, errors.New("gh pr view output was truncated")
	}
	var pull PullRequest
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pull); err != nil {
		return PullRequest{}, fmt.Errorf("invalid gh pr view JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return PullRequest{}, errors.New("invalid gh pr view JSON: multiple values")
		}
		return PullRequest{}, fmt.Errorf("invalid gh pr view JSON trailing data: %w", err)
	}
	if err := validateInfo(pull); err != nil {
		return PullRequest{}, err
	}
	resolvedHost, err := r.trustedHost(pull.URL)
	if err != nil {
		return PullRequest{}, fmt.Errorf("gh pr view returned an untrusted pull request host: %w", err)
	}
	if requestedHost != "" && requestedHost != resolvedHost {
		return PullRequest{}, errors.New("gh pr view returned a pull request on a different host than requested")
	}
	actualNumber, err := pullNumber(pull.URL)
	if err != nil || actualNumber != requestedNumber {
		return PullRequest{}, errors.New("gh pr view returned a different pull request than requested")
	}
	return pull, nil
}

// Check evaluates one B-44 rule. A false condition returns status "failed";
// transport, identity, or execution problems are returned as errors.
func (r Runner) Check(ctx context.Context, targetDir, reference, rule string) (status, reason string, err error) {
	status, reason, _, err = r.CheckIdentity(ctx, targetDir, reference, rule, nil)
	return status, reason, err
}

// CheckIdentity evaluates a rule against an optional identity established by a
// prior rule in the same review. It returns the identity actually checked so
// callers can persist and bind later review stages to it.
func (r Runner) CheckIdentity(ctx context.Context, targetDir, reference, rule string, expected *PullRequest) (status, reason string, identity PullRequest, err error) {
	pull, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return "", "", PullRequest{}, err
	}
	if expected != nil && !sameIdentity(*expected, pull) {
		return "", "", PullRequest{}, errors.New("pull request identity changed between project checks and review; retry against stable head/base SHAs")
	}
	switch rule {
	case "project_checks":
		status, reason, err = r.projectChecks(ctx, targetDir, pull)
	case "pr_checks_green":
		status, reason, err = r.prChecksGreen(ctx, targetDir, reference, pull)
	default:
		err = fmt.Errorf("unsupported PR check rule %q", rule)
	}
	if err != nil {
		return "", "", PullRequest{}, err
	}
	return status, reason, pull, nil
}

func (r Runner) prChecksGreen(ctx context.Context, targetDir, reference string, before PullRequest) (string, string, error) {
	result := r.commands().Run(ctx, targetDir, "gh", "pr", "checks", reference, "--json", "bucket,name")
	if result.Truncated {
		return "", "", errors.New("gh pr checks output was truncated")
	}
	var entries []struct {
		Bucket string `json:"bucket"`
		Name   string `json:"name"`
	}
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return "", "", fmt.Errorf("invalid gh pr checks JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", "", errors.New("invalid gh pr checks JSON: trailing data")
	}
	if len(entries) == 0 {
		if result.Status != delivery.StepPassed {
			return "", "", commandError("gh pr checks", result)
		}
		return "failed", "gh pr checks returned no check runs", nil
	}
	var reasons []string
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return "", "", errors.New("gh pr checks returned a check without a name")
		}
		switch strings.ToLower(entry.Bucket) {
		case "pass", "skipping":
		default:
			reasons = append(reasons, fmt.Sprintf("%s: %s", name, entry.Bucket))
		}
	}
	if result.Status != delivery.StepPassed && len(reasons) == 0 {
		return "", "", commandError("gh pr checks", result)
	}
	after, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return "", "", err
	}
	if !sameIdentity(before, after) {
		return "", "", errors.New("pull request changed while reading check runs; retry against stable head/base SHAs")
	}
	if len(reasons) > 0 {
		return "failed", fmt.Sprintf("head %s: %s", before.HeadRefOID, strings.Join(reasons, "; ")), nil
	}
	return "passed", "all PR checks are green for head " + before.HeadRefOID, nil
}

func (r Runner) projectChecks(ctx context.Context, targetDir string, pull PullRequest) (status, reason string, err error) {
	if pull.State != "OPEN" {
		return "failed", fmt.Sprintf("pull request state is %q, expected OPEN", pull.State), nil
	}
	number, err := pullNumber(pull.URL)
	if err != nil {
		return "", "", err
	}
	keyDigest := sha256.Sum256([]byte(pull.URL + "\x00" + pull.BaseRefOID + "\x00" + pull.HeadRefOID))
	key := hex.EncodeToString(keyDigest[:8])
	baseRef := "refs/ai-team/pr-checks/" + key + "/base"
	headRef := "refs/ai-team/pr-checks/" + key + "/head"
	baseSource := "refs/heads/" + pull.BaseRefName
	headSource := "refs/pull/" + number + "/head"
	if err := validateGitBranch(ctx, r.commands(), targetDir, pull.BaseRefName); err != nil {
		return "", "", err
	}

	commands := r.commands()
	var worktreePath, tempRoot string
	fetched := true
	tempBase := os.TempDir()
	if runtime.GOOS == "linux" {
		// bubblewrap replaces /tmp with a private mount. Keep both the isolated
		// repository and its scratch files there even when TMPDIR is customized.
		tempBase = "/tmp"
	}
	tempRoot, err = os.MkdirTemp(tempBase, "ai-team-pr-checks-")
	if err != nil {
		return "", "", err
	}
	// Include a per-invocation suffix so concurrent checks of the same PR do
	// not update or delete one another's temporary refs.
	key += "-" + strings.TrimPrefix(filepath.Base(tempRoot), "ai-team-pr-checks-")
	baseRef = "refs/ai-team/pr-checks/" + key + "/base"
	headRef = "refs/ai-team/pr-checks/" + key + "/head"
	worktreePath = filepath.Join(tempRoot, "worktree")
	defer func() {
		if fetched {
			cleanup := commands.Run(context.Background(), targetDir, "git", "update-ref", "-d", baseRef)
			if cleanup.Status != delivery.StepPassed {
				err = errors.Join(err, commandError("git update-ref base", cleanup))
			}
			cleanup = commands.Run(context.Background(), targetDir, "git", "update-ref", "-d", headRef)
			if cleanup.Status != delivery.StepPassed {
				err = errors.Join(err, commandError("git update-ref head", cleanup))
			}
		}
		if tempRoot != "" {
			if cleanup := os.RemoveAll(tempRoot); cleanup != nil {
				err = errors.Join(err, cleanup)
			}
		}
	}()
	fetch := commands.Run(ctx, targetDir, "git", "fetch", "--no-tags", "origin",
		"+"+baseSource+":"+baseRef, "+"+headSource+":"+headRef)
	if fetch.Status != delivery.StepPassed {
		return "", "", commandError("fetch PR base and head", fetch)
	}
	baseOID, err := revParse(ctx, commands, targetDir, baseRef)
	if err != nil {
		return "", "", err
	}
	headOID, err := revParse(ctx, commands, targetDir, headRef)
	if err != nil {
		return "", "", err
	}
	if baseOID != pull.BaseRefOID || headOID != pull.HeadRefOID {
		return "", "", errors.New("pull request changed while preparing project checks; retry against stable head/base SHAs")
	}
	if err := createIsolatedPRCheckout(ctx, targetDir, worktreePath, baseRef, headRef); err != nil {
		return "", "", fmt.Errorf("create isolated PR checkout: %w", err)
	}
	gateConfig, err := loadTrustedBaseGateConfig(ctx, commands, targetDir, baseOID)
	if err != nil {
		return "", "", err
	}
	sandbox, err := r.newSandbox(worktreePath, targetDir)
	if err != nil {
		return "", "", fmt.Errorf("project checks blocked: %w", err)
	}
	if sandbox == nil || !sandbox.Enforced() {
		if sandbox != nil {
			_ = sandbox.Close()
		}
		return "", "", errors.New("project checks blocked: sandbox did not confirm OS enforcement")
	}
	result, exitCode, gateErr := gate.Run(ctx, gate.Options{
		TargetDir: worktreePath, Base: baseOID, Candidate: "WORKTREE", Config: gateConfig,
		AllowUntrusted: true, Receipt: strictSandboxReceipt(), CheckSandbox: sandbox,
	})
	closeErr := sandbox.Close()
	if closeErr != nil {
		gateErr = errors.Join(gateErr, fmt.Errorf("close PR check sandbox: %w", closeErr))
	}
	if gateErr != nil {
		return "", "", gateErr
	}
	after, err := r.Resolve(ctx, targetDir, pull.URL)
	if err != nil {
		return "", "", err
	}
	if !sameIdentity(pull, after) {
		return "", "", errors.New("pull request changed while project checks were running; retry against stable head/base SHAs")
	}
	if exitCode != gate.ExitPass || result.Status != "passed" {
		return "failed", fmt.Sprintf("ai-team gate status=%s policy=%s", result.Status, result.PolicyVerdict), nil
	}
	return "passed", fmt.Sprintf("ai-team gate passed for head %s against base %s", headOID, baseOID), nil
}

func (r Runner) newSandbox(targetDir, trustedRepoDir string) (checks.CommandSandbox, error) {
	if r.SandboxFactory != nil {
		return r.SandboxFactory(targetDir, trustedRepoDir)
	}
	return checks.NewUntrustedCommandSandbox(targetDir, trustedRepoDir)
}

// createIsolatedPRCheckout builds a fresh repository containing only the
// pinned base/head commits. A linked Git worktree would point its .git file at
// the controller's common gitdir, exposing controller configuration to an
// untrusted check even when the checkout itself is mounted separately.
func createIsolatedPRCheckout(ctx context.Context, sourceDir, checkoutDir, baseRef, headRef string) error {
	if err := runIsolatedGit(ctx, filepath.Dir(checkoutDir), "init", "--quiet", checkoutDir); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := runIsolatedGit(ctx, checkoutDir, "fetch", "--no-tags", "--no-write-fetch-head", sourceDir,
		"+"+baseRef+":refs/ai-team/pr-checks/base", "+"+headRef+":refs/ai-team/pr-checks/head"); err != nil {
		return fmt.Errorf("git fetch pinned commits: %w", err)
	}
	if err := runIsolatedGit(ctx, checkoutDir, "checkout", "--detach", "refs/ai-team/pr-checks/head"); err != nil {
		return fmt.Errorf("git checkout pinned head: %w", err)
	}
	return nil
}

func runIsolatedGit(ctx context.Context, dir string, args ...string) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("locate git: %w", err)
	}
	command := exec.CommandContext(ctx, gitPath, args...)
	command.Dir = dir
	command.Env = isolatedGitEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func isolatedGitEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
}

func strictSandboxReceipt() *containment.Receipt {
	return &containment.Receipt{
		Profile: "strict",
		Axes: map[containment.Axis]containment.Level{
			containment.AxisFS: containment.LevelENFORCED, containment.AxisNet: containment.LevelENFORCED,
			containment.AxisProc: containment.LevelENFORCED, containment.AxisEnv: containment.LevelENFORCED,
		},
		Details: map[containment.Axis]map[string]bool{
			containment.AxisFS:   {"read_only_root": true, "writable_checkout_only": true, "private_home": true},
			containment.AxisNet:  {"network_namespace": true},
			containment.AxisProc: {"pid_namespace": true, "die_with_parent": true},
			containment.AxisEnv:  {"secret_environment_stripped": true, "clean_inner_environment": true},
		},
	}
}

// Diff returns gh's PR diff only when the exact head SHA remains stable around
// the network request. The result is then written as immutable attempt input.
func (r Runner) Diff(ctx context.Context, targetDir, reference string) ([]byte, PullRequest, error) {
	return r.DiffIdentity(ctx, targetDir, reference, nil)
}

// DiffIdentity reads the PR diff only if the exact identity still matches the
// one accepted by the project's check rules.
func (r Runner) DiffIdentity(ctx context.Context, targetDir, reference string, expected *PullRequest) ([]byte, PullRequest, error) {
	before, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return nil, PullRequest{}, err
	}
	if before.State != "OPEN" {
		return nil, PullRequest{}, fmt.Errorf("pull request state is %q, expected OPEN", before.State)
	}
	if expected != nil && !sameIdentity(*expected, before) {
		return nil, PullRequest{}, errors.New("pull request identity differs from the identity accepted by project checks")
	}
	result := r.commands().Run(ctx, targetDir, "gh", "pr", "diff", reference)
	if result.Status != delivery.StepPassed {
		return nil, PullRequest{}, commandError("gh pr diff", result)
	}
	if result.Truncated || len(result.Stdout) == 0 || len(result.Stdout) > maxDiffBytes {
		return nil, PullRequest{}, fmt.Errorf("gh pr diff is empty or exceeds %d bytes", maxDiffBytes)
	}
	after, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return nil, PullRequest{}, err
	}
	if !sameIdentity(before, after) {
		return nil, PullRequest{}, errors.New("pull request changed while reading its diff; retry review against stable SHAs")
	}
	return []byte(result.Stdout), before, nil
}

func sameIdentity(left, right PullRequest) bool {
	return left.URL == right.URL && left.BaseRefOID == right.BaseRefOID && left.HeadRefOID == right.HeadRefOID
}

func loadTrustedBaseGateConfig(ctx context.Context, commands delivery.CommandRunner, target, baseOID string) (*gate.Config, error) {
	listed := commands.Run(ctx, target, "git", "ls-tree", "-r", "--name-only", baseOID, "--", "gate.yaml", ".ai-team/gate.yaml")
	if listed.Status != delivery.StepPassed || listed.Truncated {
		return nil, commandError("list gate config in pinned PR base", listed)
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(listed.Stdout, "\n") {
		paths[strings.TrimSpace(line)] = true
	}
	for _, path := range []string{"gate.yaml", ".ai-team/gate.yaml"} {
		if !paths[path] {
			continue
		}
		result := commands.Run(ctx, target, "git", "show", baseOID+":"+path)
		if result.Status != delivery.StepPassed || result.Truncated || strings.TrimSpace(result.Stdout) == "" {
			return nil, commandError("read gate config from pinned PR base", result)
		}
		decoder := yaml.NewDecoder(strings.NewReader(result.Stdout))
		var cfg gate.Config
		if err := decoder.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("parse gate config from pinned PR base %s: %w", path, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, errors.New("gate config in pinned PR base contains multiple documents")
		}
		return &cfg, nil
	}
	// A PR cannot add or edit its way around the project's gate policy: when
	// the base has no gate.yaml, use the same safe default as `ai-team gate`.
	return &gate.Config{SchemaVersion: gate.SchemaVersion, DiffPolicy: gate.DiffPolicy{TestModify: gate.TestModifyRequired}}, nil
}

func validateGitBranch(ctx context.Context, commands delivery.CommandRunner, target, branch string) error {
	result := commands.Run(ctx, target, "git", "check-ref-format", "--branch", branch)
	if result.Status != delivery.StepPassed || strings.TrimSpace(result.Stdout) == "" {
		return fmt.Errorf("invalid pull request base branch %q", branch)
	}
	return nil
}

func revParse(ctx context.Context, commands delivery.CommandRunner, target, ref string) (string, error) {
	result := commands.Run(ctx, target, "git", "rev-parse", "--verify", ref+"^{commit}")
	if result.Status != delivery.StepPassed {
		return "", commandError("git rev-parse", result)
	}
	value := strings.TrimSpace(result.Stdout)
	if !fullSHA.MatchString(value) {
		return "", fmt.Errorf("git rev-parse returned invalid commit %q", value)
	}
	return strings.ToLower(value), nil
}

func validateInfo(pull PullRequest) error {
	if err := validatePRURL(pull.URL); err != nil {
		return err
	}
	if pull.State != "OPEN" && pull.State != "CLOSED" && pull.State != "MERGED" {
		return fmt.Errorf("gh pr view returned invalid state %q", pull.State)
	}
	if pull.BaseRefName == "" || pull.HeadRefName == "" || !fullSHA.MatchString(pull.BaseRefOID) || !fullSHA.MatchString(pull.HeadRefOID) {
		return errors.New("gh pr view did not return stable base/head commit identities")
	}
	return nil
}

func pullNumber(reference string) (string, error) {
	value := strings.TrimSpace(reference)
	if positiveNumber.MatchString(value) {
		return value, nil
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("PR reference must be a positive number or HTTPS pull request URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[len(parts)-2] != "pull" || !positiveNumber.MatchString(parts[len(parts)-1]) {
		return "", errors.New("PR reference must end with /pull/<number>")
	}
	return parts[len(parts)-1], nil
}

func validatePRURL(value string) error {
	if _, err := pullNumber(value); err != nil {
		return fmt.Errorf("invalid pull request URL from gh: %w", err)
	}
	return nil
}

func (r Runner) referenceHost(reference string) (string, error) {
	if positiveNumber.MatchString(strings.TrimSpace(reference)) {
		return "", nil
	}
	host, err := r.trustedHost(reference)
	if err != nil {
		return "", fmt.Errorf("PR reference host is not trusted: %w", err)
	}
	return host, nil
}

func (r Runner) trustedHost(value string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("expected an HTTPS GitHub pull request URL")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Host, "."))
	allowed := r.AllowedHosts
	if len(allowed) == 0 {
		allowed = []string{"github.com"}
	}
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
		if candidate != "" && host == candidate {
			return host, nil
		}
	}
	return "", fmt.Errorf("host %q is not in the trusted PR host allowlist", host)
}

func commandError(label string, result delivery.StepResult) error {
	message := strings.TrimSpace(result.Stderr)
	if message == "" {
		message = strings.TrimSpace(result.Reason)
	}
	if message == "" {
		message = "command failed"
	}
	return fmt.Errorf("%s failed (exit %d): %s", label, result.ExitCode, message)
}
