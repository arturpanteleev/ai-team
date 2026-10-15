//go:build !windows

package gaterun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const testSecret = "s3cr3t-value-0123456789abcdef"

type fixture struct {
	repo, evidence string
	runner         *Runner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := t.TempDir()
	evidence := t.TempDir()
	script, err := filepath.Abs("testdata/demo-gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "artifact.md"), []byte("# Spec\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(script, filepath.Join(repo, "demo-gate.sh")); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SECRET_TOKEN": testSecret, "PLAIN": "plain-value", "HOST_ONLY": "must-not-leak", "GH_TEXT": "ghp_abcdefghijklmnopqrstuvwxyz0123456789"}
	return &fixture{repo: repo, evidence: evidence, runner: &Runner{
		EvidenceRoot: evidence,
		LookupEnv:    func(name string) (string, bool) { v, ok := env[name]; return v, ok },
	}}
}

var attemptSeq atomic.Int64

func (f *fixture) request(mode string, mutate ...func(*Request)) Request {
	req := Request{
		Attempt: workflow.GateAttempt{
			TaskID: "task-1", Stage: "spec", Visit: 1, ArtifactSHA256: strings.Repeat("a", 64),
			Gate: gatelib.GateExternalProgram, GateVersion: 1, GateName: "demo", Mode: workflow.GateModeRequired,
			AttemptID: fmt.Sprintf("att-%d", attemptSeq.Add(1)),
		},
		Config: Config{
			Command: []string{"/bin/sh", filepath.Join(f.repo, "demo-gate.sh"), mode},
			Timeout: 10 * time.Second, CWD: ".", MaxOutputBytes: 64 << 10,
			EnvAllow: []string{"SECRET_TOKEN", "PLAIN"},
		},
		Artifact: ArtifactInfo{Type: "md", SHA256: strings.Repeat("a", 64), Path: filepath.Join(f.repo, "artifact.md")},
		RepoRoot: f.repo,
	}
	for _, m := range mutate {
		m(&req)
	}
	return req
}

func (f *fixture) run(t *testing.T, mode string, mutate ...func(*Request)) workflow.GateAttempt {
	t.Helper()
	attempt, err := f.runner.Run(context.Background(), f.request(mode, mutate...))
	if err != nil {
		t.Fatalf("%s: %v", mode, err)
	}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("%s: invalid attempt: %v", mode, err)
	}
	return attempt
}

func TestDemoPassFailErrorAreDistinct(t *testing.T) {
	f := newFixture(t)
	pass := f.run(t, "pass")
	fail := f.run(t, "fail")
	crash := f.run(t, "crash")

	if pass.Execution != workflow.GateExecutionSucceeded || pass.Verdict != workflow.GateVerdictPass || pass.Blocking() {
		t.Errorf("pass: %+v", pass)
	}
	// A valid fail is a successful execution with a negative verdict.
	if fail.Execution != workflow.GateExecutionSucceeded || fail.Verdict != workflow.GateVerdictFail || !fail.Blocking() || fail.Reason != "section Risks is missing" {
		t.Errorf("fail: %+v", fail)
	}
	// A crash is a technical error and carries no verdict.
	if crash.Execution != workflow.GateExecutionError || crash.Verdict != "" || !crash.Blocking() || !strings.Contains(crash.Reason, "code 3") || !strings.Contains(crash.Reason, "boom") {
		t.Errorf("crash: %+v", crash)
	}
	if pass.Executor != workflow.GateKindExternal {
		t.Errorf("executor = %s", pass.Executor)
	}
	na := f.run(t, "not_applicable")
	if na.Verdict != workflow.GateVerdictNotApplicable || na.Blocking() {
		t.Errorf("not_applicable: %+v", na)
	}
}

func TestCheckModeReadsArtifact(t *testing.T) {
	f := newFixture(t)
	if got := f.run(t, "check"); got.Verdict != workflow.GateVerdictPass {
		t.Fatalf("clean artifact: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "artifact.md"), []byte("TODO finish\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.run(t, "check"); got.Verdict != workflow.GateVerdictFail {
		t.Fatalf("TODO artifact: %+v", got)
	}
}

func TestProtocolViolationsAreNeverPass(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []string{"badjson", "twodocs", "flood", "lost", "crash"} {
		attempt := f.run(t, mode, func(r *Request) { r.Config.MaxOutputBytes = 4096 })
		if attempt.Execution != workflow.GateExecutionError {
			t.Errorf("%s: execution = %s, want error (%+v)", mode, attempt.Execution, attempt)
		}
		if attempt.Verdict != "" || !attempt.Blocking() {
			t.Errorf("%s: produced verdict %q or does not block", mode, attempt.Verdict)
		}
	}
}

func TestOutputLimitStopsProcess(t *testing.T) {
	f := newFixture(t)
	started := time.Now()
	attempt := f.run(t, "flood", func(r *Request) { r.Config.MaxOutputBytes = 1024 })
	if !strings.Contains(attempt.Reason, "output limit") {
		t.Fatalf("reason = %q", attempt.Reason)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("flooding process was not stopped promptly")
	}
	data, err := os.ReadFile(filepath.Join(f.evidence, attempt.AttemptID, "stdout.log"))
	if err != nil || len(data) > 1024 {
		t.Fatalf("stdout.log not bounded: %d bytes, %v", len(data), err)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "orphan")
	started := time.Now()
	attempt := f.run(t, "hang", func(r *Request) {
		r.Config.Timeout = time.Second
		// The background child would write the marker if it survived.
		r.Config.Command = []string{"/bin/sh", "-c", `(sleep 2; echo alive > "$0") & sleep 30`, marker}
	})
	if attempt.Execution != workflow.GateExecutionTimeout || attempt.Verdict != "" || !attempt.Blocking() {
		t.Fatalf("attempt = %+v", attempt)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("timeout did not stop the process promptly")
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("descendant of the gate survived the timeout")
	}
}

func TestCancellation(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	attempt, err := f.runner.Run(ctx, f.request("hang"))
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Execution != workflow.GateExecutionCanceled || attempt.Verdict != "" {
		t.Fatalf("attempt = %+v", attempt)
	}
}

func TestStartFailureIsError(t *testing.T) {
	f := newFixture(t)
	attempt := f.run(t, "pass", func(r *Request) { r.Config.Command = []string{"/nonexistent/program"} })
	if attempt.Execution != workflow.GateExecutionError || attempt.Verdict != "" {
		t.Fatalf("attempt = %+v", attempt)
	}
}

func TestEnvironmentAllowlist(t *testing.T) {
	f := newFixture(t)
	attempt := f.run(t, "pass", func(r *Request) {
		r.Config.Command = []string{"/usr/bin/env"}
		r.Config.EnvAllow = []string{"PLAIN", "UNSET_VAR"}
	})
	// env prints the environment, not a result: error, but the log shows what
	// the program could see.
	if attempt.Execution != workflow.GateExecutionError {
		t.Fatalf("attempt = %+v", attempt)
	}
	log, err := os.ReadFile(filepath.Join(f.evidence, attempt.AttemptID, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	for _, want := range []string{"PLAIN=plain-value", "AI_TEAM_GATE_ATTEMPT_ID=" + attempt.AttemptID, "AI_TEAM_GATE_PROTOCOL=" + Protocol} {
		if !strings.Contains(text, want) {
			t.Errorf("env lacks %q:\n%s", want, text)
		}
	}
	for _, banned := range []string{"HOST_ONLY", "SECRET_TOKEN", "must-not-leak", "PATH=", "HOME="} {
		if strings.Contains(text, banned) {
			t.Errorf("env leaked %q:\n%s", banned, text)
		}
	}
}

func TestRelativeProgramAndCWD(t *testing.T) {
	f := newFixture(t)
	sub := filepath.Join(f.repo, "tools")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	attempt := f.run(t, "pass", func(r *Request) {
		r.Config.Command = []string{"./demo-gate.sh", "pass"}
		r.Config.CWD = "tools" // relative program paths resolve against the repo root, not cwd
	})
	if attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v", attempt)
	}
	attempt = f.run(t, "pass", func(r *Request) { r.Config.CWD = "missing" })
	if attempt.Execution != workflow.GateExecutionError {
		t.Fatalf("missing cwd: %+v", attempt)
	}
	if strings.Contains(attempt.Reason, f.repo) {
		t.Fatalf("reason leaks host path: %q", attempt.Reason)
	}
}

func TestSecretsNeverReachAttemptOrFiles(t *testing.T) {
	f := newFixture(t)
	attempt := f.run(t, "leak", func(r *Request) {
		// A secret-looking argument is rejected up front; this one is passed
		// through the allowed environment instead.
		r.Config.EnvAllow = []string{"SECRET_TOKEN"}
	})
	if attempt.Verdict != workflow.GateVerdictFail {
		t.Fatalf("attempt = %+v", attempt)
	}
	if !strings.Contains(attempt.Reason, "[REDACTED:env]") {
		t.Errorf("reason not redacted: %q", attempt.Reason)
	}
	assertNoSecret(t, f.evidence, testSecret, attempt)
}

func TestStderrSecretPatternsRedacted(t *testing.T) {
	f := newFixture(t)
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	attempt := f.run(t, "crash", func(r *Request) {
		r.Config.Command = []string{"/bin/sh", "-c", `echo "login failed with $GH_TEXT" >&2; exit 1`}
		r.Config.EnvAllow = []string{"GH_TEXT"}
	})
	if attempt.Execution != workflow.GateExecutionError {
		t.Fatalf("attempt = %+v", attempt)
	}
	assertNoSecret(t, f.evidence, token, attempt)
}

func assertNoSecret(t *testing.T, root, secret string, attempt workflow.GateAttempt) {
	t.Helper()
	encoded := fmt.Sprintf("%+v", attempt)
	if strings.Contains(encoded, secret) {
		t.Errorf("attempt contains the secret: %s", encoded)
	}
	found := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		found++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("%s contains the secret", path)
		}
		return nil
	})
	if err != nil || found == 0 {
		t.Fatalf("walk: files=%d err=%v", found, err)
	}
}

func TestInlineSecretInCommandRejected(t *testing.T) {
	f := newFixture(t)
	for _, arg := range []string{"--token=ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "api_key=AbCdEf0123456789XyZ+/="} {
		req := f.request("pass", func(r *Request) { r.Config.Command = []string{"/bin/true", arg} })
		if _, err := f.runner.Run(context.Background(), req); err == nil || strings.Contains(err.Error(), "0123456789") {
			t.Errorf("arg %q: err = %v", arg, err)
		}
	}
}

func TestEvidenceStoredAndVerified(t *testing.T) {
	f := newFixture(t)
	attempt := f.run(t, "evidence")
	if attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v", attempt)
	}
	var report *workflow.GateEvidenceRef
	for i, ref := range attempt.Evidence {
		if strings.HasPrefix(ref.Path, "/") || strings.Contains(ref.Path, "..") {
			t.Errorf("unsafe evidence path %q", ref.Path)
		}
		if ref.Kind == "report" {
			report = &attempt.Evidence[i]
		}
	}
	if report == nil || report.SHA256 != hashHex([]byte("checked 3 items\n")) {
		t.Fatalf("evidence = %+v", attempt.Evidence)
	}
	data, err := os.ReadFile(filepath.Join(f.evidence, filepath.FromSlash(report.Path)))
	if err != nil || string(data) != "checked 3 items\n" {
		t.Fatalf("stored evidence: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(f.evidence, attempt.AttemptID, "work")); err == nil {
		t.Error("raw work directory kept")
	}
}

func TestEvidenceViolationsMakeError(t *testing.T) {
	f := newFixture(t)
	result := func(path, extra string) string {
		return fmt.Sprintf(`printf '{"protocol":"ai-team.gate/v1","verdict":"pass","evidence":[{"kind":"report","path":"%s"%s}]}'`, path, extra)
	}
	cases := map[string]string{
		"missing file":  result("nope.txt", ""),
		"wrong digest":  `echo hi > "$D/a.txt"; ` + result("a.txt", `,"sha256":"`+strings.Repeat("0", 64)+`"`),
		"secret inside": `echo "password=` + testSecret + `" > "$D/a.txt"; ` + result("a.txt", ""),
		"env value":     `echo "x $SECRET_TOKEN" > "$D/a.txt"; ` + result("a.txt", ""),
		"symlink":       `ln -s /etc/hosts "$D/a.txt"; ` + result("a.txt", ""),
		"symlinked dir": `mkdir "$D/../outside"; echo hi > "$D/../outside/a.txt"; ln -s ../outside "$D/sub"; ` + result("sub/a.txt", ""),
		"binary":        `printf 'a\000b' > "$D/a.txt"; ` + result("a.txt", ""),
		"too large":     `head -c 2000000 /dev/zero | tr '\000' 'a' > "$D/a.txt"; ` + result("a.txt", ""),
		"escaping path": result("../x", ""),
		"directory":     `mkdir "$D/a.txt"; ` + result("a.txt", ""),
	}
	for name, script := range cases {
		script := `D=$(sed -n 's/.*"evidence_dir":"\([^"]*\)".*/\1/p'); ` + script
		attempt := f.run(t, "pass", func(r *Request) {
			r.Config.Command = []string{"/bin/sh", "-c", script}
			r.Config.EnvAllow = []string{"SECRET_TOKEN"}
		})
		if attempt.Execution != workflow.GateExecutionError || attempt.Verdict != "" {
			t.Errorf("%s: attempt = %+v", name, attempt)
		}
		for _, ref := range attempt.Evidence {
			if ref.Kind == "report" {
				t.Errorf("%s: unsafe evidence stored: %+v", name, ref)
			}
		}
	}
}

func TestIdempotentByAttemptID(t *testing.T) {
	f := newFixture(t)
	counter := filepath.Join(t.TempDir(), "count")
	mutate := func(r *Request) {
		r.Attempt.AttemptID = "fixed-1"
		r.Config.Command = []string{"/bin/sh", "-c", `echo run >> "$0"; cat >/dev/null; printf '{"protocol":"ai-team.gate/v1","verdict":"pass"}'`, counter}
	}
	var wg sync.WaitGroup
	results := make([]workflow.GateAttempt, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := f.runner.Run(context.Background(), f.request("pass", mutate))
			if err != nil {
				t.Error(err)
			}
			results[i] = a
		}()
	}
	wg.Wait()
	again := f.run(t, "pass", mutate)
	data, _ := os.ReadFile(counter)
	if runs := strings.Count(string(data), "run"); runs != 1 {
		t.Fatalf("program ran %d times for one attempt ID", runs)
	}
	for _, a := range append(results, again) {
		if a.AttemptID != "fixed-1" || a.Verdict != workflow.GateVerdictPass || !a.FinishedAt.Equal(again.FinishedAt) {
			t.Fatalf("attempt differs from the first: %+v vs %+v", a, again)
		}
	}
}

func TestAttemptIDReuseForOtherCheckRejected(t *testing.T) {
	f := newFixture(t)
	f.run(t, "pass", func(r *Request) { r.Attempt.AttemptID = "reused" })
	_, err := f.runner.Run(context.Background(), f.request("pass", func(r *Request) {
		r.Attempt.AttemptID = "reused"
		r.Attempt.ArtifactSHA256 = strings.Repeat("b", 64)
	}))
	if err == nil || !strings.Contains(err.Error(), "different check") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryGetsNewAttemptAndKeepsOld(t *testing.T) {
	f := newFixture(t)
	first := f.run(t, "crash")
	second := f.run(t, "pass")
	if first.AttemptID == second.AttemptID || first.Execution != workflow.GateExecutionError || second.Verdict != workflow.GateVerdictPass {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if _, err := os.Stat(filepath.Join(f.evidence, first.AttemptID, "attempt.json")); err != nil {
		t.Fatalf("earlier attempt erased: %v", err)
	}
}

func TestUncommittedAttemptIsRerun(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.evidence, "half")
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt := f.run(t, "pass", func(r *Request) { r.Attempt.AttemptID = "half" })
	if attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v", attempt)
	}
}

func TestSymlinkedAttemptDirRejected(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(f.evidence, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Run(context.Background(), f.request("pass", func(r *Request) { r.Attempt.AttemptID = "linked" })); err == nil {
		t.Fatal("symlinked attempt directory accepted")
	}
}

// fakeLauncher stands in for a second adapter (Docker): it speaks the same
// protocol through the same Runner without any host process.
type fakeLauncher struct{ got LaunchSpec }

func (*fakeLauncher) Kind() workflow.GateKind { return workflow.GateKindDocker }
func (l *fakeLauncher) Launch(_ context.Context, spec LaunchSpec) Outcome {
	l.got = spec
	return Outcome{Stdout: []byte(`{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"container says no"}`)}
}

func TestSecondLauncherReusesProtocolAndClassification(t *testing.T) {
	f := newFixture(t)
	fake := &fakeLauncher{}
	f.runner.Launcher = fake
	attempt := f.run(t, "pass")
	if attempt.Executor != workflow.GateKindDocker || attempt.Verdict != workflow.GateVerdictFail || attempt.Reason != "container says no" {
		t.Fatalf("attempt = %+v", attempt)
	}
	if fake.got.Input.AttemptID != attempt.AttemptID || fake.got.Input.Artifact.Type != "md" || !strings.Contains(string(fake.got.Stdin), `"protocol":"ai-team.gate/v1"`) {
		t.Fatalf("adapter did not receive the protocol input: %+v", fake.got.Input)
	}
}

func TestInputDocumentContent(t *testing.T) {
	f := newFixture(t)
	fake := &fakeLauncher{}
	f.runner.Launcher = fake
	f.run(t, "pass")
	in := fake.got.Input
	if in.TaskID != "task-1" || in.Stage != "spec" || in.Visit != 1 || in.Gate.Name != "demo" || in.Artifact.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("input = %+v", in)
	}
	if !filepath.IsAbs(in.Paths.EvidenceDir) || in.Limits.TimeoutSeconds != 10 {
		t.Fatalf("input paths/limits = %+v %+v", in.Paths, in.Limits)
	}
	for _, entry := range fake.got.Env {
		if strings.HasPrefix(entry, "HOST_ONLY") {
			t.Fatal("non-allowlisted variable passed")
		}
	}
}

func TestParseParamsAndLibrary(t *testing.T) {
	def, err := gatelib.Default().Lookup(gatelib.GateExternalProgram, 1)
	if err != nil || def.Kind != workflow.GateKindExternal {
		t.Fatalf("definition: %+v %v", def, err)
	}
	cfg, err := ParseParams(map[string]any{
		"command": []any{"./gate.sh", "--flag"}, "timeout_seconds": 5, "cwd": "tools",
		"env_allow": []any{"PLAIN"}, "max_output_bytes": 2048,
	})
	if err != nil || cfg.Timeout != 5*time.Second || cfg.CWD != "tools" || cfg.MaxOutputBytes != 2048 || len(cfg.EnvAllow) != 1 {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
	cfg, err = ParseParams(map[string]any{"command": []any{"true"}})
	if err != nil || cfg.Timeout != DefaultTimeout || cfg.CWD != "." || cfg.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	bad := []map[string]any{
		{},
		{"command": []any{"true"}, "cwd": "/etc"},
		{"command": []any{"true"}, "cwd": "../x"},
		{"command": []any{"true"}, "timeout_seconds": 0},
		{"command": []any{"true"}, "timeout_seconds": 99999},
		{"command": []any{"true"}, "max_output_bytes": 1 << 40},
		{"command": []any{"true"}, "env_allow": []any{"A=B"}},
		{"command": []any{"true"}, "env_allow": []any{"AI_TEAM_GATE_X"}},
		{"command": []any{"true"}, "shell": true},
		{"command": []any{"--token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"}},
	}
	for _, params := range bad {
		if _, err := ParseParams(params); err == nil {
			t.Errorf("accepted %v", params)
		}
	}
}
