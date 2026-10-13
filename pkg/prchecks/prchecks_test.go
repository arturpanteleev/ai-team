package prchecks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

const testRepository = "github.com/example/project"

func TestProjectChecksUsesPinnedPRWorktreeAndStubGH(t *testing.T) {
	target, baseSHA, headSHA, url, bin := makePRFixture(t, `[ {"bucket":"pass","name":"unit"} ]`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	runIn(t, target, "git", "config", "http.extraheader", "Authorization: Bearer controller-git-config-sentinel")
	runner := Runner{SandboxFactory: func(candidateDir, trustedRepoDir string) (checks.CommandSandbox, error) {
		if trustedRepoDir != target {
			t.Fatalf("sandbox trusted repo path = %q, want %q", trustedRepoDir, target)
		}
		gitInfo, statErr := os.Stat(filepath.Join(candidateDir, ".git"))
		if statErr != nil || !gitInfo.IsDir() {
			t.Fatalf("candidate must have isolated .git directory, got info=%v err=%v", gitInfo, statErr)
		}
		config, readErr := os.ReadFile(filepath.Join(candidateDir, ".git", "config"))
		if readErr != nil || strings.Contains(string(config), "controller-git-config-sentinel") {
			t.Fatalf("candidate git config exposed controller credentials: %q err=%v", config, readErr)
		}
		return testCommandSandbox{}, nil
	}}
	status, reason, err := runner.Check(context.Background(), target, testRepository, url, "project_checks")
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

type testCommandSandbox struct{}

func (testCommandSandbox) Enforced() bool { return true }
func (testCommandSandbox) Close() error   { return nil }
func (testCommandSandbox) Command(ctx context.Context, targetDir, workingDir, executable string, args ...string) (*exec.Cmd, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = workingDir
	command.Env = os.Environ()
	return command, nil
}

func TestPRChecksGreenAndDiffRequireStablePRIdentity(t *testing.T) {
	t.Run("green", func(t *testing.T) {
		target, _, _, url, bin := makePRFixture(t, `[{"bucket":"pass","name":"unit"},{"bucket":"skipping","name":"optional"}]`)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		status, _, err := (Runner{}).Check(context.Background(), target, testRepository, url, "pr_checks_green")
		if err != nil || status != "passed" {
			t.Fatalf("green PR checks: status=%q err=%v", status, err)
		}
		diff, info, err := (Runner{}).Diff(context.Background(), target, testRepository, url)
		if err != nil || string(diff) != "diff --git a/main.go b/main.go\n" || info.HeadRefOID == "" {
			t.Fatalf("PR diff: diff=%q info=%+v err=%v", diff, info, err)
		}
	})
	t.Run("pending fails", func(t *testing.T) {
		target, _, _, url, bin := makePRFixture(t, `[{"bucket":"pending","name":"unit"}]`)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		status, reason, err := (Runner{}).Check(context.Background(), target, testRepository, url, "pr_checks_green")
		if err != nil || status != "failed" || !strings.Contains(reason, "pending") {
			t.Fatalf("pending CI must fail: status=%q reason=%q err=%v", status, reason, err)
		}
	})
}

func TestResolveRequiresPRFromTaskRepository(t *testing.T) {
	target, _, _, url, bin := makePRFixture(t, `[{"bucket":"pass","name":"unit"}]`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := (Runner{}).Resolve(context.Background(), target, "GitHub.com/Example/Project", url); err != nil {
		t.Fatalf("PR from the task repository (case-insensitive) must resolve: %v", err)
	}
	for _, repository := range []string{"github.com/example/other-project", "github.com/other-owner/project", ""} {
		status, _, err := (Runner{}).Check(context.Background(), target, repository, url, "pr_checks_green")
		if err == nil || status != "" {
			t.Errorf("green PR checked against task repository %q must be rejected: status=%q err=%v", repository, status, err)
		}
		if _, _, err := (Runner{}).Diff(context.Background(), target, repository, url); err == nil {
			t.Errorf("diff checked against task repository %q must be rejected", repository)
		}
	}
}

func TestResolveRejectsForeignPRURLs(t *testing.T) {
	const repository = "github.com/example/project"
	pull := func(url string) string {
		view, _ := json.Marshal(PullRequest{URL: url, State: "OPEN", BaseRefName: "main", BaseRefOID: strings.Repeat("a", 40), HeadRefName: "feature", HeadRefOID: strings.Repeat("b", 40)})
		return string(view)
	}
	for _, tc := range []struct {
		name, url string
		hosts     []string
	}{
		{name: "fork owner", url: "https://github.com/other-owner/project/pull/7"},
		{name: "other repository", url: "https://github.com/example/other/pull/7"},
		{name: "explicit port", url: "https://github.com:443/example/project/pull/7"},
		{name: "enterprise host", url: "https://ghe.example.com/example/project/pull/7", hosts: []string{"github.com", "ghe.example.com"}},
		{name: "userinfo", url: "https://user@github.com/example/project/pull/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := Runner{AllowedHosts: tc.hosts, Commands: &movingPRRunner{views: []string{pull(tc.url)}}}
			if _, err := runner.Resolve(context.Background(), t.TempDir(), repository, tc.url); err == nil {
				t.Fatalf("PR %s must be rejected for task repository %s", tc.url, repository)
			}
		})
	}
}

func TestTaskRepositoryReadsHardenedOrigin(t *testing.T) {
	isolateGitConfig(t)
	newRepo := func(t *testing.T, origin string) string {
		dir := t.TempDir()
		runIn(t, dir, "git", "init", "-q")
		if origin != "" {
			runIn(t, dir, "git", "remote", "add", "origin", origin)
		}
		return dir
	}
	for _, origin := range []string{
		"https://github.com/Example/Project.git",
		"git@github.com:example/project.git",
		"ssh://git@github.com/example/project",
	} {
		repository, err := (Runner{}).TaskRepository(context.Background(), newRepo(t, origin))
		if err != nil || repository != "github.com/example/project" {
			t.Errorf("origin %s: repository=%q err=%v", origin, repository, err)
		}
	}

	for _, tc := range []struct {
		name   string
		origin string
		tamper [][]string
	}{
		{name: "gh-resolved on another remote", origin: "https://github.com/example/project.git", tamper: [][]string{
			{"remote", "add", "up", "https://github.com/cli/cli.git"},
			{"config", "remote.up.gh-resolved", "base"},
		}},
		{name: "insteadOf rewrites origin", origin: "https://github.com/example/project.git", tamper: [][]string{
			{"config", "url.https://github.com/cli/cli.git.insteadOf", "https://github.com/example/project.git"},
		}},
		{name: "pushInsteadOf rewrites origin", origin: "https://github.com/example/project.git", tamper: [][]string{
			{"config", "url.https://github.com/cli/.pushInsteadOf", "https://github.com/"},
		}},
		{name: "second origin URL", origin: "https://github.com/example/project.git", tamper: [][]string{
			{"config", "--add", "remote.origin.url", "https://github.com/cli/cli.git"},
		}},
		{name: "untrusted host", origin: "https://gitlab.example.com/example/project.git"},
		{name: "local path", origin: "/srv/git/project.git"},
		{name: "no origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t, tc.origin)
			for _, args := range tc.tamper {
				runIn(t, dir, "git", args...)
			}
			if repository, err := (Runner{}).TaskRepository(context.Background(), dir); err == nil {
				t.Fatalf("tampered or non-GitHub origin must be rejected, got %q", repository)
			}
		})
	}
}

// isolateGitConfig keeps the developer's global Git configuration (for example
// a personal insteadOf rule) out of tests that inspect `git config --list`.
func isolateGitConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
}

func TestPRReferenceRejectsArbitraryURLAndFlag(t *testing.T) {
	for _, reference := range []string{"--repo", "http://github.com/a/b/pull/1", "https://example.com/a/b/issues/1"} {
		if _, err := pullNumber(reference); err == nil {
			t.Errorf("accepted invalid PR reference %q", reference)
		}
	}
}

func TestResolveRejectsUntrustedHostBeforeCallingGH(t *testing.T) {
	commands := &recordingCommandRunner{}
	for _, reference := range []string{
		"https://127.0.0.1:8443/acme/repo/pull/7",
		"https://attacker.example/acme/repo/pull/7",
	} {
		if _, err := (Runner{Commands: commands}).Resolve(context.Background(), t.TempDir(), testRepository, reference); err == nil {
			t.Errorf("untrusted PR host %q was accepted", reference)
		}
	}
	if commands.calls != 0 {
		t.Fatalf("gh was invoked for rejected hosts: %d calls", commands.calls)
	}
}

type recordingCommandRunner struct{ calls int }

func (r *recordingCommandRunner) Run(context.Context, string, string, ...string) delivery.StepResult {
	r.calls++
	return delivery.StepResult{Status: delivery.StepFailed, ExitCode: 1}
}

func TestPRChecksGreenRejectsHeadMovingDuringCheckQuery(t *testing.T) {
	url := "https://github.com/example/project/pull/7"
	before := PullRequest{URL: url, State: "OPEN", BaseRefName: "main", BaseRefOID: strings.Repeat("a", 40), HeadRefName: "feature", HeadRefOID: strings.Repeat("b", 40)}
	after := before
	after.HeadRefOID = strings.Repeat("c", 40)
	views, _ := json.Marshal(before)
	updated, _ := json.Marshal(after)
	runner := Runner{Commands: &movingPRRunner{views: []string{string(views), string(updated)}}}
	status, _, err := runner.Check(context.Background(), t.TempDir(), testRepository, url, "pr_checks_green")
	if err == nil || status != "" || !strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("moving PR head must error: status=%q err=%v", status, err)
	}
}

func TestPRChecksGreenRejectsIdentityDifferentFromPriorProjectCheck(t *testing.T) {
	url := "https://github.com/example/project/pull/7"
	current := PullRequest{URL: url, State: "OPEN", BaseRefName: "main", BaseRefOID: strings.Repeat("a", 40), HeadRefName: "feature", HeadRefOID: strings.Repeat("c", 40)}
	view, _ := json.Marshal(current)
	runner := Runner{Commands: &movingPRRunner{views: []string{string(view)}}}
	expected := current
	expected.HeadRefOID = strings.Repeat("b", 40)
	status, _, identity, err := runner.CheckIdentity(context.Background(), t.TempDir(), testRepository, url, "pr_checks_green", &expected)
	if err == nil || status != "" || identity.URL != "" || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("green check must reject a different cross-rule identity: status=%q identity=%+v err=%v", status, identity, err)
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
		if len(r.views) == 0 {
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
	ghScript := "#!/bin/sh\nset -eu\ncase \"$1 $2\" in\n" +
		"  'pr view') printf '%s\\n' '" + string(info) + "' ;;\n" +
		"  'pr checks') printf '%s\\n' '" + checksJSON + "' ;;\n" +
		"  'pr diff') printf '%s\\n' 'diff --git a/main.go b/main.go' ;;\n" +
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

// BL-21: the isolated checkout is verified against the pinned head SHA.
func TestIsolatedPRCheckoutVerifiesPinnedHead(t *testing.T) {
	target, baseSHA, headSHA, _, _ := makePRFixture(t, `[]`)
	runIn(t, target, "git", "update-ref", "refs/test/base", baseSHA)
	runIn(t, target, "git", "update-ref", "refs/test/head", headSHA)
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := createIsolatedPRCheckout(context.Background(), target, checkout, "refs/test/base", "refs/test/head", headSHA); err != nil {
		t.Fatalf("pinned head checkout: %v", err)
	}
	if got := gitOutput(t, checkout, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("checkout HEAD = %s, want %s", got, headSHA)
	}
	other := filepath.Join(t.TempDir(), "checkout")
	err := createIsolatedPRCheckout(context.Background(), target, other, "refs/test/base", "refs/test/head", baseSHA)
	if err == nil || !strings.Contains(err.Error(), "differs from pinned PR head") {
		t.Fatalf("checkout of a ref that does not match the pinned head was accepted: %v", err)
	}
}
