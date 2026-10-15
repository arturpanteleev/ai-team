package stageexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"gopkg.in/yaml.v3"
)

// testImage is alpine pinned by the digest of its multi-architecture index.
const testImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

func TestDockerSpecValidationAndStrictYAML(t *testing.T) {
	base := func(mutate func(*AdapterSpec)) AdapterSpec {
		spec := AdapterSpec{Argv: []string{"/bin/sh", "/agent/review.sh"}, Docker: &DockerSpec{Image: testImage}}
		if mutate != nil {
			mutate(&spec)
		}
		return spec
	}
	if err := base(nil).Validate("review"); err != nil {
		t.Fatalf("valid docker adapter rejected: %v", err)
	}
	for name, mutate := range map[string]func(*AdapterSpec){
		"tag without digest": func(s *AdapterSpec) { s.Docker.Image = "alpine:3.20" },
		"no image":           func(s *AdapterSpec) { s.Docker.Image = "" },
		"host network":       func(s *AdapterSpec) { s.Docker.Network = "host" },
		"bad cpus":           func(s *AdapterSpec) { s.Docker.CPUs = "lots" },
		"bad repo":           func(s *AdapterSpec) { s.Docker.Repo = "rw" },
		"mount escapes":      func(s *AdapterSpec) { s.Docker.Mounts = []string{"../etc:/agent"} },
		"mount into stage":   func(s *AdapterSpec) { s.Docker.Mounts = []string{"x:/stage/in/diff"} },
		"mount without dst":  func(s *AdapterSpec) { s.Docker.Mounts = []string{"x"} },
		"path env":           func(s *AdapterSpec) { s.EnvAllow = []string{"PATH"} },
		"docker client env":  func(s *AdapterSpec) { s.EnvAllow = []string{"DOCKER_HOST"} },
	} {
		if err := base(mutate).Validate("review"); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if err := base(func(s *AdapterSpec) { s.Docker.AllowUnpinnedImage = true; s.Docker.Image = "alpine:3.20" }).Validate("review"); err != nil {
		t.Errorf("explicit allow_unpinned_image must be accepted: %v", err)
	}
	var spec AdapterSpec
	if err := yaml.Unmarshal([]byte("argv: [x]\ndocker:\n  image: "+testImage+"\n  netwrk: bridge\n"), &spec); err == nil {
		t.Fatal("a misspelled docker key must be rejected")
	}
	if err := yaml.Unmarshal([]byte("argv: [x]\ndocker:\n  image: "+testImage+"\n  cpus: 2\n  tmpfs_mb: 0\n"), &spec); err != nil ||
		spec.Docker == nil || spec.Docker.CPUs != "2" || spec.Docker.tmpfsMB() != 0 {
		t.Fatalf("spec = %+v, %v", spec.Docker, err)
	}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("container fixture requires a POSIX host")
	}
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

// reviewFixture prepares a repository with the fixture agent and a request
// whose declared input "diff" holds the given text.
func reviewFixture(t *testing.T, diff string, flags ...string) (*External, Request) {
	t.Helper()
	requireDocker(t)
	repo := t.TempDir()
	agentDir := filepath.Join(repo, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../e2etest/container-review-agent/review.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "review.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(repo, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	diffPath := filepath.Join(artifacts, "change.patch")
	if err := os.WriteFile(diffPath, []byte(diff), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := AdapterSpec{
		Argv:    append([]string{"/bin/sh", "/agent/review.sh"}, flags...),
		Timeout: "2m",
		Docker:  &DockerSpec{Image: testImage, Mounts: []string{"agent:/agent"}},
	}
	return NewExternal("fixture-review", spec), Request{
		RunID: "run-s10", AttemptID: "run-s10-001-code_review", Stage: "code_review", Feature: "feat", Task: "task",
		WorkDir: repo, ArtifactRoot: artifacts, LogDir: filepath.Join(repo, "logs"),
		Inputs:  []runtime.Artifact{{Name: "diff", Path: diffPath, Size: int64(len(diff))}},
		Outputs: []Output{{Name: "review", Path: filepath.Join(artifacts, "review.md")}},
	}
}

func noStageContainers(t *testing.T, attemptID string) {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label="+StageAttemptLabel+"="+attemptID).Output()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("stage containers left behind: %q, %v", out, err)
	}
}

func TestDockerExecutorReviewVerdicts(t *testing.T) {
	cases := map[string]struct {
		diff, verdict string
		flags         []string
	}{
		"approve":           {diff: "+ok\n", verdict: "APPROVED"},
		"changes requested": {diff: "+x // FIXME later\n", verdict: "CHANGES_REQUESTED"},
		"result as a file":  {diff: "+ok\n", verdict: "APPROVED", flags: []string{"--file"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			executor, request := reviewFixture(t, tc.diff, tc.flags...)
			result, err := executor.Execute(context.Background(), request)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if result.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q", result.Verdict, tc.verdict)
			}
			data, err := os.ReadFile(request.Outputs[0].Path)
			if err != nil || !strings.Contains(string(data), "**Verdict:** "+tc.verdict) {
				t.Fatalf("review output = %q, %v", data, err)
			}
			meta := result.Metadata
			if meta["container.image"] != testImage || meta["container.image_pinned"] != "true" ||
				meta["container.network"] != "none" || meta["agent"] != "fixture-review" {
				t.Fatalf("metadata = %v", meta)
			}
			if meta["input_writable"] != "false" || meta["default_route"] != "false" {
				t.Fatalf("sandbox evidence = %v", meta)
			}
			if !strings.Contains(meta["container.mounts"], "in/diff:ro") || !strings.Contains(meta["container.mounts"], "out:rw") ||
				strings.Contains(meta["container.mounts"], "repo") {
				t.Fatalf("mounts = %q", meta["container.mounts"])
			}
			if result.LogPath == "" {
				t.Fatal("container log was not saved")
			}
			if diff, _ := os.ReadFile(request.Inputs[0].Path); string(diff) != tc.diff {
				t.Fatalf("input must stay untouched, got %q", diff)
			}
			noStageContainers(t, request.AttemptID)
		})
	}
}

func TestDockerExecutorFailuresAreTechnicalErrors(t *testing.T) {
	cases := map[string][]string{
		"error status":    {"--fail"},
		"container crash": {"--crash"},
		"not json":        {"--garbage"},
		"content and file": {
			"--both",
		},
		"file written, then undeclared output": {"--file-extra"},
	}
	for name, flags := range cases {
		t.Run(name, func(t *testing.T) {
			executor, request := reviewFixture(t, "+ok\n", flags...)
			result, err := executor.Execute(context.Background(), request)
			if err == nil {
				t.Fatal("a failed container must be an execution error")
			}
			if result.Metadata["container.image"] != testImage {
				t.Fatalf("failed attempt must keep the image provenance: %v", result.Metadata)
			}
			if _, statErr := os.Stat(request.Outputs[0].Path); statErr == nil {
				t.Fatal("no output may exist after a failed container")
			}
			noStageContainers(t, request.AttemptID)
		})
	}
}

func TestDockerExecutorMissingImageIsAnError(t *testing.T) {
	executor, request := reviewFixture(t, "+ok\n")
	executor.spec.Docker.Image = "alpine@sha256:" + strings.Repeat("0", 64)
	executor.launcher = DockerLauncher{Settings: *executor.spec.Docker}
	executor.spec.Docker.Pull = "never"
	executor.launcher = DockerLauncher{Settings: *executor.spec.Docker}
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("an image that cannot be started must be an execution error")
	}
	noStageContainers(t, request.AttemptID)
}

func TestDockerExecutorSecretsOnlyThroughAllowlist(t *testing.T) {
	t.Setenv("S10_ALLOWED", "allowed-secret-value")
	t.Setenv("S10_DENIED", "denied-secret-value")
	executor, request := reviewFixture(t, "+ok\n")
	executor.spec.EnvAllow = []string{"S10_ALLOWED"}
	executor.spec.Argv = []string{"/bin/sh", "-c", `cat >/dev/null
echo "a=$S10_ALLOWED d=$S10_DENIED" >&2
printf '{"schema_version":1,"status":"ok","outputs":{"review":{"content":"# R\\n\\n**Verdict:** APPROVED\\n"}}}'`}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	log, _ := os.ReadFile(result.LogPath)
	text := string(log)
	if !strings.Contains(text, "a=[redacted] d=") || strings.Contains(text, "allowed-secret-value") || strings.Contains(text, "denied-secret-value") {
		t.Fatalf("log = %s", text)
	}
	if !strings.Contains(result.Metadata["container.exceptions"], "S10_ALLOWED") ||
		strings.Contains(result.Metadata["container.exceptions"], "allowed-secret-value") {
		t.Fatalf("exceptions = %q", result.Metadata["container.exceptions"])
	}
}

func TestDockerExecutorCancellationRemovesContainer(t *testing.T) {
	executor, request := reviewFixture(t, "+ok\n")
	executor.spec.Argv = []string{"/bin/sh", "-c", "cat >/dev/null; sleep 60"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(3 * time.Second); cancel() }()
	started := time.Now()
	_, err := executor.Execute(ctx, request)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("err = %v", err)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("cancellation did not stop the container")
	}
	noStageContainers(t, request.AttemptID)
}

func TestDockerExecutorRefusesSymlinkInput(t *testing.T) {
	executor, request := reviewFixture(t, "+ok\n")
	link := filepath.Join(filepath.Dir(request.Inputs[0].Path), "link.patch")
	if err := os.Symlink("/etc/hosts", link); err != nil {
		t.Fatal(err)
	}
	request.Inputs[0].Path = link
	if _, err := executor.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "символическая") {
		t.Fatalf("a symlink input must be refused: %v", err)
	}
}

func TestDockerLauncherSweepsOnlyOldStageContainers(t *testing.T) {
	requireDocker(t)
	start := func(name string, labels ...string) {
		t.Helper()
		args := []string{"run", "-d", "--name", name, "--network", "none"}
		for _, l := range labels {
			args = append(args, "--label", l)
		}
		args = append(args, testImage, "sleep", "120")
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	}
	old := time.Now().Add(-OrphanAge - time.Hour).Unix()
	suffix := time.Now().Format("150405.000")
	suffix = strings.ReplaceAll(suffix, ".", "")
	oldName, freshName := "s10sweep-old-"+suffix, "s10sweep-fresh-"+suffix
	start(oldName, "ai-team.dockerrun=1", StageAttemptLabel+"=x", "ai-team.started="+strconv.FormatInt(old, 10))
	start(freshName, "ai-team.dockerrun=1", StageAttemptLabel+"=x", "ai-team.started="+strconv.FormatInt(time.Now().Unix(), 10))
	removed, err := DockerLauncher{}.SweepOrphans(context.Background())
	if err != nil || len(removed) != 1 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	exists := func(name string) bool {
		out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "name=^"+name+"$").Output()
		return strings.TrimSpace(string(out)) != ""
	}
	if exists(oldName) || !exists(freshName) {
		t.Fatalf("old exists=%v fresh exists=%v", exists(oldName), exists(freshName))
	}
}

func TestDockerExecutorNetworkIsAnExplicitRecordedException(t *testing.T) {
	executor, request := reviewFixture(t, "+ok\n")
	executor.spec.Docker.Network = "bridge"
	executor.launcher = DockerLauncher{Settings: *executor.spec.Docker}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Metadata["default_route"] != "true" || result.Metadata["container.network"] != "bridge" ||
		!strings.Contains(result.Metadata["container.exceptions"], "network=bridge") {
		t.Fatalf("metadata = %v", result.Metadata)
	}
}

func TestDockerExecutorOutputDirectoryIsCapped(t *testing.T) {
	executor, request := reviewFixture(t, "+ok\n", "--flood")
	executor.spec.Docker.OutputMB = 8
	executor.launcher = DockerLauncher{Settings: *executor.spec.Docker}
	started := time.Now()
	_, err := executor.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "превысил лимит 8 МБ") {
		t.Fatalf("an overflowing result directory must stop the container: %v", err)
	}
	if time.Since(started) > 60*time.Second {
		t.Fatal("the flooding container was not stopped")
	}
	if _, statErr := os.Stat(request.Outputs[0].Path); statErr == nil {
		t.Fatal("no output may exist after the overflow")
	}
	noStageContainers(t, request.AttemptID)
}

func TestDockerExecutorExplicitUserCanWriteResultFile(t *testing.T) {
	executor, request := reviewFixture(t, "+ok\n", "--file")
	executor.spec.Docker.User = "4242:4242"
	executor.launcher = DockerLauncher{Settings: *executor.spec.Docker}
	if _, err := executor.Execute(context.Background(), request); err != nil {
		t.Fatalf("an explicit non-controller user must be able to write the result: %v", err)
	}
	if data, err := os.ReadFile(request.Outputs[0].Path); err != nil || !strings.Contains(string(data), "**Verdict:** APPROVED") {
		t.Fatalf("review = %q, %v", data, err)
	}
}

func TestStagedResultFilesAreWrittenOnlyAfterTheContractCheck(t *testing.T) {
	dir := t.TempDir()
	request := Request{Outputs: []Output{{Name: "review", Path: filepath.Join(dir, "artifacts", "review.md")}}}
	resultDir := filepath.Join(dir, "out")
	if err := os.Mkdir(resultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "review"), []byte("# R\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged := map[string][]byte{}
	if err := stageResultFiles(request, resultDir, []byte(`{"status":"ok"}`), staged); err != nil {
		t.Fatal(err)
	}
	if string(staged["review"]) != "# R\n" {
		t.Fatalf("staged = %v", staged)
	}
	if _, err := os.Stat(request.Outputs[0].Path); err == nil {
		t.Fatal("staging must not write the declared path")
	}
	if err := writeStagedFiles(request, staged); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(request.Outputs[0].Path); string(data) != "# R\n" {
		t.Fatalf("committed = %q", data)
	}
	if err := stageResultFiles(request, resultDir, []byte(`{"status":"error"}`), map[string][]byte{}); err != nil {
		t.Fatalf("a non-ok status stages nothing and is not this layer's error: %v", err)
	}
}

func TestDockerSpecOutputMBValidation(t *testing.T) {
	spec := AdapterSpec{Argv: []string{"x"}, Docker: &DockerSpec{Image: testImage, OutputMB: MaxDockerOutputMB + 1}}
	if err := spec.Validate("review"); err == nil {
		t.Fatal("an output_mb above the maximum must be rejected")
	}
	spec.Docker.OutputMB = -1
	if err := spec.Validate("review"); err == nil {
		t.Fatal("a negative output_mb must be rejected")
	}
	spec.Docker.OutputMB = 8
	if err := spec.Validate("review"); err != nil {
		t.Fatalf("valid output_mb rejected: %v", err)
	}
}
