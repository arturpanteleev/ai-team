package stageexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func shellExecutor(t *testing.T, spec AdapterSpec, body string) (*External, Request) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	spec.Argv = []string{script}
	return NewExternal("demo", spec), Request{
		RunID: "run", AttemptID: "run-001-stage", Stage: "stage", Feature: "feat", Task: "task",
		WorkDir: dir, ArtifactRoot: dir, LogDir: filepath.Join(dir, "logs"),
		Outputs: []Output{{Name: "result", Path: filepath.Join(dir, "artifacts", "result.md")}},
	}
}

func TestExternalWritesContentOutputAndReturnsProvenance(t *testing.T) {
	executor, request := shellExecutor(t, AdapterSpec{}, `cat > "$0.request"
printf '{"schema_version":1,"status":"ok","summary":"s","verdict":"APPROVED","outputs":{"result":{"content":"# R\\n"}},"metadata":{"tool_version":"1","api_token":"abc"}}'
`)
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Identity != (Identity{Kind: KindExternal, Name: "demo"}) || result.Verdict != "APPROVED" || result.Summary != "s" ||
		result.Metadata["tool_version"] != "1" || result.Metadata["api_token"] != "[redacted]" || result.LogPath == "" {
		t.Fatalf("result = %+v", result)
	}
	if data, err := os.ReadFile(request.Outputs[0].Path); err != nil || string(data) != "# R\n" {
		t.Fatalf("output = %q, %v", data, err)
	}
	doc, err := os.ReadFile(request.WorkDir + "/run.sh.request")
	if err != nil || !strings.Contains(string(doc), `"schema_version":1`) || !strings.Contains(string(doc), request.Outputs[0].Path) {
		t.Fatalf("request document = %s, %v", doc, err)
	}
}

func TestExternalFailuresAreErrors(t *testing.T) {
	cases := map[string]string{
		"exit code":         "cat >/dev/null; exit 2\n",
		"empty stdout":      "cat >/dev/null\n",
		"two documents":     "cat >/dev/null; printf '{\"schema_version\":1,\"status\":\"ok\"}{\"a\":1}'\n",
		"unknown field":     "cat >/dev/null; printf '{\"schema_version\":1,\"status\":\"ok\",\"approve\":true}'\n",
		"wrong version":     "cat >/dev/null; printf '{\"schema_version\":2,\"status\":\"ok\"}'\n",
		"bad status":        "cat >/dev/null; printf '{\"schema_version\":1,\"status\":\"done\"}'\n",
		"error status":      "cat >/dev/null; printf '{\"schema_version\":1,\"status\":\"error\",\"error\":\"x\"}'\n",
		"undeclared out":    "cat >/dev/null; printf '{\"schema_version\":1,\"status\":\"ok\",\"outputs\":{\"other\":{\"content\":\"x\"}}}'\n",
		"too much metadata": "cat >/dev/null; printf '{\"schema_version\":1,\"status\":\"ok\",\"metadata\":{\"BAD KEY\":\"x\"}}'\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			executor, request := shellExecutor(t, AdapterSpec{}, body)
			if _, err := executor.Execute(context.Background(), request); err == nil {
				t.Fatal("expected an execution error")
			}
		})
	}
}

func TestExternalStopsOnOwnTimeoutWithDistinctError(t *testing.T) {
	executor, request := shellExecutor(t, AdapterSpec{Timeout: "300ms"}, "cat >/dev/null; sleep 30\n")
	started := time.Now()
	_, err := executor.Execute(context.Background(), request)
	if !errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("process was not stopped at its timeout")
	}
}

func TestExternalCancellationKeepsContextError(t *testing.T) {
	executor, request := shellExecutor(t, AdapterSpec{}, "cat >/dev/null; sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	if _, err := executor.Execute(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestExternalCapsResultAndRedactsAllowedEnvInLog(t *testing.T) {
	t.Setenv("S09_UNIT_ALLOWED", "super-secret-value")
	t.Setenv("S09_UNIT_DENIED", "denied-value-xyz")
	executor, request := shellExecutor(t, AdapterSpec{MaxResultBytes: 64, EnvAllow: []string{"S09_UNIT_ALLOWED"}},
		"cat >/dev/null\necho \"a=$S09_UNIT_ALLOWED d=$S09_UNIT_DENIED p=$AI_TEAM_STAGE\" >&2\nhead -c 4096 /dev/zero | tr '\\0' 'x'\n")
	result, err := executor.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "превышает") {
		t.Fatalf("oversized result must be an error: %v", err)
	}
	log, readErr := os.ReadFile(result.LogPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(log)
	if strings.Contains(text, "super-secret-value") || strings.Contains(text, "denied-value-xyz") ||
		!strings.Contains(text, "a=[redacted] d= p=stage") {
		t.Fatalf("log = %s", text)
	}
}

func TestAdapterSpecValidationAndStrictYAML(t *testing.T) {
	for name, spec := range map[string]AdapterSpec{
		"no argv":        {},
		"bad timeout":    {Argv: []string{"x"}, Timeout: "soon"},
		"reserved env":   {Argv: []string{"x"}, EnvAllow: []string{"AI_TEAM_X"}},
		"bad env name":   {Argv: []string{"x"}, EnvAllow: []string{"A=B"}},
		"negative bytes": {Argv: []string{"x"}, MaxResultBytes: -1},
	} {
		if err := spec.Validate("demo"); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if err := (AdapterSpec{Argv: []string{"./x", "--flag"}, Timeout: "1m", EnvAllow: []string{"PATH"}}).Validate("demo"); err != nil {
		t.Fatal(err)
	}
	var spec AdapterSpec
	if err := yaml.Unmarshal([]byte("argv: [x]\ntimout: 5m\n"), &spec); err == nil {
		t.Fatal("a misspelled key must be rejected")
	}
	if err := yaml.Unmarshal([]byte("argv: [x]\ntimeout: 5m\n"), &spec); err != nil || spec.Timeout != "5m" {
		t.Fatalf("spec = %+v, %v", spec, err)
	}
}
