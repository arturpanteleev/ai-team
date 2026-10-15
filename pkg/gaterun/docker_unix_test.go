//go:build !windows

package gaterun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/dockerrun"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// testImage is alpine pinned by the digest of its multi-architecture index.
const testImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

func TestParseDockerParams(t *testing.T) {
	cfg, err := ParseDockerParams(map[string]any{
		"image": testImage, "command": []any{"/bin/sh", "/gate/repo/gate.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Docker
	if d == nil || d.network() != "none" || d.RepoWritable || len(d.Mounts) != 0 || d.cpus() != "1" ||
		d.memoryMB() != 512 || d.pidsLimit() != 128 || d.tmpfsMB() != 64 || len(d.Exceptions(nil)) != 0 {
		t.Fatalf("defaults are not strict: %+v exceptions=%v", d, d.Exceptions(nil))
	}
	cfg, err = ParseDockerParams(map[string]any{
		"image": testImage, "command": []any{"check"}, "network": "bridge", "repo_access": "rw",
		"mounts": []any{"build:/out:rw", "docs:/docs"}, "env_allow": []any{"API_URL"}, "tmpfs_mb": 0,
		"cpus": "0.5", "memory_mb": 128, "pids_limit": 32, "user": "0:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	exceptions := strings.Join(cfg.Docker.Exceptions(cfg.EnvAllow), "\n")
	for _, want := range []string{"network=bridge", "repository mounted read-write", "extra mount build -> /out (read-write)",
		"extra mount docs -> /docs (read-only)", "environment passed: API_URL", "runs as root", "no scratch /tmp"} {
		if !strings.Contains(exceptions, want) {
			t.Errorf("exceptions lack %q:\n%s", want, exceptions)
		}
	}
	if !cfg.Docker.Mounts[0].Writable || cfg.Docker.Mounts[1].Writable {
		t.Errorf("mount modes: %+v", cfg.Docker.Mounts)
	}
}

func TestParseDockerParamsRejects(t *testing.T) {
	base := func(extra map[string]any) map[string]any {
		params := map[string]any{"image": testImage, "command": []any{"/bin/true"}}
		for k, v := range extra {
			params[k] = v
		}
		return params
	}
	cases := map[string]map[string]any{
		"tag without digest":   base(map[string]any{"image": "alpine:3.20"}),
		"latest":               base(map[string]any{"image": "alpine"}),
		"host network":         base(map[string]any{"network": "host"}),
		"unknown param":        base(map[string]any{"privileged": true}),
		"socket mount":         base(map[string]any{"mounts": []any{"/var/run/docker.sock:/var/run/docker.sock"}}),
		"mount escapes repo":   base(map[string]any{"mounts": []any{"../etc:/etc2"}}),
		"mount absolute":       base(map[string]any{"mounts": []any{"/etc:/etc2"}}),
		"mount whole repo":     base(map[string]any{"mounts": []any{".:/x"}}),
		"mount no target":      base(map[string]any{"mounts": []any{"build"}}),
		"mount bad mode":       base(map[string]any{"mounts": []any{"build:/out:exec"}}),
		"mount reserved":       base(map[string]any{"mounts": []any{"build:/gate/evidence"}}),
		"mount in evidence":    base(map[string]any{"mounts": []any{"build:/gate/evidence/x"}}),
		"mount proc":           base(map[string]any{"mounts": []any{"build:/proc/x"}}),
		"git rw":               base(map[string]any{"mounts": []any{".git:/g:rw"}}),
		"git subpath rw":       base(map[string]any{"mounts": []any{".git/hooks:/g:rw"}}),
		"cpus text":            base(map[string]any{"cpus": "many"}),
		"user injection":       base(map[string]any{"user": "1 --privileged"}),
		"docker client env":    base(map[string]any{"env_allow": []any{"DOCKER_HOST"}}),
		"home env":             base(map[string]any{"env_allow": []any{"HOME"}}),
		"inline secret":        base(map[string]any{"command": []any{"gate", "--token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}),
		"reserved env":         base(map[string]any{"env_allow": []any{"AI_TEAM_GATE_X"}}),
		"absolute cwd":         base(map[string]any{"cwd": "/etc"}),
		"memory too small":     base(map[string]any{"memory_mb": 1}),
		"pids too many":        base(map[string]any{"pids_limit": 1 << 20}),
		"missing image":        {"command": []any{"x"}},
		"missing command":      {"image": testImage},
		"unpinned flag string": base(map[string]any{"allow_unpinned_image": "yes"}),
	}
	for name, params := range cases {
		cfg, err := ParseDockerParams(params)
		if err == nil {
			t.Errorf("%s: accepted: %+v", name, cfg.Docker)
			continue
		}
		if strings.Contains(err.Error(), "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
			t.Errorf("%s: error echoes the secret: %v", name, err)
		}
	}
	if _, err := ParseDockerParams(base(map[string]any{"image": "alpine:3.20", "allow_unpinned_image": true})); err != nil {
		t.Errorf("explicit allow_unpinned_image: %v", err)
	}
	// The host gate does not accept container parameters, and vice versa.
	if _, err := ParseParams(map[string]any{"command": []any{"x"}, "image": testImage}); err == nil {
		t.Error("external_program accepted an image parameter")
	}
}

func TestDockerGateIsInTheLibrary(t *testing.T) {
	def, err := gatelib.Default().Lookup(gatelib.GateDockerProgram, 1)
	if err != nil || def.Kind != workflow.GateKindDocker {
		t.Fatalf("def = %+v err = %v", def, err)
	}
}

// fakeDockerCLI is a stand-in docker that records what it was asked to do.
func fakeDockerCLI(t *testing.T, mode string) (dockerrun.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
cmd=$1
printf '%s\n' "$@" > "$FAKE_DIR/$cmd.args"
if [ "$cmd" = "rm" ]; then echo removed; exit 0; fi
if [ "$cmd" = "ps" ]; then exit 0; fi
env > "$FAKE_DIR/run.env"
cat > "$FAKE_DIR/run.stdin"
case "$FAKE_MODE" in
  pass) echo '{"protocol":"ai-team.gate/v1","verdict":"pass","reason":"fake ok"}'; exit 0 ;;
  fail) echo '{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"fake says no"}'; exit 0 ;;
  daemon) echo "docker: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2; exit 125 ;;
  noimage) echo "Unable to find image 'x' locally; pull access denied, repository does not exist" >&2; exit 125 ;;
  oom) exit 137 ;;
  hang) exec sleep 30 ;;
esac
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	warmUp(t, bin)
	return dockerrun.Runner{Binary: bin, CleanupTimeout: 60 * time.Second, ClientEnv: func() []string {
		return []string{"PATH=/usr/bin:/bin", "FAKE_DIR=" + dir, "FAKE_MODE=" + mode}
	}}, dir
}

// warmUp executes a freshly written script once, so a first-exec security scan
// on a loaded machine does not eat into the timeouts the tests rely on.
func warmUp(t *testing.T, bin string) {
	t.Helper()
	command := exec.Command(bin, "rm")
	command.Env = []string{"FAKE_DIR=" + filepath.Dir(bin), "FAKE_MODE=pass"}
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(filepath.Dir(bin), "rm.args"))
}

func dockerFixture(t *testing.T, docker dockerrun.Runner) *fixture {
	t.Helper()
	f := newFixture(t)
	f.runner.DockerLauncher = DockerLauncher{Docker: docker, Getuid: func() int { return 501 }, Getgid: func() int { return 20 }}
	return f
}

func dockerRequest(f *fixture, mode string, settings DockerSettings, mutate ...func(*Request)) Request {
	settings.Image = testImage
	return f.request(mode, func(r *Request) {
		r.Config.Command = []string{"/bin/sh", "/gate/repo/demo-gate.sh", mode}
		r.Config.Docker = &settings
	}, func(r *Request) {
		for _, m := range mutate {
			m(r)
		}
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDockerLauncherPassesExactlyTheDeclaredMountsAndEnv(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	f := dockerFixture(t, docker)
	if err := os.MkdirAll(filepath.Join(f.repo, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	req := dockerRequest(f, "pass", DockerSettings{Mounts: []DockerMount{{Path: "build", Target: "/out", Writable: true}}})
	attempt, err := f.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Execution != workflow.GateExecutionSucceeded || attempt.Verdict != workflow.GateVerdictPass || attempt.Executor != workflow.GateKindDocker {
		t.Fatalf("attempt = %+v", attempt)
	}

	args := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(fakeDir, "run.args"))), "\n")
	var mounts, envs []string
	for i, a := range args {
		switch a {
		case "--mount":
			mounts = append(mounts, args[i+1])
		case "-e":
			envs = append(envs, args[i+1])
		}
	}
	wantMounts := []string{
		"type=bind,source=" + f.repo + ",target=/gate/repo,readonly",
		"type=bind,source=",
		"type=bind,source=" + filepath.Join(f.repo, "build") + ",target=/out",
	}
	if len(mounts) != 3 || mounts[0] != wantMounts[0] || !strings.HasPrefix(mounts[1], wantMounts[1]) || !strings.HasSuffix(mounts[1], ",target=/gate/evidence") ||
		strings.Contains(mounts[1], "readonly") || mounts[2] != wantMounts[2] {
		t.Fatalf("mounts = %q", mounts)
	}
	wantEnv := map[string]bool{"AI_TEAM_GATE_PROTOCOL": true, "AI_TEAM_GATE_ATTEMPT_ID": true, "SECRET_TOKEN": true, "PLAIN": true}
	if len(envs) != len(wantEnv) {
		t.Fatalf("env = %q", envs)
	}
	for _, name := range envs {
		if !wantEnv[name] {
			t.Errorf("unexpected variable %q", name)
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--user 501:20", "--rm", "--workdir /gate/repo", "--entrypoint /bin/sh " + testImage + " /gate/repo/demo-gate.sh pass"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q:\n%s", want, joined)
		}
	}
	for _, banned := range []string{testSecret, "HOST_ONLY", "docker.sock", "--privileged", "/Users", os.Getenv("HOME") + "/"} {
		if banned != "/" && strings.Contains(joined, banned) {
			t.Errorf("argv contains %q:\n%s", banned, joined)
		}
	}
	if env := readFile(t, filepath.Join(fakeDir, "run.env")); !strings.Contains(env, "SECRET_TOKEN="+testSecret) || strings.Contains(env, "HOST_ONLY") {
		t.Errorf("docker CLI environment wrong:\n%s", env)
	}

	// The program is told container paths only.
	var input Input
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(fakeDir, "run.stdin"))), &input); err != nil {
		t.Fatal(err)
	}
	if input.Paths.RepoRoot != ContainerRepoRoot || input.Paths.EvidenceDir != ContainerEvidenceDir || input.Artifact.Path != "/gate/repo/artifact.md" {
		t.Fatalf("input paths = %+v artifact %q", input.Paths, input.Artifact.Path)
	}
	if strings.Contains(readFile(t, filepath.Join(fakeDir, "run.stdin")), f.repo) {
		t.Error("a host path leaked into the input document")
	}

	// Evidence: the exceptions are recorded, secrets are not.
	invocation := readFile(t, filepath.Join(f.evidence, attempt.AttemptID, "invocation.json"))
	for _, want := range []string{`"executor": "docker"`, "extra mount build -\\u003e /out (read-write)", "environment passed: SECRET_TOKEN, PLAIN", `"network": "none"`, `"docker_socket": false`, "repository:build", testImage} {
		if !strings.Contains(invocation, want) {
			t.Errorf("invocation.json lacks %q:\n%s", want, invocation)
		}
	}
	for _, banned := range []string{testSecret, f.repo, "plain-value", "must-not-leak"} {
		if strings.Contains(invocation, banned) {
			t.Errorf("invocation.json contains %q:\n%s", banned, invocation)
		}
	}
	if !json.Valid([]byte(invocation)) {
		t.Error("invocation.json is not valid JSON")
	}
}

func TestDockerDefaultsMountOnlyRepoAndEvidence(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	f := dockerFixture(t, docker)
	req := dockerRequest(f, "pass", DockerSettings{}, func(r *Request) { r.Config.EnvAllow = nil })
	if _, err := f.runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	args := readFile(t, filepath.Join(fakeDir, "run.args"))
	if got := strings.Count(args, "--mount\n"); got != 2 {
		t.Errorf("mounts = %d, want 2 (repo ro + evidence):\n%s", got, args)
	}
	if got := strings.Count(args, "-e\n"); got != 2 {
		t.Errorf("env = %d, want only the 2 protocol variables:\n%s", got, args)
	}
	if strings.Contains(args, "SECRET_TOKEN") || strings.Contains(args, "PLAIN") {
		t.Errorf("env_allow names present without being configured:\n%s", args)
	}
}

func TestDockerArtifactOutsideRepoIsMountedReadOnly(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	f := dockerFixture(t, docker)
	outside := filepath.Join(t.TempDir(), "result.md")
	if err := os.WriteFile(outside, []byte("# R\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := dockerRequest(f, "pass", DockerSettings{}, func(r *Request) { r.Artifact.Path = outside })
	attempt, err := f.runner.Run(context.Background(), req)
	if err != nil || attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v err = %v", attempt, err)
	}
	args := readFile(t, filepath.Join(fakeDir, "run.args"))
	if !strings.Contains(args, "source="+outside+",target=/gate/artifact/result.md,readonly") {
		t.Errorf("the result file must be mounted read-only:\n%s", args)
	}
	invocation := readFile(t, filepath.Join(f.evidence, attempt.AttemptID, "invocation.json"))
	if strings.Contains(invocation, outside) {
		t.Errorf("invocation.json exposes the host path:\n%s", invocation)
	}
	// A symlinked result is refused instead of followed by the daemon.
	link := filepath.Join(t.TempDir(), "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	req = dockerRequest(f, "pass", DockerSettings{}, func(r *Request) { r.Artifact.Path = link })
	attempt, err = f.runner.Run(context.Background(), req)
	if err != nil || attempt.Execution != workflow.GateExecutionError {
		t.Fatalf("symlinked artifact: attempt = %+v err = %v", attempt, err)
	}
}

func TestDockerRefusesSymlinkMountSource(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	f := dockerFixture(t, docker)
	if err := os.Symlink("/etc", filepath.Join(f.repo, "etc-link")); err != nil {
		t.Fatal(err)
	}
	req := dockerRequest(f, "pass", DockerSettings{Mounts: []DockerMount{{Path: "etc-link", Target: "/x"}}})
	attempt, err := f.runner.Run(context.Background(), req)
	if err != nil || attempt.Execution != workflow.GateExecutionError || !strings.Contains(attempt.Reason, "symlink") {
		t.Fatalf("attempt = %+v err = %v", attempt, err)
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "run.args")); err == nil {
		t.Fatal("docker was started with a symlinked mount source")
	}
}

func TestDockerTechnicalFailuresAreErrorsNotVerdicts(t *testing.T) {
	for _, tc := range []struct {
		mode, want string
	}{
		{"daemon", "daemon unreachable"},
		{"noimage", "image not found"},
		{"oom", "out of memory"},
	} {
		docker, _ := fakeDockerCLI(t, tc.mode)
		f := dockerFixture(t, docker)
		attempt, err := f.runner.Run(context.Background(), dockerRequest(f, "pass", DockerSettings{}))
		if err != nil {
			t.Fatal(err)
		}
		if attempt.Execution != workflow.GateExecutionError || attempt.Verdict != "" || !strings.Contains(attempt.Reason, tc.want) {
			t.Errorf("%s: attempt = %+v", tc.mode, attempt)
		}
	}
	// No docker binary at all.
	f := dockerFixture(t, dockerrun.Runner{Binary: filepath.Join(t.TempDir(), "absent")})
	attempt, err := f.runner.Run(context.Background(), dockerRequest(f, "pass", DockerSettings{}))
	if err != nil || attempt.Execution != workflow.GateExecutionError || !strings.Contains(attempt.Reason, "Docker installed") {
		t.Fatalf("attempt = %+v err = %v", attempt, err)
	}
	// A content verdict is still a verdict.
	docker, _ := fakeDockerCLI(t, "fail")
	f = dockerFixture(t, docker)
	attempt, err = f.runner.Run(context.Background(), dockerRequest(f, "pass", DockerSettings{}))
	if err != nil || attempt.Execution != workflow.GateExecutionSucceeded || attempt.Verdict != workflow.GateVerdictFail || attempt.Reason != "fake says no" {
		t.Fatalf("attempt = %+v err = %v", attempt, err)
	}
}

func TestDockerTimeoutStopsAndRemovesContainer(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "hang")
	f := dockerFixture(t, docker)
	req := dockerRequest(f, "hang", DockerSettings{}, func(r *Request) { r.Config.Timeout = time.Second })
	start := time.Now()
	attempt, err := f.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Execution != workflow.GateExecutionTimeout || attempt.Verdict != "" || time.Since(start) > 15*time.Second {
		t.Fatalf("attempt = %+v after %s", attempt, time.Since(start))
	}
	run := strings.Fields(readFile(t, filepath.Join(fakeDir, "run.args")))
	rm := strings.Fields(readFile(t, filepath.Join(fakeDir, "rm.args")))
	var name string
	for i, a := range run {
		if a == "--name" {
			name = run[i+1]
		}
	}
	if name == "" || len(rm) != 3 || rm[1] != "-f" || rm[2] != name {
		t.Fatalf("run name %q, rm args %q", name, rm)
	}
	if !strings.Contains(attempt.Reason, "removed") {
		t.Errorf("reason = %q", attempt.Reason)
	}

	// Cancellation behaves the same and is not a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	req = dockerRequest(f, "hang", DockerSettings{})
	attempt, err = f.runner.Run(ctx, req)
	if err != nil || attempt.Execution != workflow.GateExecutionCanceled {
		t.Fatalf("attempt = %+v err = %v", attempt, err)
	}
}

func TestHostLauncherRefusesDockerSettings(t *testing.T) {
	outcome := HostLauncher{}.Launch(context.Background(), LaunchSpec{Argv: []string{"/bin/true"}, Docker: &DockerSettings{Image: testImage}})
	if outcome.Err == nil {
		t.Fatal("host launcher ran a docker gate")
	}
	outcome = DockerLauncher{}.Launch(context.Background(), LaunchSpec{Argv: []string{"/bin/true"}})
	if outcome.Err == nil {
		t.Fatal("docker launcher ran without settings")
	}
}

func dockerAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "pull", "-q", testImage).CombinedOutput(); err != nil {
		t.Skipf("pinned test image cannot be pulled: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// materialize replaces the fixture's symlink to the demo gate by a real copy:
// a symlink to a file outside the repository would dangle in the container.
func materialize(t *testing.T, f *fixture) {
	t.Helper()
	link := filepath.Join(f.repo, "demo-gate.sh")
	data, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSameFixtureOnHostAndDocker is the acceptance test: one gate fixture run
// by both adapters yields the same classification, verdict, reason and
// evidence files.
func TestSameFixtureOnHostAndDocker(t *testing.T) {
	dockerAvailable(t)
	f := newFixture(t)
	materialize(t, f)
	f.runner.DockerLauncher = DockerLauncher{}
	for _, mode := range []string{"pass", "fail", "not_applicable", "check", "evidence", "crash", "badjson", "twodocs", "leak"} {
		host := f.run(t, mode, func(r *Request) {
			r.Config.Command = []string{"/bin/sh", filepath.Join(f.repo, "demo-gate.sh"), mode}
		})
		dock := f.run(t, mode, func(r *Request) {
			r.Config.Command = []string{"/bin/sh", "/gate/repo/demo-gate.sh", mode}
			r.Config.Docker = &DockerSettings{Image: testImage}
		})
		if host.Executor != workflow.GateKindExternal || dock.Executor != workflow.GateKindDocker {
			t.Fatalf("%s: executors %s / %s", mode, host.Executor, dock.Executor)
		}
		if host.Execution != dock.Execution || host.Verdict != dock.Verdict || host.Reason != dock.Reason {
			t.Errorf("%s: host {%s %q %q} != docker {%s %q %q}", mode, host.Execution, host.Verdict, host.Reason, dock.Execution, dock.Verdict, dock.Reason)
		}
		if got, want := evidenceSummary(host), evidenceSummary(dock); got != want {
			t.Errorf("%s: evidence differs:\nhost   %s\ndocker %s", mode, got, want)
		}
	}
}

// evidenceSummary lists kind, file name and, except for invocation.json (which
// legitimately differs: it describes the container), the content digest.
func evidenceSummary(a workflow.GateAttempt) string {
	var parts []string
	for _, ref := range a.Evidence {
		name := strings.TrimPrefix(ref.Path, a.AttemptID+"/")
		digest := ref.SHA256
		if ref.Kind == "invocation" {
			digest = "-"
		}
		parts = append(parts, ref.Kind+":"+name+":"+digest)
	}
	return strings.Join(parts, " ")
}

// TestDockerSandboxIsReallyClosed runs a probe inside the container and checks
// from within that the defaults hold: read-only repository and root, no
// network, no controller environment, no Docker socket, no root user, no host
// home directory.
func TestDockerSandboxIsReallyClosed(t *testing.T) {
	dockerAvailable(t)
	f := newFixture(t)
	probe := `#!/bin/sh
input=$(cat)
bad=""
note() { bad="$bad $1;"; }
touch /gate/repo/pwned 2>/dev/null && note "repo writable"
touch /pwned 2>/dev/null && note "root fs writable"
touch /gate/evidence/ok 2>/dev/null || note "evidence dir not writable"
[ "$(id -u)" = "0" ] && note "running as root"
[ -n "${HOST_ONLY:-}" ] && note "HOST_ONLY leaked"
[ -n "${HOME_LEAK:-}" ] && note "HOME_LEAK leaked"
[ -S /var/run/docker.sock ] && note "docker socket present"
[ -d /root/.ssh ] && note "root ssh dir"
ls /Users /home/*/.ssh >/dev/null 2>&1 && note "host home visible"
wget -q -T 3 -O /dev/null http://1.1.1.1/ 2>/dev/null && note "network reachable"
grep -q ' / .* ro[ ,]' /proc/mounts || note "root not mounted ro"
[ "$(awk '/CapEff/ {print $2}' /proc/self/status)" = "0000000000000000" ] || note "capabilities present"
[ "${PLAIN:-}" = "plain-value" ] || note "allowed variable missing"
if [ -n "$bad" ]; then
  printf '{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"sandbox open:%s"}\n' "$bad"
else
  printf '{"protocol":"ai-team.gate/v1","verdict":"pass","reason":"sandbox closed"}\n'
fi
`
	if err := os.WriteFile(filepath.Join(f.repo, "probe.sh"), []byte(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	f.runner.LookupEnv = func(name string) (string, bool) {
		return map[string]string{"PLAIN": "plain-value", "HOST_ONLY": "x", "HOME_LEAK": "x"}[name], name == "PLAIN" || name == "HOST_ONLY" || name == "HOME_LEAK"
	}
	attempt := f.run(t, "pass", func(r *Request) {
		r.Config.Command = []string{"/bin/sh", "/gate/repo/probe.sh"}
		r.Config.EnvAllow = []string{"PLAIN"}
		r.Config.Docker = &DockerSettings{Image: testImage}
	})
	if attempt.Execution != workflow.GateExecutionSucceeded || attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v\n%s", attempt, readFile(t, filepath.Join(f.evidence, attempt.AttemptID, "stderr.log")))
	}
	if _, err := os.Stat(filepath.Join(f.repo, "pwned")); err == nil {
		t.Fatal("the program wrote into the repository on the host")
	}
}

func TestDockerUnknownImageIsATechnicalError(t *testing.T) {
	dockerAvailable(t)
	f := newFixture(t)
	missing := "alpine@sha256:" + strings.Repeat("0", 64)
	attempt := f.run(t, "pass", func(r *Request) {
		r.Config.Command = []string{"/bin/true"}
		r.Config.Docker = &DockerSettings{Image: missing, Pull: "never"}
	})
	if attempt.Execution != workflow.GateExecutionError || attempt.Verdict != "" {
		t.Fatalf("attempt = %+v", attempt)
	}
	t.Logf("reason: %s", attempt.Reason)
}

func TestDockerTimeoutRemovesRealContainer(t *testing.T) {
	dockerAvailable(t)
	f := newFixture(t)
	materialize(t, f)
	attempt := f.run(t, "hang", func(r *Request) {
		r.Config.Command = []string{"/bin/sh", "/gate/repo/demo-gate.sh", "hang"}
		r.Config.Timeout = 3 * time.Second
		r.Config.Docker = &DockerSettings{Image: testImage}
	})
	if attempt.Execution != workflow.GateExecutionTimeout {
		t.Fatalf("attempt = %+v", attempt)
	}
	out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=ai-team.gate-attempt="+attempt.AttemptID).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("container left behind: %q err=%v", out, err)
	}
}

// TestDockerWritableRepoKeepsGitReadOnly: with repo_access rw the repository
// takes writes but .git (hooks!) does not, checked from inside a real
// container; an ordinary file written in the container shows up on the host.
func TestDockerWritableRepoKeepsGitReadOnly(t *testing.T) {
	dockerAvailable(t)
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Docker Desktop keeps the read-only mountpoint busy for a moment after the
	// container is gone; retry the removal so the temp dir cleanup succeeds.
	t.Cleanup(func() {
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if os.RemoveAll(filepath.Join(f.repo, ".git")) == nil {
				return
			}
		}
	})
	probe := `#!/bin/sh
cat >/dev/null
bad=""
touch /gate/repo/written-by-container 2>/dev/null || bad="$bad repo not writable;"
touch /gate/repo/.git/hooks/pre-commit 2>/dev/null && bad="$bad hook written;"
echo x >> /gate/repo/.git/config 2>/dev/null && bad="$bad git config written;"
if [ -n "$bad" ]; then
  printf '{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"%s"}\n' "$bad"
else
  printf '{"protocol":"ai-team.gate/v1","verdict":"pass","reason":"git is read-only"}\n'
fi
`
	if err := os.WriteFile(filepath.Join(f.repo, "probe.sh"), []byte(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	attempt := f.run(t, "pass", func(r *Request) {
		r.Config.Command = []string{"/bin/sh", "/gate/repo/probe.sh"}
		r.Config.Docker = &DockerSettings{Image: testImage, RepoWritable: true}
	})
	if attempt.Execution != workflow.GateExecutionSucceeded || attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v\n%s", attempt, attempt.Reason)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "written-by-container")); err != nil {
		t.Fatalf("the writable repository did not take the write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".git", "hooks", "pre-commit")); err == nil {
		t.Fatal("a hook was planted in .git on the host")
	}
}

func TestGitOverlays(t *testing.T) {
	repo := t.TempDir()
	if got, err := gitOverlays(repo); err != nil || len(got) != 0 {
		t.Fatalf("no .git: %v %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := gitOverlays(repo); err != nil || len(got) != 1 || got[0] != ".git" {
		t.Fatalf("plain repo: %v %v", got, err)
	}
	other := t.TempDir()
	repo2 := t.TempDir()
	if err := os.Symlink(other, filepath.Join(repo2, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOverlays(repo2); err == nil {
		t.Fatal("a symlinked .git was accepted")
	}
}

// TestRunnerSweepsOrphansOnFirstDockerGate: the first Docker gate of a Runner
// asks the launcher to remove containers a dead controller left behind, once.
func TestRunnerSweepsOrphansOnFirstDockerGate(t *testing.T) {
	docker, fakeDir := fakeDockerCLI(t, "pass")
	f := dockerFixture(t, docker)
	for i := 0; i < 2; i++ {
		req := dockerRequest(f, "pass", DockerSettings{})
		req.Attempt.AttemptID = req.Attempt.AttemptID + string(rune('a'+i))
		if _, err := f.runner.Run(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	ps := readFile(t, filepath.Join(fakeDir, "ps.args"))
	for _, want := range []string{"label=ai-team.dockerrun", "label=" + GateAttemptLabel} {
		if !strings.Contains(ps, want) {
			t.Errorf("sweep did not filter by %q:\n%s", want, ps)
		}
	}
}
