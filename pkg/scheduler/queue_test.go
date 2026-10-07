package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

func TestQueueLoadsLegacyDurableJobForFreshInvocationUpgrade(t *testing.T) {
	target := filepath.Clean(t.TempDir())
	queue, err := Open(filepath.Join(t.TempDir(), "queue.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	payload, err := json.Marshal(worker.Job{
		SchemaVersion: worker.LegacyQueueSchemaVersion, Operation: worker.OperationStart,
		RunID: "legacy-run", TargetDir: target, Feature: "feature", Task: "persisted task",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	insert, err := queue.db.Exec(`INSERT INTO worker_jobs
		(run_id, operation, target_dir, payload_json, status, created_ms, updated_ms)
		VALUES (?, ?, ?, ?, 'queued', ?, ?)`, "legacy-run", worker.OperationStart,
		target, string(payload), now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := queue.Get(id)
	if err != nil || !exists {
		t.Fatalf("legacy queued row не загрузился: exists=%v err=%v", exists, err)
	}
	if record.Job.SchemaVersion != worker.LegacyQueueSchemaVersion || record.Job.ExecutionID != "" {
		t.Fatalf("legacy logical job должен сохраниться без process identity: %+v", record.Job)
	}
	if err := record.Job.ValidateQueued(target); err != nil {
		t.Fatalf("legacy queued job должен быть передаваем ProcessEngine для reissue: %v", err)
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
	defer func() { _ = queue.Close() }()
	record, claimed, err := queue.Claim(context.Background(), "worker-1")
	if err != nil || !claimed || record.Job.RunID != "run-1" {
		t.Fatalf("persistent claim: record=%+v claimed=%v err=%v", record, claimed, err)
	}
}

func TestEnsureStartJobConvergesAcrossForegroundAndRecoveryRaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.db")
	target := t.TempDir()
	foregroundQueue, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foregroundQueue.Close() }()
	recoveryQueue, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recoveryQueue.Close() }()
	engine, err := NewQueueEngine(foregroundQueue, target)
	if err != nil {
		t.Fatal(err)
	}
	job := testJob(target, "racing-start")

	// Synchronize independent queue handles to exercise the same cross-process
	// race as the foreground controller and startup reconciler.
	const callers = 20
	start := make(chan struct{})
	ids := make(chan int64, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				id, ensureErr := recoveryQueue.EnsureStartJob(job)
				if ensureErr != nil {
					errs <- ensureErr
					return
				}
				ids <- id
				return
			}
			result, startErr := engine.Start(context.Background(), pipeline.RunConfig{
				RunID: job.RunID, TargetDir: target, Feature: job.Feature, TaskDesc: job.Task,
			})
			if startErr != nil {
				errs <- startErr
				return
			}
			ids <- result.QueueJobID
		}(i)
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent admission failed: %v", err)
	}
	var expected int64
	for id := range ids {
		if expected == 0 {
			expected = id
		} else if id != expected {
			t.Fatalf("foreground/recovery returned different queue IDs: got %d, want %d", id, expected)
		}
	}
	if expected == 0 {
		t.Fatal("no queue ID returned")
	}
	jobs, err := recoveryQueue.ListRun(job.RunID)
	if err != nil || len(jobs) != 1 || jobs[0].ID != expected || jobs[0].Status != StatusPending {
		t.Fatalf("race created unexpected durable jobs: jobs=%+v err=%v", jobs, err)
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
	defer func() { _ = queue.Close() }()
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
	if err != nil || !claimed || reclaimed.ID != firstID || reclaimed.Attempts != 2 || reclaimed.Job.Operation != worker.OperationRecover {
		t.Fatalf("expired lease не reclaimed: %+v claimed=%v err=%v", reclaimed, claimed, err)
	}
	if err := queue.Complete(first.ID, "worker-1", first.LeaseToken, true, ""); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion принят: %v", err)
	}
}

func TestExpiredLeaseCannotRenewOrCompleteBeforeReclaim(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{
		LeaseDuration: time.Second,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	jobID, err := queue.Enqueue(testJob(t.TempDir(), "run-expired-lease"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := queue.Claim(context.Background(), "old-worker")
	if err != nil || !ok || claimed.ID != jobID {
		t.Fatalf("claim: record=%+v ok=%v err=%v", claimed, ok, err)
	}
	now = now.Add(time.Second)
	if _, err := queue.Renew(context.Background(), jobID, "old-worker", claimed.LeaseToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease was renewed before reclaim: %v", err)
	}
	if err := queue.Complete(jobID, "old-worker", claimed.LeaseToken, true, "stale publish"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease was completed before reclaim: %v", err)
	}
	stored, exists, err := queue.Get(jobID)
	if err != nil || !exists || stored.Status != StatusRunning {
		t.Fatalf("expired job changed before reclaim: record=%+v exists=%v err=%v", stored, exists, err)
	}
}

func TestClaimForTargetAndKilledWorkerRecovery(t *testing.T) {
	if db := os.Getenv("AI_TEAM_SCHEDULER_CRASH_DB"); db != "" {
		target := os.Getenv("AI_TEAM_SCHEDULER_CRASH_TARGET")
		marker := os.Getenv("AI_TEAM_SCHEDULER_CRASH_MARKER")
		queue, err := Open(db, Options{LeaseDuration: 250 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = queue.Close() }()
		if _, ok, err := queue.ClaimForTarget(context.Background(), "crash-worker", target); err != nil || !ok {
			jobs, _ := queue.ListRun("run-crash")
			t.Fatalf("child claim: target=%q jobs=%+v ok=%v err=%v", target, jobs, ok, err)
		}
		if err := os.WriteFile(marker, []byte("claimed"), 0o600); err != nil {
			t.Fatal(err)
		}
		select {}
	}

	dir := t.TempDir()
	targetA, targetB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.MkdirAll(targetA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetB, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "scheduler.db")
	queue, err := Open(db, Options{LeaseDuration: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	jobID, err := queue.Enqueue(testJob(targetA, "run-crash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.ClaimForTarget(context.Background(), "wrong-target", targetB); err != nil || ok {
		t.Fatalf("worker claimed foreign target: ok=%v err=%v", ok, err)
	}
	marker := filepath.Join(dir, "claimed")
	child := exec.Command(os.Args[0], "-test.run=^TestClaimForTargetAndKilledWorkerRecovery$")
	var childOutput bytes.Buffer
	child.Stdout, child.Stderr = &childOutput, &childOutput
	child.Env = append(os.Environ(), "AI_TEAM_SCHEDULER_CRASH_DB="+db,
		"AI_TEAM_SCHEDULER_CRASH_TARGET="+targetA, "AI_TEAM_SCHEDULER_CRASH_MARKER="+marker)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Process.Kill()
			t.Fatalf("worker subprocess did not claim job: %s", childOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	time.Sleep(300 * time.Millisecond)
	recovered, ok, err := queue.ClaimForTarget(context.Background(), "replacement", targetA)
	if err != nil || !ok || recovered.ID != jobID || recovered.Attempts != 2 || recovered.Job.Operation != worker.OperationRecover {
		t.Fatalf("killed process recovery: record=%+v ok=%v err=%v", recovered, ok, err)
	}
	if err := queue.Complete(jobID, "crash-worker", "stale-token", true, "stale publish"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old worker could publish after lease loss: %v", err)
	}
}

func TestQueueCancelVisibleOnHeartbeat(t *testing.T) {
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
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
	defer func() { _ = firstQueue.Close() }()
	secondQueue, err := Open(path, Options{MaxConcurrent: 1, PerTarget: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondQueue.Close() }()
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
	mu     sync.Mutex
	jobs   []worker.Job
	err    error
	target string
}

func (f *fakeExecutor) TargetDir() string { return f.target }

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
	target  string
}

func (e *cancelAwareExecutor) TargetDir() string { return e.target }

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
	defer func() { _ = queue.Close() }()
	jobID, _ := queue.Enqueue(testJob(target, "run-distributed-cancel"))
	engine, _ := NewQueueEngine(queue, target)
	executor := &cancelAwareExecutor{started: make(chan struct{}), target: target}
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
	defer func() { _ = queue.Close() }()
	engine, err := NewQueueEngine(queue, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), pipeline.RunConfig{
		RunID: "run-poller", Feature: "feature", TaskDesc: "задача", TargetDir: target,
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.ListRun("run-poller")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("queued start job: jobs=%+v err=%v", jobs, err)
	}
	if err := queue.Activate(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{target: target}
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

func TestPollerRequiresFixedWorkerTarget(t *testing.T) {
	target := t.TempDir()
	queue, err := Open(filepath.Join(t.TempDir(), "scheduler.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	if _, err := NewPoller(queue, &fakeExecutor{}, nil); err == nil {
		t.Fatal("poller must reject executors without a fixed target")
	}
	if _, err := NewPoller(queue, &fakeExecutor{target: target}, nil); err != nil {
		t.Fatalf("fixed target executor rejected: %v", err)
	}
}
