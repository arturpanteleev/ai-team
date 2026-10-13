package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedRepositoryPathsIncludesLinkedCommonGitdirAndCustomTemp(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "linked-checkout")
	parentRepo := filepath.Join(base, "controller")
	common := filepath.Join(parentRepo, ".git")
	admin := filepath.Join(common, "worktrees", "linked-checkout")
	tempOverride := filepath.Join(base, "custom-temp")
	for _, path := range []string{root, admin, common, parentRepo, tempOverride} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../controller/.git/worktrees/linked-checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempOverride)
	t.Setenv("TMP", "")
	t.Setenv("TEMP", "")

	paths, err := trustedRepositoryPaths(root)
	if err != nil {
		t.Fatalf("resolve trusted paths: %v", err)
	}
	for _, want := range []string{root, parentRepo, tempOverride} {
		resolved, resolveErr := filepath.EvalSymlinks(want)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		found := false
		for _, got := range paths {
			if got == resolved {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("protected paths %v do not include %s", paths, resolved)
		}
	}
}

func TestCompactPathsDropsNestedMounts(t *testing.T) {
	got := compactPaths([]string{"/workspace/repo/.git", "/workspace/repo", "/home/runner"})
	if len(got) != 2 || got[0] != "/home/runner" || got[1] != "/workspace/repo" {
		t.Fatalf("compact paths = %v", got)
	}
}

// BL-21: a host tool hidden by the sandbox mounts (such as ~/go/bin) yields an
// explicit error instead of bubblewrap's bare "No such file or directory".
func TestCheckSandboxToolVisibleRejectsHiddenTools(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home", "dev")
	goBin := filepath.Join(home, "go", "bin")
	visibleBin := filepath.Join(base, "usr", "local", "bin")
	target := filepath.Join(home, "checkout")
	for _, dir := range []string{goBin, visibleBin, filepath.Join(target, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{filepath.Join(goBin, "golangci-lint"), filepath.Join(visibleBin, "lint"), filepath.Join(target, "bin", "tool")} {
		if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linked := filepath.Join(visibleBin, "linked-lint")
	if err := os.Symlink(filepath.Join(goBin, "golangci-lint"), linked); err != nil {
		t.Fatal(err)
	}
	hidden := []string{filepath.Join(base, "home")}

	err = checkSandboxToolVisible(target, filepath.Join(goBin, "golangci-lint"), hidden)
	if err == nil || !strings.Contains(err.Error(), "golangci-lint") || !strings.Contains(err.Error(), "hidden inside the untrusted-check sandbox") {
		t.Fatalf("tool under a masked home must fail explicitly: %v", err)
	}
	if err := checkSandboxToolVisible(target, linked, hidden); err == nil {
		t.Fatal("symlink into a masked home must fail explicitly")
	}
	if err := checkSandboxToolVisible(target, filepath.Join(visibleBin, "lint"), hidden); err != nil {
		t.Fatalf("tool outside masked paths must be accepted: %v", err)
	}
	if err := checkSandboxToolVisible(target, filepath.Join(target, "bin", "tool"), hidden); err != nil {
		t.Fatalf("tool inside the mapped checkout must be accepted: %v", err)
	}
}

func TestSandboxMaskedPathsIncludeHomeRootsAndProtectedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := sandboxMaskedPaths([]string{"/srv/controller", "/srv/controller/.git"})
	for _, want := range []string{"/home", "/root", "/Users", filepath.Clean(home), "/srv/controller"} {
		found := false
		for _, path := range got {
			found = found || path == want || pathWithin(path, want)
		}
		if !found {
			t.Errorf("masked paths %v do not cover %s", got, want)
		}
	}
	for _, path := range got {
		if path == "/srv/controller/.git" {
			t.Errorf("nested protected path was not compacted: %v", got)
		}
	}
}
