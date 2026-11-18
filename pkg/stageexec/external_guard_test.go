//go:build !windows

package stageexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// recordingLauncher stands in for the host process: it records what the
// executor would launch and returns a minimal valid response. It lets the
// tests prove that a rejected workdir never reaches a process at all.
type recordingLauncher struct {
	called bool
	dir    string
	stdin  []byte
}

func (l *recordingLauncher) Run(_ context.Context, launch Launch) error {
	l.called = true
	l.dir = launch.Dir
	l.stdin = append([]byte(nil), launch.Stdin...)
	_, _ = launch.Stdout.Write([]byte(`{"schema_version":1,"status":"ok","outputs":{}}`))
	return nil
}

func guardedExternal(recorder *recordingLauncher) *External {
	return NewExternal("demo", AdapterSpec{Argv: []string{"program"}}).WithLauncher(recorder)
}

// A symlinked WorkDir is refused before anything starts. PinRoot rejects a
// symlink in the final component because the program would otherwise run in a
// tree the git guard never inspected.
func TestExternalRejectsSymlinkedWorkDir(t *testing.T) {
	target := t.TempDir()
	writeTree(t, target, map[string]string{".git/HEAD": "ref: x"})
	link := filepath.Join(t.TempDir(), "workdir")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	recorder := &recordingLauncher{}
	if _, err := guardedExternal(recorder).Execute(context.Background(), Request{WorkDir: link}); err == nil {
		t.Fatal("a symlinked WorkDir was accepted")
	} else if !strings.Contains(err.Error(), "root") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should name the symlink: %v", err)
	}
	if recorder.called {
		t.Fatal("a process was launched for a symlinked WorkDir")
	}
}

// A legitimate workdir whose ancestors are symlinks is resolved (Real) before
// the git guard and before launch. This also covers macOS /var -> /private/var,
// which t.TempDir() exercises on every run. The process must see the resolved
// tree, not the caller's spelling with the ancestor symlink.
func TestExternalAcceptsWorkDirBehindAncestorSymlink(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	realRepo := filepath.Join(parent, "repo")
	writeTree(t, realRepo, map[string]string{".git/HEAD": "ref: x", "src/main.go": "package main"})
	link := filepath.Join(base, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	shown := filepath.Join(link, "repo")
	resolved, err := filepath.EvalSymlinks(shown)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == shown {
		t.Fatalf("expected an ancestor symlink to resolve: %q", shown)
	}
	recorder := &recordingLauncher{}
	if _, err := guardedExternal(recorder).Execute(context.Background(), Request{WorkDir: shown}); err != nil {
		t.Fatalf("legitimate workdir rejected: %v", err)
	}
	if !recorder.called {
		t.Fatal("the process was not launched")
	}
	if recorder.dir != resolved {
		t.Fatalf("launch dir = %q, want resolved %q", recorder.dir, resolved)
	}
	if !strings.Contains(string(recorder.stdin), `"workdir":"`+resolved+`"`) {
		t.Fatalf("request document does not carry the resolved workdir: %s", recorder.stdin)
	}
	if strings.Contains(string(recorder.stdin), `"workdir":"`+shown+`"`) {
		t.Fatalf("request document still uses the unresolved workdir: %s", recorder.stdin)
	}
}

// Two gitfile FIFOs are a deterministic hook inside the git guard: the guard
// blocks reading the first while the ancestor symlink is swapped, then the
// second lets it finish. The re-verify immediately before launch must catch
// the swap and no process may start.
func TestExternalRejectsWorkDirSwappedAfterGuard(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	realRepo := filepath.Join(parent, "repo")
	writeTree(t, realRepo, map[string]string{".git/HEAD": "ref: x"})
	for _, dir := range []string{"sub/a", "sub/b"} {
		if err := os.MkdirAll(filepath.Join(realRepo, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fifoA := filepath.Join(realRepo, "sub", "a", "GIT~1")
	fifoB := filepath.Join(realRepo, "sub", "b", "GIT~2")
	if err := syscall.Mkfifo(fifoA, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifoB, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	other := filepath.Join(base, "other")
	if err := os.MkdirAll(filepath.Join(other, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeGitfile := func(path string, opened chan struct{}) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = f.WriteString("gitdir: /outside\n")
		_ = f.Close()
		if opened != nil {
			close(opened)
		}
	}
	openedA := make(chan struct{})
	releaseB := make(chan struct{})
	go writeGitfile(fifoA, openedA)
	go func() { <-releaseB; writeGitfile(fifoB, nil) }()

	recorder := &recordingLauncher{}
	done := make(chan error, 1)
	go func() {
		_, err := guardedExternal(recorder).Execute(context.Background(), Request{WorkDir: filepath.Join(link, "repo")})
		done <- err
	}()
	select {
	case <-openedA:
	case err := <-done:
		t.Fatalf("the git guard finished before the hook fired: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the git guard did not read the FIFO")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	close(releaseB)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "изменён после проверки") {
			t.Fatalf("a workdir swapped after the guard was launched: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the executor did not finish after the swap")
	}
	if recorder.called {
		t.Fatal("a process was launched after the workdir was swapped")
	}
}
