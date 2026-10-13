//go:build linux

package checks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBubblewrapHidesCommonGitdirAndCustomTMPDIR(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is unavailable")
	}

	trustedRepo := makeSandboxTestTempDir(t, "trusted")
	runGitSandboxTest(t, trustedRepo, "init", "--quiet", "--initial-branch=main")
	runGitSandboxTest(t, trustedRepo, "config", "user.name", "sandbox test")
	runGitSandboxTest(t, trustedRepo, "config", "user.email", "sandbox@example.test")
	if err := os.WriteFile(filepath.Join(trustedRepo, "trusted.txt"), []byte("trusted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSandboxTest(t, trustedRepo, "add", "trusted.txt")
	runGitSandboxTest(t, trustedRepo, "commit", "--quiet", "-m", "trusted")
	runGitSandboxTest(t, trustedRepo, "config", "http.extraheader", "Authorization: Bearer controller-git-sentinel")

	linkedRepo := filepath.Join(makeSandboxTestTempDir(t, "linked-root"), "controller-worktree")
	runGitSandboxTest(t, trustedRepo, "worktree", "add", "--quiet", "--detach", linkedRepo, "HEAD")
	commonGitDir := gitOutputSandboxTest(t, trustedRepo, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(commonGitDir) {
		commonGitDir = filepath.Join(trustedRepo, commonGitDir)
	}
	credentialConfig := filepath.Join(commonGitDir, "config")
	config, err := os.ReadFile(credentialConfig)
	if err != nil || !strings.Contains(string(config), "controller-git-sentinel") {
		t.Fatalf("sentinel missing from controller config: %q err=%v", config, err)
	}

	candidate := makeSandboxTestTempDir(t, "candidate")
	runGitSandboxTest(t, candidate, "init", "--quiet", "--initial-branch=main")
	runGitSandboxTest(t, candidate, "config", "user.name", "candidate")
	runGitSandboxTest(t, candidate, "config", "user.email", "candidate@example.test")
	if err := os.WriteFile(filepath.Join(candidate, "candidate.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSandboxTest(t, candidate, "add", "candidate.txt")
	runGitSandboxTest(t, candidate, "commit", "--quiet", "-m", "candidate")

	customTemp := makeSandboxTestTempDir(t, "custom-tmpdir")
	tempCredential := filepath.Join(customTemp, "runner-credential")
	if err := os.WriteFile(tempCredential, []byte("tmpdir-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", customTemp)
	for _, name := range []string{"TMP", "TEMP"} {
		t.Setenv(name, "")
	}

	sandbox, err := NewUntrustedCommandSandbox(candidate, linkedRepo)
	if err != nil {
		t.Fatalf("initialize bubblewrap sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sandbox.Close() })
	if !sandbox.Enforced() {
		t.Fatal("sandbox did not confirm enforcement")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	trustedFile := filepath.Join(trustedRepo, "trusted.txt")
	script := `"$1" rev-parse --verify HEAD >/dev/null 2>&1 || exit 10
test -f candidate.txt || exit 11
test ! -r "$2" || exit 12
test ! -r "$3" || exit 13
test ! -r "$4" || exit 14`
	command, err := sandbox.Command(context.Background(), candidate, candidate, shPath,
		"-c", script, "sh", gitPath, credentialConfig, tempCredential, trustedFile)
	if err != nil {
		t.Fatalf("create sandbox probe: %v", err)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox must allow candidate Git checks and hide controller/TMPDIR credentials: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "controller-git-sentinel") || strings.Contains(string(output), "tmpdir-sentinel") {
		t.Fatalf("sandbox probe leaked a sentinel: %q", output)
	}
}

func makeSandboxTestTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "ai-team-pr-check-"+prefix+"-")
	if err != nil {
		t.Fatalf("create test directory outside /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runGitSandboxTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutputSandboxTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

// BL-21: the sandbox command builder rejects a tool under the masked home
// before bubblewrap runs, naming the tool and the hiding mount.
func TestBubblewrapCommandRejectsToolUnderMaskedHome(t *testing.T) {
	home := makeSandboxTestTempDir(t, "home")
	t.Setenv("HOME", home)
	goBin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(goBin, "golangci-lint")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := makeSandboxTestTempDir(t, "candidate")
	sandbox := &bubblewrapSandbox{bwrap: "/usr/bin/bwrap", root: makeSandboxTestTempDir(t, "root"), target: target}
	if _, err := sandbox.Command(context.Background(), target, target, tool, "run"); err == nil ||
		!strings.Contains(err.Error(), tool) || !strings.Contains(err.Error(), "hidden inside the untrusted-check sandbox") {
		t.Fatalf("tool under masked HOME must fail explicitly: %v", err)
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.Command(context.Background(), target, target, shPath, "-c", "true"); err != nil {
		t.Fatalf("system tool must remain usable: %v", err)
	}
}
