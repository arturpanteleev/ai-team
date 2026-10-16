package stageexec

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
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
		"submodule inside tree":       {files: map[string]string{".git/HEAD": "x", ".git/modules/sub/HEAD": "x", "sub/.git": "gitdir: ../.git/modules/sub\n"}},
		"submodule points outside":    {files: map[string]string{"sub/.git": "gitdir: ../../outside\n"}},
		"submodule absolute outside":  {files: map[string]string{"sub/.git": "gitdir: /etc\n"}},
		"gitfile target missing":      {files: map[string]string{"sub/.git": "gitdir: ../nowhere\n"}, bad: true},
		"upper-case nested gitfile":   {files: map[string]string{"sub/.GIT": "hello"}, bad: true},
		"upper-case nested clone dir": {files: map[string]string{"a/.GIT/hooks/x": "x"}},
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

func TestCheckRepoGitMetadataRejectsGitfileThroughSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/x": "x", "sub/.git": "gitdir: ../link/x\n"})
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(root); err == nil {
		t.Error("gitfile through a symlink accepted")
	}
}

// ".." after a symlink component is applied to the real directory: this
// gitfile looks outside the tree lexically but really names <root>/d/x.
func TestCheckRepoGitMetadataRejectsDotDotAfterSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"d/e/f/keep": "x", "d/x/HEAD": "x", "s/.git": "gitdir: ../l/../../x\n"})
	if err := os.Symlink(filepath.Join(root, "d", "e", "f"), filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	if err := checkRepoGitMetadata(root); err == nil {
		t.Error("gitfile that really names a path inside the mount via symlink+.. accepted")
	}
}

func TestCheckRepoGitMetadataRejectsDotDotAfterMissingComponent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"s/.git": "gitdir: ../nothere/../../x\n"})
	if err := checkRepoGitMetadata(root); err == nil {
		t.Error("ambiguous gitfile path accepted")
	}
}

// A submodule of a linked worktree has a gitfile that points into the main
// checkout, outside the worktree: it must not be refused.
func TestCheckRepoGitMetadataAcceptsSubmoduleOfLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always", "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	subSrc := filepath.Join(base, "subsrc")
	main := filepath.Join(base, "main")
	for _, d := range []string{subSrc, main} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		git(d, "init", "-q")
		writeTree(t, d, map[string]string{"f": "x"})
		git(d, "add", "f")
		git(d, "commit", "-q", "-m", "init")
	}
	git(main, "submodule", "add", "-q", subSrc, "sub")
	git(main, "commit", "-q", "-m", "sub")
	wt := filepath.Join(base, "wt")
	git(main, "worktree", "add", "-q", wt)
	git(wt, "submodule", "update", "--init")
	data, err := os.ReadFile(filepath.Join(wt, "sub", ".git"))
	if err != nil || !strings.Contains(string(data), "worktrees") {
		t.Fatalf("unexpected submodule gitfile %q (%v)", data, err)
	}
	if err := checkRepoGitMetadata(wt); err != nil {
		t.Fatalf("submodule of a linked worktree rejected: %v", err)
	}
}

func TestHasGitElement(t *testing.T) {
	for path, want := range map[string]bool{".git": true, "a/.git/config": true, "a/.GIT": true, "a/.github": false, "a/b.git": false} {
		if got := hasGitElement(path); got != want {
			t.Errorf("hasGitElement(%q) = %v, want %v", path, got, want)
		}
	}
}
