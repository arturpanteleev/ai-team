package prchecks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

func TestProjectChecksUsesPinnedPRWorktreeAndStubGH(t *testing.T) {
	target, baseSHA, headSHA, url, bin := makePRFixture(t, `[ {"bucket":"pass","name":"unit"} ]`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	status, reason, err := (Runner{}).Check(context.Background(), target, url, "project_checks")
	if err != nil || status != "passed" || !strings.Contains(reason, headSHA) {
		t.Fatalf("project checks: status=%q reason=%q err=%v", status, reason, err)
	}
	if got := gitOutput(t, target, "rev-parse", "HEAD"); got != baseSHA {
		t.Fatalf("controller worktree moved from base after check: got %s want %s", got, baseSHA)
	}
	worktrees := gitOutput(t, target, "worktree", "list", "--porcelain")
	if strings.Contains(worktrees, "ai-team-pr-checks-") {
		t.Fatalf("temporary PR worktree leaked: %s", worktrees)
	}
	if _, err := os.Stat(filepath.Join(target, "main.go")); err != nil {
		t.Fatalf("candidate checkout escaped into controller worktree: %v", err)
	}
}

func TestPRChecksGreenAndDiffRequireStablePRIdentity(t *testing.T) {
	t.Run("green", func(t *testing.T) {
		target, _, _, url, bin := makePRFixture(t, `[{"bucket":"pass","name":"unit"},{"bucket":"skipping","name":"optional"}]`)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		status, _, err := (Runner{}).Check(context.Background(), target, url, "pr_checks_green")
		if err != nil || status != "passed" {
			t.Fatalf("green PR checks: status=%q err=%v", status, err)
		}
		diff, info, err := (Runner{}).Diff(context.Background(), target, url)
		if err != nil || string(diff) != "diff --git a/main.go b/main.go\n" || info.HeadRefOID == "" {
			t.Fatalf("PR diff: diff=%q info=%+v err=%v", diff, info, err)
		}
	})
	t.Run("pending fails", func(t *testing.T) {
		target, _, _, url, bin := makePRFixture(t, `[{"bucket":"pending","name":"unit"}]`)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		status, reason, err := (Runner{}).Check(context.Background(), target, url, "pr_checks_green")
		if err != nil || status != "failed" || !strings.Contains(reason, "pending") {
			t.Fatalf("pending CI must fail: status=%q reason=%q err=%v", status, reason, err)
		}
	})
}

func TestPRReferenceRejectsArbitraryURLAndFlag(t *testing.T) {
	for _, reference := range []string{"--repo", "http://github.com/a/b/pull/1", "https://example.com/a/b/issues/1"} {
		if _, err := pullNumber(reference); err == nil {
			t.Errorf("accepted invalid PR reference %q", reference)
		}
	}
}

func TestPRChecksGreenRejectsHeadMovingDuringCheckQuery(t *testing.T) {
	url := "https://github.com/example/project/pull/7"
	before := PullRequest{URL: url, State: "OPEN", BaseRefName: "main", BaseRefOID: strings.Repeat("a", 40), HeadRefName: "feature", HeadRefOID: strings.Repeat("b", 40)}
	after := before
	after.HeadRefOID = strings.Repeat("c", 40)
	views, _ := json.Marshal(before)
	updated, _ := json.Marshal(after)
	runner := Runner{Commands: &movingPRRunner{views: []string{string(views), string(updated)}}}
	status, _, err := runner.Check(context.Background(), t.TempDir(), url, "pr_checks_green")
	if err == nil || status != "" || !strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("moving PR head must error: status=%q err=%v", status, err)
	}
}

type movingPRRunner struct{ views []string }

func (r *movingPRRunner) Run(_ context.Context, _ string, name string, args ...string) delivery.StepResult {
	result := delivery.StepResult{Status: delivery.StepPassed, ExitCode: 0}
	if name != "gh" || len(args) < 2 {
		return delivery.StepResult{Status: delivery.StepFailed, ExitCode: 2, Stderr: "unexpected command"}
	}
	switch args[1] {
	case "view":
		index := len(r.views) - 1
		if index < 0 {
			return delivery.StepResult{Status: delivery.StepFailed, ExitCode: 2, Stderr: "missing view fixture"}
		}
		result.Stdout = r.views[0]
		r.views = r.views[1:]
		return result
	case "checks":
		result.Stdout = `[{"bucket":"pass","name":"unit"}]`
		return result
	default:
		return delivery.StepResult{Status: delivery.StepFailed, ExitCode: 2, Stderr: "unexpected gh command"}
	}
}

func makePRFixture(t *testing.T, checksJSON string) (target, baseSHA, headSHA, url, bin string) {
	t.Helper()
	target = t.TempDir()
	bare := filepath.Join(t.TempDir(), "remote.git")
	run(t, "git", "init", "--bare", bare)
	runIn(t, target, "git", "init", "-b", "main")
	runIn(t, target, "git", "config", "user.name", "PR checks test")
	runIn(t, target, "git", "config", "user.email", "prchecks@example.test")
	write(t, filepath.Join(target, "main.go"), "package main\nfunc Main() int { return 1 }\n")
	write(t, filepath.Join(target, "gate.yaml"), `schema_version: 1
diff_policy:
  test_modify: warning
checks:
  - name: base-gate
    class: build
    command: [sh, -c, "test -f main.go"]
    policy: required
`)
	runIn(t, target, "git", "add", "main.go")
	runIn(t, target, "git", "add", "gate.yaml")
	runIn(t, target, "git", "commit", "-m", "base")
	baseSHA = gitOutput(t, target, "rev-parse", "HEAD")
	runIn(t, target, "git", "remote", "add", "origin", bare)
	runIn(t, target, "git", "push", "-u", "origin", "main")
	runIn(t, target, "git", "switch", "-c", "feature")
	write(t, filepath.Join(target, "gate.yaml"), `schema_version: 1
diff_policy:
  test_modify: off
checks: []
`)
	write(t, filepath.Join(target, "main.go"), "package main\nfunc Main() int { return 2 }\n")
	runIn(t, target, "git", "add", "gate.yaml", "main.go")
	runIn(t, target, "git", "commit", "-m", "candidate")
	headSHA = gitOutput(t, target, "rev-parse", "HEAD")
	runIn(t, target, "git", "push", "origin", "feature")
	run(t, "git", "--git-dir", bare, "update-ref", "refs/pull/7/head", headSHA)
	runIn(t, target, "git", "switch", "main")
	url = "https://github.com/example/project/pull/7"
	info, _ := json.Marshal(PullRequest{URL: url, State: "OPEN", BaseRefName: "main", BaseRefOID: baseSHA, HeadRefName: "feature", HeadRefOID: headSHA})
	bin = t.TempDir()
	ghScript := "#!/bin/sh\nset -eu\ncase \"$2\" in\n" +
		"  view) printf '%s\\n' '" + string(info) + "' ;;\n" +
		"  checks) printf '%s\\n' '" + checksJSON + "' ;;\n" +
		"  diff) printf '%s\\n' 'diff --git a/main.go b/main.go' ;;\n" +
		"  *) exit 2 ;;\nesac\n"
	write(t, filepath.Join(bin, "gh"), ghScript)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	return target, baseSHA, headSHA, url, bin
}

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("(%s) %s %s: %v\n%s", dir, name, strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}
