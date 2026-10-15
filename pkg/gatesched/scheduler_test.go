package gatesched

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const hashV1 = "1111111111111111111111111111111111111111111111111111111111111111"
const hashV2 = "2222222222222222222222222222222222222222222222222222222222222222"

func def(id string, kind workflow.GateKind) workflow.GateDefinition {
	return workflow.GateDefinition{ID: id, Version: 1, Kind: kind, Title: id}
}

func ref(id string) workflow.GateRef { return workflow.GateRef{Gate: id, Version: 1} }

func defs(items ...workflow.GateDefinition) map[string]workflow.GateDefinition {
	out := map[string]workflow.GateDefinition{}
	for _, d := range items {
		out[d.Ref()] = d
	}
	return out
}

func key(hash string) WaveKey {
	return WaveKey{TaskID: "task-1", Stage: "spec", Visit: 1, Scope: "attempt-1", ArtifactSHA256: hash}
}

// counters records how many times each gate was invoked and when.
type counters struct {
	mu    sync.Mutex
	calls map[string]int
	order []string
}

func newCounters() *counters { return &counters{calls: map[string]int{}} }

func (c *counters) hit(name string) {
	c.mu.Lock()
	c.calls[name]++
	c.order = append(c.order, name)
	c.mu.Unlock()
}

func (c *counters) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func newScheduler(t *testing.T, j Journal, opts Options, runner Runner) *Scheduler {
	t.Helper()
	s, err := New(j, map[workflow.GateKind]Runner{
		workflow.GateKindBuiltin: runner, workflow.GateKindLLM: runner, workflow.GateKindHuman: runner,
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func chainAB_C() (workflow.GateGroups, map[string]workflow.GateDefinition) {
	groups := workflow.GateGroups{{ref("a"), ref("b")}, {ref("c")}}
	return groups, defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin), def("c", workflow.GateKindLLM))
}

// Acceptance: two parallel gates and a dependent third. Both parallel gates
// finish even when one fails, the dependent one does not start on failure, and
// starts exactly once on success.
func TestDependentGateDoesNotStartAfterFailure(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	calls := newCounters()
	bothRunning := make(chan struct{})
	var started int32
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		if in.Ref.Gate == "a" || in.Ref.Gate == "b" {
			// Parallelism proof: neither can finish before the other started.
			if atomic.AddInt32(&started, 1) == 2 {
				close(bothRunning)
			}
			select {
			case <-bothRunning:
			case <-time.After(5 * time.Second):
				return TechnicalError("gates a and b did not run in parallel")
			}
		}
		if in.Ref.Gate == "a" {
			return Fail("раздел отсутствует")
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	result, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if calls.count("a") != 1 || calls.count("b") != 1 {
		t.Fatalf("both parallel gates must run exactly once: a=%d b=%d", calls.count("a"), calls.count("b"))
	}
	if calls.count("c") != 0 {
		t.Fatalf("dependent gate started after a failure")
	}
	if len(result.NotStarted) != 1 || result.NotStarted[0] != "c" || result.StoppedAtGroup != 0 {
		t.Fatalf("not started = %v stopped = %d", result.NotStarted, result.StoppedAtGroup)
	}
	if len(result.Blocking) != 1 || result.Blocking[0].GateName != "a" {
		t.Fatalf("blocking = %+v", result.Blocking)
	}
}

func TestDependentGateStartsExactlyOnceOnSuccess(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		return Pass("ok")
	})
	s := newScheduler(t, journal, Options{}, runner)
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
	result, err := s.Run(context.Background(), wave)
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", result.Status, err)
	}
	if calls.count("c") != 1 {
		t.Fatalf("c invoked %d times", calls.count("c"))
	}
	// c starts only after both a and b are recorded as finished.
	finished := 0
	for _, rec := range journal.Records() {
		switch {
		case rec.Type == RecordFinished && (rec.Attempt.GateName == "a" || rec.Attempt.GateName == "b"):
			finished++
		case rec.Type == RecordStarted && rec.Attempt.GateName == "c" && finished != 2:
			t.Fatalf("c started when only %d of the first group finished", finished)
		}
	}

	// Restart: a fresh scheduler over the same journal repeats nothing, and a
	// redelivered Run is idempotent.
	for i := 0; i < 2; i++ {
		restarted := newScheduler(t, journal, Options{}, runner)
		again, err := restarted.Run(context.Background(), wave)
		if err != nil || again.Status != StatusPassed {
			t.Fatalf("restart %d: status = %s err = %v", i, again.Status, err)
		}
		if len(again.Invoked) != 0 {
			t.Fatalf("restart %d re-invoked %v", i, again.Invoked)
		}
	}
	for _, name := range []string{"a", "b", "c"} {
		if calls.count(name) != 1 {
			t.Fatalf("gate %s invoked %d times across restarts", name, calls.count(name))
		}
	}
}

func TestNewResultVersionRunsEveryGroupAgain(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	if _, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Run(context.Background(), Wave{Key: key(hashV2), Groups: groups, Definitions: definitions})
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", result.Status, err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if calls.count(name) != 2 {
			t.Fatalf("gate %s ran %d times, want 2 (once per version)", name, calls.count(name))
		}
	}
	for _, attempt := range result.Attempts {
		if attempt.ArtifactSHA256 != hashV2 {
			t.Fatalf("attempt %s of the new wave carries hash %s", attempt.AttemptID, attempt.ArtifactSHA256)
		}
	}
}

// A new stage attempt (scope) with identical bytes is a new wave too.
func TestOtherScopeIsNotReused(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome { calls.hit(in.Ref.Gate); return Pass("") })
	s := newScheduler(t, journal, Options{}, runner)
	k := key(hashV1)
	if _, err := s.Run(context.Background(), Wave{Key: k, Groups: groups, Definitions: definitions}); err != nil {
		t.Fatal(err)
	}
	k.Scope = "attempt-2"
	if _, err := s.Run(context.Background(), Wave{Key: k, Groups: groups, Definitions: definitions}); err != nil {
		t.Fatal(err)
	}
	if calls.count("a") != 2 {
		t.Fatalf("a ran %d times, want 2", calls.count("a"))
	}
}

func TestFailureDoesNotCancelStartedSiblings(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), ref("b")}, {ref("c")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin), def("c", workflow.GateKindBuiltin))
	journal := &MemoryJournal{}
	release := make(chan struct{})
	var bFinished atomic.Bool
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		switch in.Ref.Gate {
		case "a":
			return Fail("нет")
		case "b":
			<-release
			bFinished.Store(true)
			return Pass("")
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	done := make(chan Result, 1)
	go func() {
		result, _ := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
		done <- result
	}()
	select {
	case <-done:
		t.Fatal("wave returned before the slow sibling finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	result := <-done
	if !bFinished.Load() {
		t.Fatal("slow sibling was not awaited")
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %s", result.Status)
	}
	for _, attempt := range result.Attempts {
		if attempt.GateName == "b" && attempt.Verdict != workflow.GateVerdictPass {
			t.Fatalf("b must keep its own pass verdict: %+v", attempt)
		}
	}
}

func TestConcurrencyLimit(t *testing.T) {
	var group workflow.GateGroup
	var list []workflow.GateDefinition
	for _, id := range []string{"g1", "g2", "g3", "g4", "g5", "g6"} {
		group = append(group, ref(id))
		list = append(list, def(id, workflow.GateKindBuiltin))
	}
	var running, peak int32
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		now := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if now <= old || atomic.CompareAndSwapInt32(&peak, old, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return Pass("")
	})
	s := newScheduler(t, &MemoryJournal{}, Options{MaxParallel: 2}, runner)
	result, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: workflow.GateGroups{group}, Definitions: defs(list...)})
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", result.Status, err)
	}
	if peak != 2 {
		t.Fatalf("peak concurrency = %d, want exactly the limit 2", peak)
	}
}

func TestInterruptedAttemptIsClosedAndReplaced(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	k := key(hashV1)
	// Pre-crash state: a finished, b started but never finished.
	t0 := time.Now().UTC().Add(-time.Minute)
	a := workflow.GateAttempt{TaskID: k.TaskID, Stage: k.Stage, Visit: 1, ArtifactSHA256: k.ArtifactSHA256, Gate: "a", GateVersion: 1, GateName: "a",
		Mode: workflow.GateModeRequired, AttemptID: AttemptID(k, "a", 1), Executor: workflow.GateKindBuiltin, Group: 0,
		Execution: workflow.GateExecutionSucceeded, Verdict: workflow.GateVerdictPass, StartedAt: t0, FinishedAt: t0.Add(time.Second)}
	b := a
	b.Gate, b.GateName, b.AttemptID = "b", "b", AttemptID(k, "b", 1)
	b.Execution, b.Verdict, b.FinishedAt = workflow.GateExecutionRunning, "", time.Time{}
	_ = journal.Record(Record{Type: RecordStarted, Key: k, Attempt: a})
	_ = journal.Record(Record{Type: RecordFinished, Key: k, Attempt: a})
	_ = journal.Record(Record{Type: RecordStarted, Key: k, Attempt: b})

	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome { calls.hit(in.Ref.Gate); return Pass("") })
	s := newScheduler(t, journal, Options{}, runner)
	result, err := s.Run(context.Background(), Wave{Key: k, Groups: groups, Definitions: definitions})
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", result.Status, err)
	}
	if calls.count("a") != 0 {
		t.Fatal("completed gate a was invoked again")
	}
	if calls.count("b") != 1 || calls.count("c") != 1 {
		t.Fatalf("b=%d c=%d", calls.count("b"), calls.count("c"))
	}
	history, _ := journal.Load(k)
	var interrupted, replacement bool
	for _, attempt := range history {
		if attempt.AttemptID == AttemptID(k, "b", 1) && attempt.Execution == workflow.GateExecutionCanceled && !attempt.FinishedAt.IsZero() {
			interrupted = true
		}
		if attempt.AttemptID == AttemptID(k, "b", 2) && attempt.Execution == workflow.GateExecutionSucceeded {
			replacement = true
		}
	}
	if !interrupted || !replacement {
		t.Fatalf("history must keep the interrupted attempt and the new one: %+v", history)
	}
}

func TestTechnicalFailureIsNotRetriedImplicitly(t *testing.T) {
	groups := workflow.GateGroups{{ref("a")}}
	definitions := defs(def("a", workflow.GateKindBuiltin))
	journal := &MemoryJournal{}
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit("a")
		if calls.count("a") == 1 {
			return Outcome{Execution: workflow.GateExecutionTimeout, Reason: "таймаут"}
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
	first, err := s.Run(context.Background(), wave)
	if err != nil || first.Status != StatusError {
		t.Fatalf("status = %s err = %v", first.Status, err)
	}
	if first.Blocking[0].Execution != workflow.GateExecutionTimeout || first.Blocking[0].Verdict != "" {
		t.Fatalf("a timeout must stay a timeout without a verdict: %+v", first.Blocking[0])
	}
	again, _ := s.Run(context.Background(), wave)
	if calls.count("a") != 1 || again.Status != StatusError {
		t.Fatalf("recorded technical failure must not be retried implicitly (calls=%d status=%s)", calls.count("a"), again.Status)
	}
	wave.Retry = map[string]bool{"a": true}
	retried, err := s.Run(context.Background(), wave)
	if err != nil || retried.Status != StatusPassed || calls.count("a") != 2 {
		t.Fatalf("explicit retry: status=%s calls=%d err=%v", retried.Status, calls.count("a"), err)
	}
	history, _ := journal.Load(wave.Key)
	if len(history) != 2 || history[0].Execution != workflow.GateExecutionTimeout {
		t.Fatalf("retry must keep the earlier attempt: %+v", history)
	}
}

func TestPendingGateWaitsAndResumesWithoutDuplicateAttempt(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), ref("h")}, {ref("c")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("h", workflow.GateKindHuman), def("c", workflow.GateKindBuiltin))
	journal := &MemoryJournal{}
	calls := newCounters()
	decided := false
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		if in.Ref.Gate == "h" {
			if !decided {
				return Waiting("ждёт решения PO")
			}
			if !in.Resumed {
				t.Error("pending attempt must be resumed, not restarted")
			}
			return Pass("approved")
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
	first, err := s.Run(context.Background(), wave)
	if err != nil || first.Status != StatusWaiting {
		t.Fatalf("status = %s err = %v", first.Status, err)
	}
	if calls.count("c") != 0 {
		t.Fatal("the group after a waiting gate started")
	}
	decided = true
	second, err := s.Run(context.Background(), wave)
	if err != nil || second.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", second.Status, err)
	}
	if calls.count("a") != 1 || calls.count("h") != 2 || calls.count("c") != 1 {
		t.Fatalf("a=%d h=%d c=%d", calls.count("a"), calls.count("h"), calls.count("c"))
	}
	history, _ := journal.Load(wave.Key)
	attemptsOfH := 0
	for _, attempt := range history {
		if attempt.GateName == "h" {
			attemptsOfH++
		}
	}
	if attemptsOfH != 1 {
		t.Fatalf("the human gate must keep one attempt across the wait, got %d", attemptsOfH)
	}
}

func TestResultChangedDuringWaveAbortsIt(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	freezer := &fakeFreezer{}
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		if in.Ref.Gate == "a" {
			freezer.changed.Store(true) // someone edits the result while a gate runs
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{Freezer: freezer}, runner)
	_, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
	if !errors.Is(err, ErrResultChanged) {
		t.Fatalf("err = %v, want ErrResultChanged", err)
	}
	for _, rec := range journal.Records() {
		if rec.Type == RecordStarted && rec.Attempt.GateName == "c" {
			t.Fatal("the next group started after the result changed")
		}
	}
	if !freezer.released.Load() {
		t.Fatal("lease not released")
	}
}

type fakeFreezer struct{ changed, released atomic.Bool }

func (f *fakeFreezer) Freeze(context.Context, WaveKey) (Lease, error) { return fakeLease{f}, nil }

type fakeLease struct{ f *fakeFreezer }

func (l fakeLease) Verify() error {
	if l.f.changed.Load() {
		return ErrResultChanged
	}
	return nil
}
func (l fakeLease) Release() { l.f.released.Store(true) }

func TestHistoryHasTimesAndOutcomes(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{}
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		time.Sleep(5 * time.Millisecond)
		if in.Ref.Gate == "b" {
			return Fail("b не прошёл")
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	result, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range result.Attempts {
		if err := attempt.Validate(); err != nil {
			t.Fatalf("invalid attempt: %v", err)
		}
		if attempt.StartedAt.IsZero() || attempt.FinishedAt.Before(attempt.StartedAt) {
			t.Fatalf("times: %+v", attempt)
		}
		if attempt.Executor == "" || attempt.Group != 0 {
			t.Fatalf("executor/group: %+v", attempt)
		}
	}
}

func TestAdvisoryFailureWarnsButDoesNotBlock(t *testing.T) {
	adv := ref("b")
	adv.Mode = workflow.GateModeAdvisory
	groups := workflow.GateGroups{{ref("a"), adv}, {ref("c")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin), def("c", workflow.GateKindBuiltin))
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		if in.Ref.Gate == "b" {
			return Fail("рекомендация")
		}
		return Pass("")
	})
	s := newScheduler(t, &MemoryJournal{}, Options{}, runner)
	result, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", result.Status, err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].GateName != "b" || len(result.NotStarted) != 0 {
		t.Fatalf("warnings=%+v notStarted=%v", result.Warnings, result.NotStarted)
	}
}

func TestInvalidOutcomesBecomeTechnicalErrors(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), ref("b")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin))
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		if in.Ref.Gate == "a" {
			return Outcome{Execution: "bogus"}
		}
		return Outcome{Execution: workflow.GateExecutionSucceeded} // no verdict
	})
	s := newScheduler(t, &MemoryJournal{}, Options{}, runner)
	result, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
	if err != nil || result.Status != StatusError || len(result.Blocking) != 2 {
		t.Fatalf("status = %s err = %v blocking=%d", result.Status, err, len(result.Blocking))
	}
	for _, attempt := range result.Blocking {
		if attempt.Execution != workflow.GateExecutionError || attempt.Verdict != "" {
			t.Fatalf("must be a technical error without verdict: %+v", attempt)
		}
	}
}

// A panic is a crash of the controller, not a verdict: it surfaces in the
// caller after the sibling finished, leaves the attempt open in the journal,
// and the next run reports it as interrupted and runs a new attempt.
func TestPanicIsACrashAndRecoversAsInterrupted(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), ref("b")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin))
	journal := &MemoryJournal{}
	crash := true
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		if in.Ref.Gate == "a" && crash {
			panic("boom")
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic of a gate must reach the caller")
			}
		}()
		_, _ = s.Run(context.Background(), wave)
	}()
	history, _ := journal.Load(wave.Key)
	byName := map[string]workflow.GateAttempt{}
	for _, attempt := range history {
		byName[attempt.GateName] = attempt
	}
	if byName["a"].Execution != workflow.GateExecutionRunning || byName["b"].Execution != workflow.GateExecutionSucceeded {
		t.Fatalf("sibling must be awaited and the crashed attempt left open: %+v", byName)
	}
	crash = false
	result, err := s.Run(context.Background(), wave)
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("status = %s err = %v", result.Status, err)
	}
	if calls.count("b") != 1 || calls.count("a") != 2 {
		t.Fatalf("a=%d b=%d", calls.count("a"), calls.count("b"))
	}
	history, _ = journal.Load(wave.Key)
	interrupted := 0
	for _, attempt := range history {
		if attempt.GateName == "a" && attempt.Execution == workflow.GateExecutionCanceled {
			interrupted++
		}
	}
	if interrupted != 1 {
		t.Fatalf("the crashed attempt must be closed as canceled: %+v", history)
	}
}

func TestJournalFailureIsFailClosed(t *testing.T) {
	groups, definitions := chainAB_C()
	journal := &MemoryJournal{FailOn: RecordStarted}
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome { calls.hit(in.Ref.Gate); return Pass("") })
	s := newScheduler(t, journal, Options{}, runner)
	if _, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}); err == nil {
		t.Fatal("a journal failure must fail the wave")
	}
	if calls.count("a")+calls.count("b")+calls.count("c") != 0 {
		t.Fatal("a gate ran although its start could not be recorded")
	}
}

func TestInvalidWavesAreRejected(t *testing.T) {
	groups, definitions := chainAB_C()
	s := newScheduler(t, &MemoryJournal{}, Options{}, RunnerFunc(func(context.Context, Invocation) Outcome { return Pass("") }))
	bad := key(hashV1)
	bad.ArtifactSHA256 = ""
	if _, err := s.Run(context.Background(), Wave{Key: bad, Groups: groups, Definitions: definitions}); err == nil {
		t.Fatal("empty hash accepted")
	}
	if _, err := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: map[string]workflow.GateDefinition{}}); err == nil {
		t.Fatal("unpinned definition accepted")
	}
	if _, err := s.Run(context.Background(), Wave{Key: key(hashV1), Definitions: definitions}); err == nil {
		t.Fatal("empty chain accepted")
	}
	noRunner, _ := New(&MemoryJournal{}, map[workflow.GateKind]Runner{}, Options{})
	if _, err := noRunner.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}); err == nil {
		t.Fatal("missing runner accepted")
	}
	if _, err := New(&MemoryJournal{}, nil, Options{MaxParallel: 1000}); err == nil {
		t.Fatal("MaxParallel above the limit accepted")
	}
}

// The evidence-backed journal survives a "process restart": a new journal over
// the same event log sees the same attempts and repeats nothing, and a
// different hash sees none of them.
func TestEventJournalRoundTripAndRestart(t *testing.T) {
	log := &fakeLog{}
	sink := EventSink{Read: log.read, Append: log.append}
	groups, definitions := chainAB_C()
	calls := newCounters()
	runner := RunnerFunc(func(ctx context.Context, in Invocation) Outcome { calls.hit(in.Ref.Gate); return Pass("ok") })
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
	if _, err := newScheduler(t, NewEventJournal(sink), Options{}, runner).Run(context.Background(), wave); err != nil {
		t.Fatal(err)
	}
	result, err := newScheduler(t, NewEventJournal(sink), Options{}, runner).Run(context.Background(), wave)
	if err != nil || result.Status != StatusPassed || len(result.Invoked) != 0 {
		t.Fatalf("restart: status=%s invoked=%v err=%v", result.Status, result.Invoked, err)
	}
	if calls.count("a")+calls.count("b")+calls.count("c") != 3 {
		t.Fatalf("calls = %v", calls.calls)
	}
	other, err := FoldAttempts(log.events, key(hashV2))
	if err != nil || len(other) != 0 {
		t.Fatalf("another hash must see no attempts: %v %v", other, err)
	}
	all, err := AllAttempts(log.events, "spec")
	if err != nil || len(all) != 3 {
		t.Fatalf("history = %d attempts, err %v", len(all), err)
	}
	for _, attempt := range all {
		if attempt.Execution != workflow.GateExecutionSucceeded || attempt.FinishedAt.IsZero() {
			t.Fatalf("history entry without outcome/time: %+v", attempt)
		}
	}
}

type fakeLog struct {
	mu     sync.Mutex
	events []evidence.Event
}

func (l *fakeLog) read() ([]evidence.Event, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]evidence.Event(nil), l.events...), len(l.events) > 0, nil
}

func (l *fakeLog) append(event evidence.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Mirror the real store: data is normalized through JSON.
	event.Sequence = uint64(len(l.events) + 1)
	l.events = append(l.events, event)
	return nil
}

func TestFoldIgnoresRedeliveredOlderRecordAfterTerminal(t *testing.T) {
	k := key(hashV1)
	attempt := workflow.GateAttempt{TaskID: k.TaskID, Stage: k.Stage, Visit: 1, ArtifactSHA256: k.ArtifactSHA256, Gate: "a", GateVersion: 1, GateName: "a",
		Mode: workflow.GateModeRequired, AttemptID: AttemptID(k, "a", 1), Executor: workflow.GateKindBuiltin,
		Execution: workflow.GateExecutionRunning, StartedAt: time.Now().UTC()}
	finished := attempt
	finished.Execution, finished.Verdict, finished.FinishedAt = workflow.GateExecutionSucceeded, workflow.GateVerdictPass, time.Now().UTC()
	log := &fakeLog{}
	j := NewEventJournal(EventSink{Read: log.read, Append: log.append})
	_ = j.Record(Record{Type: RecordStarted, Key: k, Attempt: attempt})
	_ = j.Record(Record{Type: RecordFinished, Key: k, Attempt: finished})
	_ = j.Record(Record{Type: RecordStarted, Key: k, Attempt: attempt}) // redelivery
	got, err := j.Load(k)
	if err != nil || len(got) != 1 || got[0].Execution != workflow.GateExecutionSucceeded {
		t.Fatalf("got %+v err %v", got, err)
	}
}
