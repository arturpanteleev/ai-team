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
		TargetDir: target, Feature: "feature", Task: "задача",
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

func TestProcessEnginePassesStrictJob(t *testing.T) {
	target := t.TempDir()
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=TestProcessEngineHelper", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"),
	)
	if err != nil {
		t.Fatal(err)
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
	engine, err := NewProcessEngine(
		[]string{os.Args[0], "-test.run=TestProcessEngineHelper", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Start(ctx, pipeline.RunConfig{
		RunID: "run-cancel", Feature: "feature", TaskDesc: "задача", TargetDir: target,
	})
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
		if err := os.WriteFile(marker, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, encodeErr := json.Marshal(Result{
		SchemaVersion: ResultSchemaVersion, RunID: job.RunID, Outcome: OutcomeCompleted,
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	fmt.Printf("%s%s\n", ResultPrefix, result)
	os.Exit(0)
}
