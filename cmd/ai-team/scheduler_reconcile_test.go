package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/scheduler"
	web "github.com/arturpanteleev/ai-team/pkg/web"
	webstore "github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

func TestReconcileSchedulerQueueOnceRecoversAdmissionsAndProjectsWorkerState(t *testing.T) {
	target := t.TempDir()
	databasePath := filepath.Join(t.TempDir(), "web.db")
	queue, err := scheduler.Open(filepath.Join(t.TempDir(), "scheduler.db"), scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	srv, err := web.NewServer(databasePath, "", filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	store, err := webstore.New(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jobs := map[string]worker.Job{}
	for _, runID := range []string{"before-enqueue", "after-enqueue", "linked-pending", "already-running"} {
		job := worker.Job{
			SchemaVersion: worker.SchemaVersion,
			Operation:     worker.OperationStart,
			RunID:         runID,
			TargetDir:     target,
			Feature:       "feature",
			Task:          "recover durable cloud admission",
		}
		jobs[runID] = job
		snapshot, err := json.Marshal(struct {
			Feature string `json:"feature"`
			Task    string `json:"task"`
		}{Feature: job.Feature, Task: job.Task})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AdmitPipelineRun(&webstore.PipelineRun{
			RunID: runID, Feature: job.Feature, Status: "queued", StartedAt: time.Now().UTC(), ConfigSnapshot: string(snapshot),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Crash before queue persistence: the recovery pass must create the job.
	// Crash after persistence but before projection: it must reuse this pending
	// job, which remains unclaimable until the pass links its ID to the run.
	existingID, err := queue.EnsureStartJob(jobs["after-enqueue"])
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := queue.Claim(context.Background(), "before-reconcile"); err != nil || claimed {
		t.Fatalf("unlinked pending job should not be claimable: claimed=%v err=%v", claimed, err)
	}

	// Crash after projection but before activation: the correlated pending job
	// must be activated by the same reconciliation pass.
	linkedID, err := queue.EnqueuePending(jobs["linked-pending"])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunQueued("linked-pending", linkedID); err != nil {
		t.Fatal(err)
	}

	// A worker may already have claimed another correlated queue job when the
	// web process restarts. Reconciliation must project running state early.
	runningID, err := queue.Enqueue(jobs["already-running"])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunQueued("already-running", runningID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := queue.Claim(context.Background(), "reconcile-test-worker")
	if err != nil || !ok || claimed.ID != runningID {
		t.Fatalf("claim existing job: job=%+v claimed=%v err=%v", claimed, ok, err)
	}

	reconcileSchedulerQueueOnce(store, queue, srv, target)

	for runID, expectedStatus := range map[string]string{
		"before-enqueue": "queued", "after-enqueue": "queued",
		"linked-pending": "queued", "already-running": "running",
	} {
		projected, err := store.GetPipelineRunByRunID(runID)
		if err != nil || projected.Status != expectedStatus || projected.QueueJobID <= 0 {
			t.Fatalf("run %s not reconciled: run=%+v err=%v", runID, projected, err)
		}
		queueJobs, err := queue.ListRun(runID)
		if err != nil || len(queueJobs) != 1 || queueJobs[0].Status != scheduler.Status(expectedStatusForQueue(runID)) {
			t.Fatalf("run %s queue state: jobs=%+v err=%v", runID, queueJobs, err)
		}
		if runID == "after-enqueue" && queueJobs[0].ID != existingID {
			t.Fatalf("recovery duplicated the existing pending job: got %d want %d", queueJobs[0].ID, existingID)
		}
	}
}

func expectedStatusForQueue(runID string) string {
	if runID == "already-running" {
		return "running"
	}
	return "queued"
}
