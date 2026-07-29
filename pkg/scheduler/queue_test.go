package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

func testJob(target, runID string) worker.Job {
	return worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
		RunID: runID, TargetDir: target, Feature: "feature", Task: "задача",
	}
}

func TestQueuePersistsAndRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.db")
	target := t.TempDir()
	queue, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(testJob(target, "run-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(testJob(target, "run-1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	record, claimed, err := queue.Claim(context.Background(), "worker-1")
	if err != nil || !claimed || record.Job.RunID != "run-1" {
		t.Fatalf("persistent claim: record=%+v claimed=%v err=%v", record, claimed, err)
	}
}

func TestQueueTargetConcurrencyAndLeaseRecovery(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{
		LeaseDuration: time.Second, MaxConcurrent: 4, PerTarget: 1,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	target := t.TempDir()
	firstID, _ := queue.Enqueue(testJob(target, "run-1"))
	_, _ = queue.Enqueue(testJob(target, "run-2"))
	first, claimed, err := queue.Claim(context.Background(), "worker-1")
	if err != nil || !claimed || first.ID != firstID {
		t.Fatalf("first claim: %+v %v %v", first, claimed, err)
	}
	if _, claimed, err := queue.Claim(context.Background(), "worker-2"); err != nil || claimed {
		t.Fatalf("target lock пропустил второй job: claimed=%v err=%v", claimed, err)
	}
	now = now.Add(2 * time.Second)
	reclaimed, claimed, err := queue.Claim(context.Background(), "worker-2")
	if err != nil || !claimed || reclaimed.ID != firstID || reclaimed.Attempts != 2 {
		t.Fatalf("expired lease не reclaimed: %+v claimed=%v err=%v", reclaimed, claimed, err)
	}
	if err := queue.Complete(first.ID, "worker-1", first.LeaseToken, true, ""); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion принят: %v", err)
	}
}

func TestQueueCancelVisibleOnHeartbeat(t *testing.T) {
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	target := t.TempDir()
	_, _ = queue.Enqueue(testJob(target, "run-cancel"))
	record, _, _ := queue.Claim(context.Background(), "worker-1")
	if affected, err := queue.CancelRun("run-cancel"); err != nil || affected != 1 {
		t.Fatalf("cancel: affected=%d err=%v", affected, err)
	}
	cancelled, err := queue.Renew(context.Background(), record.ID, "worker-1", record.LeaseToken)
	if err != nil || !cancelled {
		t.Fatalf("heartbeat не увидел cancel: cancelled=%v err=%v", cancelled, err)
	}
	if err := queue.Complete(record.ID, "worker-1", record.LeaseToken, false, "canceled"); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := queue.Get(record.ID)
	if stored.Status != StatusCanceled {
		t.Fatalf("cancelled job status=%s", stored.Status)
	}
}

func TestQueueGlobalConcurrencyAtomicAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.db")
	firstQueue, err := Open(path, Options{MaxConcurrent: 1, PerTarget: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer firstQueue.Close()
	secondQueue, err := Open(path, Options{MaxConcurrent: 1, PerTarget: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer secondQueue.Close()
	_, _ = firstQueue.Enqueue(testJob(t.TempDir(), "run-a"))
	_, _ = firstQueue.Enqueue(testJob(t.TempDir(), "run-b"))
	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for index, queue := range []*Queue{firstQueue, secondQueue} {
		group.Add(1)
		go func(owner string, value *Queue) {
			defer group.Done()
			<-start
			_, claimed, claimErr := value.Claim(context.Background(), owner)
			results <- claimed
			errs <- claimErr
		}(string(rune('a'+index)), queue)
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	claimedCount := 0
	for claimed := range results {
		if claimed {
			claimedCount++
		}
	}
	for claimErr := range errs {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	if claimedCount != 1 {
		t.Fatalf("global concurrency claim count=%d", claimedCount)
	}
}

type fakeExecutor struct {
	mu   sync.Mutex
	jobs []worker.Job
	err  error
}

func (f *fakeExecutor) Execute(_ context.Context, job worker.Job) (pipeline.RunResult, error) {
	f.mu.Lock()
	f.jobs = append(f.jobs, job)
	f.mu.Unlock()
	return pipeline.RunResult{RunID: job.RunID}, f.err
}

type cancelAwareExecutor struct {
	started chan struct{}
	mu      sync.Mutex
	jobs    []worker.Job
}

func (e *cancelAwareExecutor) Execute(ctx context.Context, job worker.Job) (pipeline.RunResult, error) {
	e.mu.Lock()
	e.jobs = append(e.jobs, job)
	e.mu.Unlock()
	if job.Operation == worker.OperationStart {
		close(e.started)
		<-ctx.Done()
		return pipeline.RunResult{RunID: job.RunID}, ctx.Err()
	}
	return pipeline.RunResult{RunID: job.RunID}, nil
}

func TestPollerPropagatesDistributedCancel(t *testing.T) {
	target := t.TempDir()
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{
		LeaseDuration: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	jobID, _ := queue.Enqueue(testJob(target, "run-distributed-cancel"))
	engine, _ := NewQueueEngine(queue, target)
	executor := &cancelAwareExecutor{started: make(chan struct{})}
	poller, _ := NewPoller(queue, executor, nil)
	done := make(chan error, 1)
	go func() {
		_, runErr := poller.RunOnce(context.Background(), "worker-1")
		done <- runErr
	}()
	<-executor.started
	if _, err := engine.Cancel(pipeline.CancelConfig{
		RunID: "run-distributed-cancel", TargetDir: target,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poller не передал distributed cancel")
	}
	record, _, _ := queue.Get(jobID)
	if record.Status != StatusCanceled {
		t.Fatalf("cancelled poller status=%s", record.Status)
	}
	if claimed, err := poller.RunOnce(context.Background(), "worker-1"); err != nil || !claimed {
		t.Fatalf("persisted cancel job: claimed=%v err=%v", claimed, err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.jobs) != 2 || executor.jobs[1].Operation != worker.OperationCancel {
		t.Fatalf("persisted cancel job не выполнен: %+v", executor.jobs)
	}
}

func TestQueueEngineAndPoller(t *testing.T) {
	target := t.TempDir()
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	engine, err := NewQueueEngine(queue, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "run-poller", Feature: "feature", TaskDesc: "задача", TargetDir: target,
	}); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	poller, err := NewPoller(queue, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := poller.RunOnce(context.Background(), "worker-1")
	if err != nil || !claimed {
		t.Fatalf("poller: claimed=%v err=%v", claimed, err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.jobs) != 1 || executor.jobs[0].Operation != worker.OperationStart {
		t.Fatalf("poller jobs: %+v", executor.jobs)
	}
}
