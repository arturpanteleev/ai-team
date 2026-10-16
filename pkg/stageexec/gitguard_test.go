package stageexec

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckRepoGitMetadata(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		bad   bool
	}{
		"plain repo":                  {files: map[string]string{".git/HEAD": "ref: x", "a.txt": "x"}},
		"linked worktree top gitfile": {files: map[string]string{".git": "gitdir: /elsewhere/.git/worktrees/w\n"}},
		"submodule inside tree":       {files: map[string]string{".git/HEAD": "x", "sub/.git": "gitdir: ../.git/modules/sub\n"}},
		"submodule escapes":           {files: map[string]string{"sub/.git": "gitdir: ../../outside\n"}, bad: true},
		"submodule absolute":          {files: map[string]string{"sub/.git": "gitdir: /etc\n"}, bad: true},
		"nested gitfile garbage":      {files: map[string]string{"sub/.git": "hello"}, bad: true},
		"nested gitfile empty path":   {files: map[string]string{"sub/.git": "gitdir:   \n"}, bad: true},
		"nested clone dir":            {files: map[string]string{"sub/.git/HEAD": "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.files)
			err := checkRepoGitMetadata(root)
			if tc.bad && err == nil {
				t.Fatal("accepted")
			}
			if !tc.bad && err != nil {
				t.Fatalf("rejected: %v", err)
			}
		})
	}
}

func TestCheckRepoGitMetadataRejectsSymlinks(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	top := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(top, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(top); err == nil {
		t.Error("symlinked top-level .git accepted")
	}
	nested := t.TempDir()
	writeTree(t, nested, map[string]string{"sub/x": "x"})
	if err := os.Symlink(outside, filepath.Join(nested, "sub", ".git")); err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(nested); err == nil {
		t.Error("symlinked nested .git accepted")
	}
}

func TestHasGitElement(t *testing.T) {
	for path, want := range map[string]bool{".git": true, "a/.git/config": true, "a/.GIT": true, "a/.github": false, "a/b.git": false} {
		if got := hasGitElement(path); got != want {
			t.Errorf("hasGitElement(%q) = %v, want %v", path, got, want)
		}
	}
}
