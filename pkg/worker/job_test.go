package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

func TestDecodeJobStrictAndExactTarget(t *testing.T) {
	target := filepath.Clean(t.TempDir())
	value := Job{
		SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "run-1",
		TargetDir: target, Feature: "feature", Task: "задача", ExecutionID: strings.Repeat("a", ExecutionIDBytes*2),
	}
	data, _ := json.Marshal(value)
	if _, err := DecodeJob(bytes.NewReader(data), target); err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.TrimSuffix(string(data), "}") + `,"unknown":true}`
	if _, err := DecodeJob(strings.NewReader(withUnknown), target); err == nil {
		t.Fatal("unknown field должен быть отклонён")
	}
	if _, err := DecodeJob(bytes.NewReader(data), t.TempDir()); err == nil {
		t.Fatal("другой mounted target должен быть отклонён")
	}
}

func TestQueuedLegacyJobIsUpgradedOnlyAtProcessSpawn(t *testing.T) {
	target := filepath.Clean(t.TempDir())
	legacy := Job{SchemaVersion: LegacyQueueSchemaVersion, Operation: OperationStart,
		RunID: "legacy-run", TargetDir: target, Feature: "feature", Task: "задача"}
	if err := legacy.ValidateQueued(target); err != nil {
		t.Fatalf("старый durable queue job должен оставаться читаемым: %v", err)
	}
	if err := legacy.Validate(target); err == nil {
		t.Fatal("legacy queue job нельзя запускать как worker invocation без новой identity")
	}
	legacy.SchemaVersion = SchemaVersion
	if err := legacy.ValidateQueued(target); err != nil {
		t.Fatalf("новая queued job schema без process identity должна приниматься: %v", err)
	}
	legacy.ExecutionID = strings.Repeat("a", ExecutionIDBytes*2)
	if err := legacy.ValidateQueued(target); err == nil {
		t.Fatal("durable queue не должен сохранять process-specific execution_id")
	}
}

func TestProcessEnginePassesStrictJob(t *testing.T) {
	target := t.TempDir()
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=TestProcessEngineHelper", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if engine.TargetDir() != filepath.Clean(canonicalTarget) {
		t.Fatalf("scheduler target must match the worker's mounted workspace: got %q, want %q", engine.TargetDir(), filepath.Clean(canonicalTarget))
	}
	result, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "run-1", Feature: "feature", TaskDesc: "задача", TargetDir: target,
	})
	if err != nil || result.RunID != "run-1" {
		t.Fatalf("process worker: result=%+v err=%v", result, err)
	}
}

func TestProcessEngineHonorsContextCancellation(t *testing.T) {
	target := t.TempDir()
	marker := filepath.Join(t.TempDir(), "worker-started")
	t.Setenv("AI_TEAM_WORKER_TEST_MARKER", marker)
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "wait")
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=TestWorkerProtocolHelper", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, runErr := engine.Start(ctx, pipeline.RunConfig{
			RunID: "run-cancel", Feature: "feature", TaskDesc: "задача", TargetDir: target,
		})
		result <- runErr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, readErr := os.Stat(marker); readErr == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("worker process did not start before cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("worker process remained alive after context cancellation")
	}
	if err != context.Canceled {
		t.Fatalf("process context cancellation: %v", err)
	}
}

func TestControlPlaneStartsDisposableWorkerProcess(t *testing.T) {
	target := t.TempDir()
	marker := filepath.Join(t.TempDir(), "worker-job.json")
	t.Setenv("AI_TEAM_WORKER_TEST_MARKER", marker)
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=TestProcessEngineHelper", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := control.New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := controller.Start("cloud-feature", "задача")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, readErr := os.ReadFile(marker)
		if readErr == nil {
			var job Job
			if json.Unmarshal(data, &job) != nil || job.RunID != runID ||
				job.Operation != OperationStart || job.TargetDir != target {
				t.Fatalf("неверный disposable job: %s", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker process не получил job: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcessEngineHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), " worker ") {
		return
	}
	target := ""
	for index := range os.Args {
		if os.Args[index] == "--target" && index+1 < len(os.Args) {
			target = os.Args[index+1]
		}
	}
	job, err := DecodeJob(os.Stdin, target)
	if err != nil {
		t.Fatal(err)
	}
	if marker := os.Getenv("AI_TEAM_WORKER_TEST_MARKER"); marker != "" {
		data, _ := json.Marshal(job)
		if err := writeMarkerAtomically(marker, data); err != nil {
			t.Fatal(err)
		}
	}
	result, encodeErr := json.Marshal(Result{
		SchemaVersion: ResultSchemaVersion, RunID: job.RunID, Operation: job.Operation,
		ExecutionID: job.ExecutionID, Outcome: OutcomeCompleted,
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	fmt.Printf("%s%s\n", ResultPrefix, result)
	os.Exit(0)
}

// writeMarkerAtomically keeps the parent test from observing a partially
// written marker while the helper process is publishing it.
func writeMarkerAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".worker-marker-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
