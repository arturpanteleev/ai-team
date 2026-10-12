package checks

import (
	"os"
	"path/filepath"
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
