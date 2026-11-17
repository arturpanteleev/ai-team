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

	"github.com/arturpanteleev/ai-team/pkg/dockerrun"
)

// fakeStageDocker records what the launcher would ask the daemon to do without
// starting any container. A "run" succeeds with an empty ok response.
func fakeStageDocker(t *testing.T) (dockerrun.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
cmd=$1
printf '%s\n' "$@" > "$FAKE_DIR/$cmd.args"
case "$cmd" in
  ps|rm) exit 0 ;;
esac
cat > "$FAKE_DIR/run.stdin"
echo '{"schema_version":1,"status":"ok","outputs":{}}'
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dockerrun.Runner{Binary: bin, CleanupTimeout: time.Second, ClientEnv: func() []string {
		return []string{"FAKE_DIR=" + dir}
	}}, dir
}

func stageDockerLaunch(t *testing.T, workDir string) Launch {
	t.Helper()
	return Launch{
		Argv:   []string{"/bin/sh", "-c", "true"},
		Stdout: NewLimitedBuffer(4096),
		Stderr: NewLimitedBuffer(4096),
		Request: Request{
			RunID: "run-s10", AttemptID: "run-s10-001-review", Stage: "review", Feature: "feat", Task: "task",
			WorkDir: workDir, ArtifactRoot: t.TempDir(),
		},
	}
}

func stageDockerMounts(t *testing.T, dir string) []string {
	t.Helper()
	args := strings.Split(strings.TrimSpace(string(mustReadFile(t, filepath.Join(dir, "run.args")))), "\n")
	var mounts []string
	for i, a := range args {
		if a == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	return mounts
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The repository mount and every configured repository mount must be based on
// the resolved root, not on the spelling that still contains an ancestor link.
func TestDockerLauncherMountsResolvedRootBehindAncestorSymlink(t *testing.T) {
	docker, fakeDir := fakeStageDocker(t)
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	realRepo := filepath.Join(parent, "repo")
	writeTree(t, realRepo, map[string]string{".git/HEAD": "ref: x", "docs/readme": "x"})
	link := filepath.Join(base, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	shown := filepath.Join(link, "repo")
	resolved, err := filepath.EvalSymlinks(shown)
	if err != nil {
		t.Fatal(err)
	}
	launcher := DockerLauncher{
		Settings: DockerSpec{Image: testImage, Repo: "ro", Mounts: []string{"docs:/docs"}},
		Docker:   docker,
		Getuid:   func() int { return 501 }, Getgid: func() int { return 20 },
	}
	if err := launcher.Run(context.Background(), stageDockerLaunch(t, shown)); err != nil {
		t.Fatalf("docker executor failed: %v", err)
	}
	mounts := stageDockerMounts(t, fakeDir)
	for _, want := range []string{
		"type=bind,source=" + resolved + ",target=/stage/repo,readonly",
		"type=bind,source=" + filepath.Join(resolved, "docs") + ",target=/docs,readonly",
	} {
		found := false
		for _, m := range mounts {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mounts lack %q:\n%q", want, mounts)
		}
	}
	for _, m := range mounts {
		if strings.Contains(m, "source="+shown) || strings.Contains(m, "source="+filepath.Join(shown, "docs")) {
			t.Errorf("a mount still uses the unresolved root: %q", m)
		}
	}
}

// Two gitfile FIFOs are a deterministic hook inside the git guard: after the
// first is read the ancestor symlink is swapped, and the second keeps the
// guard busy until the swap is in place. The launcher must re-verify the pin
// before the daemon sees a mount.
func TestDockerLauncherRejectsRootSwappedAfterGuard(t *testing.T) {
	docker, fakeDir := fakeStageDocker(t)
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
		t.Fatal(err)
	}
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	openedA := make(chan struct{})
	releaseB := make(chan struct{})
	go func() {
		f, err := os.OpenFile(fifoA, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = f.WriteString("gitdir: /outside\n")
		_ = f.Close()
		close(openedA)
	}()
	go func() {
		<-releaseB
		f, err := os.OpenFile(fifoB, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = f.WriteString("gitdir: /outside\n")
		_ = f.Close()
	}()
	launcher := DockerLauncher{
		Settings: DockerSpec{Image: testImage, Repo: "ro"},
		Docker:   docker,
		Getuid:   func() int { return 501 }, Getgid: func() int { return 20 },
	}
	launch := stageDockerLaunch(t, filepath.Join(link, "repo"))
	done := make(chan error, 1)
	go func() { done <- launcher.Run(context.Background(), launch) }()
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
			t.Fatalf("a root swapped after the guard was launched: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the launcher did not finish after the swap")
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "run.args")); err == nil {
		t.Fatal("the daemon was invoked after the root was swapped")
	}
}
