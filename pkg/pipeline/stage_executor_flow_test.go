package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aitruntime "github.com/arturpanteleev/ai-team/pkg/runtime"
)

// S09: verdict cross-check, retry and cancellation of an external stage.

func TestRun_ExternalExecutorVerdictMismatchFailsStage(t *testing.T) {
	dir := env(t)
	writeExecutorScript(t, dir, `cat >/dev/null
printf '{"schema_version":1,"status":"ok","verdict":"APPROVED","outputs":{"result":{"content":"# Tech\\n\\n**Verdict:** REJECTED\\n"}}}'
`)
	capture, err := runExternalExecutor(t, dir)
	if err == nil {
		t.Fatal("a verdict claim that disagrees with the outputs must fail the run")
	}
	if len(capture.calls) != 1 || capture.calls[0].Status == "passed" || capture.calls[0].Err == nil ||
		!strings.Contains(capture.calls[0].Err.Error(), "заявил verdict") {
		t.Fatalf("stage results = %+v", capture.calls)
	}
}

func newExternalPipeline(t *testing.T, capture *captureNotifier) *Pipeline {
	t.Helper()
	registry := externalExecutorRegistry()
	return New(externalExecutorConfig(t, registry, "demo"), registry,
		WithRuntimeFactory(func(string) (aitruntime.Runtime, error) {
			return nil, errors.New("internal runtime must not be created for an external executor")
		}),
		WithNotifier(capture), WithPrompter(&scriptedPrompter{}))
}

func TestRun_ExternalExecutorRetryAfterFailureStartsNewAttempt(t *testing.T) {
	dir := env(t)
	writeExecutorScript(t, dir, "cat >/dev/null\nexit 3\n")
	capture := &captureNotifier{}
	first, err := newExternalPipeline(t, capture).RunWithResult(context.Background(),
		RunConfig{Feature: "feat", TaskDesc: "демо", TargetDir: dir, ApproveGates: true})
	if err == nil || first.RunID == "" || len(capture.calls) != 1 {
		t.Fatalf("first run must fail with a resumable run: %+v, %v", first, err)
	}
	writeExecutorScript(t, dir, `cat >/dev/null
printf '{"schema_version":1,"status":"ok","outputs":{"result":{"content":"# Tech\\n\\nok\\n"}}}'
`)
	retry := &captureNotifier{}
	second, err := newExternalPipeline(t, retry).RunWithResult(context.Background(),
		RunConfig{ResumeRunID: first.RunID, TargetDir: dir, ApproveGates: true})
	if err != nil || second.RunID != first.RunID {
		t.Fatalf("retry of the failed external stage: %+v, %v", second, err)
	}
	if len(retry.calls) == 0 {
		t.Fatal("retry produced no stage result")
	}
	last := retry.calls[len(retry.calls)-1]
	if last.Status != "passed" || last.ExecutorInfo == nil || last.ExecutorInfo.Name != "demo" {
		t.Fatalf("retry results = %+v", retry.calls)
	}
	if last.AttemptID == capture.calls[0].AttemptID {
		t.Fatalf("retry must be a fresh attempt, got %s twice", last.AttemptID)
	}
}

func TestRun_ExternalExecutorCancellationStopsProcessAndStage(t *testing.T) {
	dir := env(t)
	started := filepath.Join(dir, "executor.started")
	writeExecutorScript(t, dir, "cat >/dev/null\ntouch "+started+"\nsleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	capture := &captureNotifier{}
	begin := time.Now()
	_, err := newExternalPipeline(t, capture).RunWithResult(ctx,
		RunConfig{Feature: "feat", TaskDesc: "демо", TargetDir: dir, ApproveGates: true})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must surface as context.Canceled: %v", err)
	}
	if time.Since(begin) > 20*time.Second {
		t.Fatal("cancellation must kill the external process instead of waiting for it")
	}
	for _, call := range capture.calls {
		if call.Status == "passed" {
			t.Fatalf("a cancelled stage must not pass: %+v", call)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "tech.md")); statErr == nil {
		t.Fatal("no output may exist after cancellation")
	}
}
