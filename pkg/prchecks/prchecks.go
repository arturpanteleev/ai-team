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
	"path/filepath"
	"regexp"
	"strings"

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
type Runner struct{ Commands delivery.CommandRunner }

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
	actualNumber, err := pullNumber(pull.URL)
	if err != nil || actualNumber != requestedNumber {
		return PullRequest{}, errors.New("gh pr view returned a different pull request than requested")
	}
	return pull, nil
}

// Check evaluates one B-44 rule. A false condition returns status "failed";
// transport, identity, or execution problems are returned as errors.
func (r Runner) Check(ctx context.Context, targetDir, reference, rule string) (status, reason string, err error) {
	switch rule {
	case "project_checks":
		return r.projectChecks(ctx, targetDir, reference)
	case "pr_checks_green":
		return r.prChecksGreen(ctx, targetDir, reference)
	default:
		return "", "", fmt.Errorf("unsupported PR check rule %q", rule)
	}
}

func (r Runner) prChecksGreen(ctx context.Context, targetDir, reference string) (string, string, error) {
	before, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return "", "", err
	}
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

func (r Runner) projectChecks(ctx context.Context, targetDir, reference string) (status, reason string, err error) {
	pull, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return "", "", err
	}
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
	worktreeAdded := false
	var worktreePath, tempRoot string
	fetched := true
	tempRoot, err = os.MkdirTemp("", "ai-team-pr-checks-")
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
		if worktreeAdded {
			cleanup := commands.Run(context.Background(), targetDir, "git", "worktree", "remove", "--force", worktreePath)
			if cleanup.Status != delivery.StepPassed {
				err = errors.Join(err, commandError("git worktree remove", cleanup))
			}
		}
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
	add := commands.Run(ctx, targetDir, "git", "worktree", "add", "--detach", worktreePath, headOID)
	if add.Status != delivery.StepPassed {
		if registered, checkErr := worktreeIsRegistered(commands, targetDir, worktreePath); checkErr == nil {
			worktreeAdded = registered
		}
		return "", "", commandError("git worktree add for PR", add)
	}
	worktreeAdded = true
	gateConfig, err := loadTrustedBaseGateConfig(ctx, commands, targetDir, baseOID)
	if err != nil {
		return "", "", err
	}
	result, exitCode, gateErr := gate.Run(ctx, gate.Options{
		TargetDir: worktreePath, Base: baseOID, Candidate: "WORKTREE", Config: gateConfig,
	})
	if gateErr != nil {
		return "", "", gateErr
	}
	if exitCode != gate.ExitPass || result.Status != "passed" {
		return "failed", fmt.Sprintf("ai-team gate status=%s policy=%s", result.Status, result.PolicyVerdict), nil
	}
	return "passed", fmt.Sprintf("ai-team gate passed for head %s against base %s", headOID, baseOID), nil
}

func worktreeIsRegistered(commands delivery.CommandRunner, target, worktree string) (bool, error) {
	result := commands.Run(context.Background(), target, "git", "worktree", "list", "--porcelain")
	if result.Status != delivery.StepPassed {
		return false, commandError("git worktree list", result)
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if line == "worktree "+worktree {
			return true, nil
		}
	}
	return false, nil
}

// Diff returns gh's PR diff only when the exact head SHA remains stable around
// the network request. The result is then written as immutable attempt input.
func (r Runner) Diff(ctx context.Context, targetDir, reference string) ([]byte, PullRequest, error) {
	before, err := r.Resolve(ctx, targetDir, reference)
	if err != nil {
		return nil, PullRequest{}, err
	}
	if before.State != "OPEN" {
		return nil, PullRequest{}, fmt.Errorf("pull request state is %q, expected OPEN", before.State)
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
	if before.URL != after.URL || before.HeadRefOID != after.HeadRefOID || before.BaseRefOID != after.BaseRefOID {
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
