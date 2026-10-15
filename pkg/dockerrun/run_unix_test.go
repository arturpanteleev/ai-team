//go:build !windows

package dockerrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeDocker writes a stand-in docker CLI that records its calls under dir and
// behaves according to FAKE_MODE.
func fakeDocker(t *testing.T, mode string) (Runner, string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
cmd=$1
printf '%s\n' "$@" > "$FAKE_DIR/$cmd.args"
if [ "$cmd" = "ps" ]; then printf '%s' "$FAKE_PS"; exit 0; fi
if [ "$cmd" = "rm" ]; then
  if [ "$FAKE_MODE" = "inprogress" ]; then echo "Error response from daemon: removal of container x is already in progress" >&2; exit 1; fi
  if [ "$FAKE_MODE" = "rmfail" ]; then echo "daemon down" >&2; exit 1; fi
  if [ "$FAKE_MODE" = "gone" ]; then echo "Error: No such container: x" >&2; exit 1; fi
  echo removed; exit 0
fi
env > "$FAKE_DIR/run.env"
cat > "$FAKE_DIR/run.stdin"
case "$FAKE_MODE" in
  ok) echo '{"ok":true}'; exit 0 ;;
  daemon) echo "Cannot connect to the Docker daemon" >&2; exit 125 ;;
  oom) exit 137 ;;
  hang|rmfail|gone|inprogress) exec sleep 30 ;;
  flood) yes xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx ;;
esac
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	warm := exec.Command(bin, "rm")
	warm.Env = []string{"FAKE_DIR=" + dir, "FAKE_MODE=ok"}
	if err := warm.Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "rm.args"))
	return Runner{Binary: bin, CleanupTimeout: 60 * time.Second, ClientEnv: func() []string {
		return []string{"PATH=/usr/bin:/bin", "FAKE_DIR=" + dir, "FAKE_MODE=" + mode, "FAKE_PS=" + os.Getenv("S08_FAKE_PS")}
	}}, dir
}

// cancelWhenStarted cancels the context once the fake docker has started (it
// has written run.args), so a slow first exec of a fresh script on a loaded
// machine cannot make the test race its own timeout.
func cancelWhenStarted(t *testing.T, dir string, delay time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "run.args")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(delay)
		cancel()
	}()
	return ctx, cancel
}

func TestRunSuccessKeepsSecretOutOfArgv(t *testing.T) {
	runner, dir := fakeDocker(t, "ok")
	res := runner.Run(context.Background(), validSpec(), []byte("input"), 1<<16)
	if res.Err != nil || strings.TrimSpace(string(res.Stdout)) != `{"ok":true}` {
		t.Fatalf("res = %+v", res)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "run.args"))
	if strings.Contains(string(args), "very-secret-value-123") {
		t.Fatalf("secret in argv:\n%s", args)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "run.env"))
	if !strings.Contains(string(env), "TOKEN=very-secret-value-123") {
		t.Fatalf("the value must reach the docker CLI through its environment:\n%s", env)
	}
	for _, banned := range []string{"HOME=", "SSH_AUTH_SOCK="} {
		if strings.Contains(string(env), banned) {
			t.Errorf("controller variable %s leaked into the docker CLI environment", banned)
		}
	}
	if stdin, _ := os.ReadFile(filepath.Join(dir, "run.stdin")); string(stdin) != "input" {
		t.Fatalf("stdin = %q", stdin)
	}
	if _, err := os.Stat(filepath.Join(dir, "rm.args")); err == nil {
		t.Fatal("a clean run must not need a removal")
	}
}

func TestRunTimeoutRemovesContainerByName(t *testing.T) {
	runner, dir := fakeDocker(t, "hang")
	ctx, cancel := cancelWhenStarted(t, dir, 100*time.Millisecond)
	defer cancel()
	res := runner.Run(ctx, validSpec(), nil, 1<<16)
	if res.Err == nil {
		t.Fatalf("res = %+v", res)
	}
	rm, err := os.ReadFile(filepath.Join(dir, "rm.args"))
	if err != nil {
		t.Fatalf("container was not removed: %v", err)
	}
	if got := strings.Fields(string(rm)); len(got) != 3 || got[0] != "rm" || got[1] != "-f" || got[2] != "ai-team-gate-1" {
		t.Fatalf("rm args = %q", got)
	}
	if !strings.Contains(res.Detail, "removed") {
		t.Fatalf("detail = %q", res.Detail)
	}
}

func TestRunReportsRemovalFailure(t *testing.T) {
	runner, dir := fakeDocker(t, "rmfail")
	ctx, cancel := cancelWhenStarted(t, dir, 100*time.Millisecond)
	defer cancel()
	res := runner.Run(ctx, validSpec(), nil, 1<<16)
	if !strings.Contains(res.Detail, "could NOT be removed") {
		t.Fatalf("detail = %q", res.Detail)
	}
	runner, dir = fakeDocker(t, "inprogress")
	ctx3, cancel3 := cancelWhenStarted(t, dir, 100*time.Millisecond)
	defer cancel3()
	if res := runner.Run(ctx3, validSpec(), nil, 1<<16); strings.Contains(res.Detail, "NOT") || !strings.Contains(res.Detail, "being removed") {
		t.Fatalf("an in-progress removal is not a failure: %q", res.Detail)
	}
	runner, dir = fakeDocker(t, "gone")
	ctx2, cancel2 := cancelWhenStarted(t, dir, 100*time.Millisecond)
	defer cancel2()
	if res := runner.Run(ctx2, validSpec(), nil, 1<<16); !strings.Contains(res.Detail, "already gone") {
		t.Fatalf("detail = %q", res.Detail)
	}
}

func TestRunDockerFailuresAreDistinguished(t *testing.T) {
	runner, _ := fakeDocker(t, "daemon")
	res := runner.Run(context.Background(), validSpec(), nil, 1<<16)
	if res.Err == nil || res.ExitCode != 125 || !strings.Contains(res.Detail, "daemon unreachable") || !strings.Contains(string(res.Stderr), "Cannot connect") {
		t.Fatalf("res = %+v", res)
	}
	runner, _ = fakeDocker(t, "oom")
	if res := runner.Run(context.Background(), validSpec(), nil, 1<<16); res.ExitCode != 137 || !strings.Contains(res.Detail, "out of memory") {
		t.Fatalf("res = %+v", res)
	}
	missing := Runner{Binary: filepath.Join(t.TempDir(), "no-such-docker")}
	res = missing.Run(context.Background(), validSpec(), nil, 1<<16)
	if res.Err == nil || !strings.Contains(res.Detail, "could not be started") {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunOutputLimit(t *testing.T) {
	runner, _ := fakeDocker(t, "flood")
	res := runner.Run(context.Background(), validSpec(), nil, 1024)
	if !res.OutputExceeded || res.Err == nil || len(res.Stdout) > 1024 {
		t.Fatalf("exceeded=%v err=%v len=%d", res.OutputExceeded, res.Err, len(res.Stdout))
	}
}

func TestRunInvalidSpecNeverStartsDocker(t *testing.T) {
	runner, dir := fakeDocker(t, "ok")
	spec := validSpec()
	spec.Image = "alpine:3.20"
	res := runner.Run(context.Background(), spec, nil, 1<<16)
	if res.Err == nil {
		t.Fatal("unpinned image was run")
	}
	if _, err := os.Stat(filepath.Join(dir, "run.args")); err == nil {
		t.Fatal("docker was invoked for an invalid spec")
	}
}

func TestRunLabelsContainers(t *testing.T) {
	runner, dir := fakeDocker(t, "ok")
	_ = runner.Run(context.Background(), validSpec(), nil, 1<<16)
	args, _ := os.ReadFile(filepath.Join(dir, "run.args"))
	for _, want := range []string{"--label\n" + LabelOwner + "=1\n", "--label\n" + LabelStarted + "="} {
		if !strings.Contains(string(args), want) {
			t.Errorf("argv lacks label %q:\n%s", want, args)
		}
	}
}

func TestSweepOrphansRemovesOnlyOldOnes(t *testing.T) {
	old := strconv.FormatInt(time.Now().Add(-3*time.Hour).Unix(), 10)
	fresh := strconv.FormatInt(time.Now().Unix(), 10)
	t.Setenv("S08_FAKE_PS", "aaa "+old+"\nbbb "+fresh+"\nccc \n")
	runner, dir := fakeDocker(t, "ok")
	removed, err := runner.SweepOrphans(context.Background(), "ai-team.gate-attempt", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "aaa,ccc" {
		t.Fatalf("removed = %v (old and unlabeled-time are swept, fresh is kept)", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "ps.args")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.SweepOrphans(context.Background(), "bad label!", time.Hour); err == nil {
		t.Fatal("invalid label accepted")
	}
}

func TestClientEnvNameIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{"docker_host", "Docker_Context", "path", "Home", "xdg_config_home"} {
		if !ClientEnvName(name) {
			t.Errorf("%s must be a client variable", name)
		}
	}
}
