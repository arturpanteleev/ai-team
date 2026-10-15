//go:build !windows

package gaterun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const failResult = `{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"real opinion"}`

// forgedAttempt is a finished, passing record for the request's identity.
func forgedAttempt(t *testing.T, req Request) string {
	t.Helper()
	forged := req.Attempt
	forged.Executor = workflow.GateKindExternal
	forged.Mode = forged.Mode.Effective()
	forged.Execution, forged.Verdict = workflow.GateExecutionSucceeded, workflow.GateVerdictPass
	forged.StartedAt = time.Now().UTC().Add(-time.Second)
	forged.FinishedAt = time.Now().UTC()
	if err := forged.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestProgramCannotForgeAttemptRecord(t *testing.T) {
	f := newFixture(t)
	req := f.request("pass", func(r *Request) { r.Attempt.AttemptID = "forge-1" })
	attemptDir := filepath.Join(f.evidence, "forge-1")
	// The worst case: the program somehow knows the controller's attempt
	// directory and writes a passing record there before reporting a failure.
	req.Config.Command = []string{"/bin/sh", "-c", `cat >/dev/null; printf '%s' "$1" > "$0/attempt.json"; printf '%s' "$2"`, attemptDir, forgedAttempt(t, req), failResult}

	first, err := f.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Verdict == workflow.GateVerdictPass || first.Execution == workflow.GateExecutionSucceeded || !first.Blocking() {
		t.Fatalf("forged record produced a pass: %+v", first)
	}
	again, err := f.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Verdict == workflow.GateVerdictPass || again.Execution != first.Execution || !again.FinishedAt.Equal(first.FinishedAt) {
		t.Fatalf("second run returned %+v, first was %+v", again, first)
	}
}

func TestProgramEvidenceDirIsOutsideAttemptDir(t *testing.T) {
	f := newFixture(t)
	var seen string
	f.runner.Launcher = launcherFunc(func(spec LaunchSpec) Outcome {
		seen = spec.Input.Paths.EvidenceDir
		return Outcome{Stdout: []byte(`{"protocol":"ai-team.gate/v1","verdict":"pass"}`)}
	})
	f.run(t, "pass")
	if seen == "" || strings.HasPrefix(seen, f.evidence) {
		t.Fatalf("evidence_dir %q is inside the evidence root %q", seen, f.evidence)
	}
	if _, err := os.Stat(seen); err == nil {
		t.Fatal("work directory was not removed")
	}
}

func TestTamperedStoredAttemptIsRejected(t *testing.T) {
	f := newFixture(t)
	req := f.request("fail", func(r *Request) { r.Attempt.AttemptID = "tamper-1" })
	if got, err := f.runner.Run(context.Background(), req); err != nil || got.Verdict != workflow.GateVerdictFail {
		t.Fatalf("first run: %+v %v", got, err)
	}
	path := filepath.Join(f.evidence, "tamper-1", "attempt.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(forgedAttempt(t, req)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := f.runner.Run(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("forged stored record accepted: %+v, %v", got, err)
	}
	if got.Verdict == workflow.GateVerdictPass {
		t.Fatalf("forged pass returned: %+v", got)
	}

	// A record copied under another attempt ID does not verify either.
	other := f.request("fail", func(r *Request) { r.Attempt.AttemptID = "tamper-2" })
	if _, err := f.runner.Run(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.evidence, "tamper-2", "attempt.json")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(f.evidence, "tamper-1", "attempt.json"))
	if err := os.WriteFile(filepath.Join(f.evidence, "tamper-2", "attempt.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Run(context.Background(), other); err == nil {
		t.Fatal("record with a foreign signature accepted")
	}
}

func TestBackgroundDescendantIsKilledAfterExit(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "orphan")
	attempt := f.run(t, "pass", func(r *Request) {
		// The program exits cleanly at once; its background child would write
		// the marker a second later if it survived.
		r.Config.Command = []string{"/bin/sh", "-c", `(sleep 1; echo alive > "$0") >/dev/null 2>&1 & cat >/dev/null; printf '{"protocol":"ai-team.gate/v1","verdict":"pass"}'`, marker}
	})
	if attempt.Verdict != workflow.GateVerdictPass {
		t.Fatalf("attempt = %+v", attempt)
	}
	time.Sleep(1800 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("background descendant outlived the gate")
	}
}

func TestEveryAllowedEnvValueIsMasked(t *testing.T) {
	f := newFixture(t)
	f.runner.LookupEnv = func(name string) (string, bool) {
		return map[string]string{"BUILD_FLAVOUR": "hunter22-flavour", "SHORT": "ab"}[name], name == "BUILD_FLAVOUR" || name == "SHORT"
	}
	attempt := f.run(t, "pass", func(r *Request) {
		r.Config.EnvAllow = []string{"BUILD_FLAVOUR", "SHORT"}
		r.Config.Command = []string{"/bin/sh", "-c", `cat >/dev/null; printf '{"protocol":"ai-team.gate/v1","verdict":"fail","reason":"flavour %s"}' "$BUILD_FLAVOUR"`}
	})
	if strings.Contains(attempt.Reason, "hunter22-flavour") || !strings.Contains(attempt.Reason, "[REDACTED:env]") {
		t.Fatalf("env value with a harmless name was not masked: %q", attempt.Reason)
	}
}

func TestRelativeProgramCannotEscapeRepository(t *testing.T) {
	f := newFixture(t)
	for _, command := range []string{"../tool", "tools/../../tool", "./../tool"} {
		req := f.request("pass", func(r *Request) { r.Config.Command = []string{command} })
		if _, err := f.runner.Run(context.Background(), req); err == nil {
			t.Errorf("%q accepted", command)
		}
	}
	// Staying inside while using .. is fine.
	if err := (Config{Command: []string{"tools/../demo-gate.sh"}, Timeout: time.Second, CWD: ".", MaxOutputBytes: 1024}).Validate(); err != nil {
		t.Errorf("inner .. rejected: %v", err)
	}
}

type launcherFunc func(LaunchSpec) Outcome

func (launcherFunc) Kind() workflow.GateKind { return workflow.GateKindExternal }
func (l launcherFunc) Launch(_ context.Context, spec LaunchSpec) Outcome {
	return l(spec)
}
