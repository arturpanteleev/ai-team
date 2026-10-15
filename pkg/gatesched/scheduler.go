// Package gatesched executes the exit gate chain of one stage result.
//
// The chain (workflow.GateGroups) is run sequentially group by group; the
// gates inside a group run in parallel with a configurable concurrency limit.
// Every attempt and every transition is written to a Journal before and after
// the gate runs, so that after a restart the scheduler does not repeat a
// completed invocation and honestly accounts for an interrupted one.
//
// The scheduler knows nothing about stages, agents or evidence files: gate
// kinds are executed by Runner implementations supplied by the caller, and
// persistence goes through the Journal port (see EventJournal for the
// evidence-backed implementation).
//
// Invariants:
//
//   - A wave is bound to one result version (ArtifactSHA256) and one scope
//     (the stage attempt that produced it). Attempts recorded for any other
//     hash or scope are never read, so a success of an old version cannot
//     satisfy a new one: a new version runs every group again from the first.
//   - The hash is frozen for the whole wave: the Freezer lease is verified
//     before the wave, after every gate and at the end. A change aborts the
//     wave with ErrResultChanged and nothing from that wave is trusted.
//   - After a required gate fails the following groups never start, but every
//     gate of the group that is already running is awaited and recorded.
//   - A gate whose latest attempt already succeeded (or ended in a technical
//     failure that nobody asked to retry) is not invoked again. An attempt
//     recorded as running without a terminal record is closed as canceled
//     ("interrupted") and replaced by a new attempt; the old one is kept.
package gatesched

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// DefaultMaxParallel is the concurrency limit used when Options.MaxParallel
// is not set.
const DefaultMaxParallel = 4

// MaxParallelLimit is the largest accepted concurrency limit.
const MaxParallelLimit = 64

// ErrResultChanged reports that the frozen result version was modified while
// the wave ran. The wave is void: no outcome of it may be used.
var ErrResultChanged = errors.New("gatesched: результат изменён во время проверки")

// Status is the aggregate state of a wave.
type Status string

const (
	// StatusPassed: every required gate passed (or was not applicable).
	StatusPassed Status = "passed"
	// StatusFailed: at least one required gate returned verdict fail.
	StatusFailed Status = "failed"
	// StatusError: no required fail, but at least one required gate ended in a
	// technical failure (error, timeout or canceled).
	StatusError Status = "error"
	// StatusWaiting: nothing failed but at least one gate waits for an
	// external decision (a human, a clarification round).
	StatusWaiting Status = "waiting"
)

// WaveKey identifies the result version a wave checks.
type WaveKey struct {
	TaskID string
	Stage  string
	// Visit is the 1-based visit of the stage.
	Visit int
	// Scope is the opaque identity of the stage attempt that produced the
	// result. Recorded attempts of another scope are ignored.
	Scope string
	// ArtifactSHA256 is the hash of the frozen result version.
	ArtifactSHA256 string
}

// Wave is one execution of a stage's exit chain over one result version.
type Wave struct {
	Key    WaveKey
	Groups workflow.GateGroups
	// Definitions maps GateRef.Key() ("id@version") to the pinned definition.
	Definitions map[string]workflow.GateDefinition
	// Retry names the gate instances whose latest technical failure
	// (error/timeout/canceled) on this very hash must be run again. The
	// earlier attempt stays in the journal. Empty by default: a recorded
	// technical failure is not retried implicitly.
	Retry map[string]bool
}

// Invocation is what a Runner receives for one gate attempt.
type Invocation struct {
	// Attempt is the attempt being executed (Execution running, or pending when
	// Resumed). The runner must not modify it.
	Attempt workflow.GateAttempt
	Ref     workflow.GateRef
	Def     workflow.GateDefinition
	// Resumed is true when the attempt was left pending by an earlier run
	// (waiting for an external decision) and is being continued.
	Resumed bool
}

// Outcome is the result of one invocation.
//
// Execution succeeded needs a Verdict. Execution pending means the gate is
// waiting for an external decision: the attempt stays open and the wave stops
// with StatusWaiting once the rest of the group has finished.
type Outcome struct {
	Execution workflow.GateExecution
	Verdict   workflow.GateVerdict
	Reason    string
	Evidence  []workflow.GateEvidenceRef
}

// Pass is a convenience constructor for a passed outcome.
func Pass(reason string) Outcome {
	return Outcome{Execution: workflow.GateExecutionSucceeded, Verdict: workflow.GateVerdictPass, Reason: reason}
}

// Fail is a convenience constructor for a negative verdict.
func Fail(reason string) Outcome {
	return Outcome{Execution: workflow.GateExecutionSucceeded, Verdict: workflow.GateVerdictFail, Reason: reason}
}

// TechnicalError is a convenience constructor for an execution error.
func TechnicalError(reason string) Outcome {
	return Outcome{Execution: workflow.GateExecutionError, Reason: reason}
}

// Waiting is a convenience constructor for a gate that awaits a decision.
func Waiting(reason string) Outcome {
	return Outcome{Execution: workflow.GateExecutionPending, Reason: reason}
}

// Runner executes one gate attempt. It must honor ctx cancellation and must
// not retry internally in a way that hides the technical failure.
type Runner interface {
	Run(ctx context.Context, in Invocation) Outcome
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, in Invocation) Outcome

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, in Invocation) Outcome { return f(ctx, in) }

// RecordType is the kind of journal record.
type RecordType string

const (
	// RecordStarted is written before a gate is invoked (Execution running).
	RecordStarted RecordType = "started"
	// RecordWaiting is written when the gate waits for a decision (pending).
	RecordWaiting RecordType = "waiting"
	// RecordFinished is written when the attempt reached a terminal state.
	RecordFinished RecordType = "finished"
	// RecordAborted closes a wave whose frozen result changed. It carries no
	// attempt.
	RecordAborted RecordType = "aborted"
)

// Record is one durable journal entry.
type Record struct {
	Type    RecordType
	Key     WaveKey
	Attempt workflow.GateAttempt
	// Note carries the reason of RecordAborted.
	Note string
}

// Journal is the durable port of the scheduler.
type Journal interface {
	// Load returns the latest known state of every attempt recorded for the
	// wave key, ordered by first appearance. Attempts of other keys are not
	// returned.
	Load(key WaveKey) ([]workflow.GateAttempt, error)
	// Record appends a record. It must be safe for concurrent use. A record
	// that is not durable must return an error.
	Record(rec Record) error
}

// Lease is a held freeze of one result version.
type Lease interface {
	// Verify returns ErrResultChanged (possibly wrapped) when the frozen result
	// no longer has the hash of the wave.
	Verify() error
	// Release ends the freeze.
	Release()
}

// Freezer pins the result version for the duration of a wave.
type Freezer interface {
	Freeze(ctx context.Context, key WaveKey) (Lease, error)
}

// Options configure a Scheduler.
type Options struct {
	// MaxParallel limits concurrently running gates of one group
	// (default DefaultMaxParallel).
	MaxParallel int
	// Clock returns the current time (default time.Now().UTC()).
	Clock func() time.Time
	// Freezer guards the result hash; nil disables the guard.
	Freezer Freezer
}

// Scheduler runs waves. A Scheduler is safe for concurrent use by different
// waves; two concurrent runs of the same wave key are not supported.
type Scheduler struct {
	runners map[workflow.GateKind]Runner
	journal Journal
	opts    Options
}

// New creates a scheduler. runners maps a gate kind to its executor.
func New(journal Journal, runners map[workflow.GateKind]Runner, opts Options) (*Scheduler, error) {
	if journal == nil {
		return nil, errors.New("gatesched: журнал обязателен")
	}
	if opts.MaxParallel == 0 {
		opts.MaxParallel = DefaultMaxParallel
	}
	if opts.MaxParallel < 1 || opts.MaxParallel > MaxParallelLimit {
		return nil, fmt.Errorf("gatesched: MaxParallel %d вне диапазона 1..%d", opts.MaxParallel, MaxParallelLimit)
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	copied := make(map[workflow.GateKind]Runner, len(runners))
	for kind, runner := range runners {
		copied[kind] = runner
	}
	return &Scheduler{runners: copied, journal: journal, opts: opts}, nil
}

// Result is the outcome of a wave.
type Result struct {
	Status Status
	// Attempts holds the effective (latest) attempt of every gate that has one,
	// in chain order.
	Attempts []workflow.GateAttempt
	// Blocking are the effective attempts that prevent the transition.
	Blocking []workflow.GateAttempt
	// Warnings are advisory gates that did not pass; they never block.
	Warnings []workflow.GateAttempt
	// NotStarted lists the instance names that did not start because an
	// earlier group blocked.
	NotStarted []string
	// Invoked lists the attempt IDs actually executed by this call (reused
	// attempts are not listed).
	Invoked []string
	// StoppedAtGroup is the 0-based index of the group that stopped the chain,
	// or -1 when every group completed.
	StoppedAtGroup int
}

type slot struct {
	ref      workflow.GateRef
	def      workflow.GateDefinition
	group    int
	attempts []workflow.GateAttempt // all attempts of this gate on this key
}

func (s *slot) latest() (workflow.GateAttempt, bool) {
	if len(s.attempts) == 0 {
		return workflow.GateAttempt{}, false
	}
	return s.attempts[len(s.attempts)-1], true
}

// Run executes the wave. It returns a non-nil error only when the wave could
// not be carried out reliably (journal failure, invalid wave, changed result,
// canceled context before anything ran); gate failures are in Result.
func (s *Scheduler) Run(ctx context.Context, wave Wave) (Result, error) {
	slots, err := s.plan(wave)
	if err != nil {
		return Result{}, err
	}
	var lease Lease
	if s.opts.Freezer != nil {
		lease, err = s.opts.Freezer.Freeze(ctx, wave.Key)
		if err != nil {
			return Result{}, fmt.Errorf("gatesched: фиксация результата: %w", err)
		}
		defer lease.Release()
	}
	verify := func() error {
		if lease == nil {
			return nil
		}
		return lease.Verify()
	}
	abort := func(cause error) (Result, error) {
		_ = s.journal.Record(Record{Type: RecordAborted, Key: wave.Key, Note: cause.Error()})
		return Result{}, cause
	}
	if err := verify(); err != nil {
		return abort(err)
	}

	recorded, err := s.journal.Load(wave.Key)
	if err != nil {
		return Result{}, fmt.Errorf("gatesched: чтение журнала: %w", err)
	}
	byName := map[string][]workflow.GateAttempt{}
	for _, attempt := range recorded {
		byName[attempt.GateName] = append(byName[attempt.GateName], attempt)
	}
	for _, sl := range slots {
		sl.attempts = byName[sl.ref.InstanceName()]
	}
	// Recovery: a started attempt without a terminal record was interrupted.
	// Close it as canceled so the history is honest, then run a new attempt.
	for _, sl := range slots {
		for i, attempt := range sl.attempts {
			if attempt.Execution != workflow.GateExecutionRunning {
				continue
			}
			attempt.Execution = workflow.GateExecutionCanceled
			attempt.Verdict = ""
			attempt.Reason = "прервана перезапуском контроллера; исход неизвестен, будет запущена новая попытка"
			attempt.FinishedAt = s.opts.Clock()
			if err := s.journal.Record(Record{Type: RecordFinished, Key: wave.Key, Attempt: attempt}); err != nil {
				return Result{}, fmt.Errorf("gatesched: закрытие прерванной попытки %s: %w", attempt.AttemptID, err)
			}
			sl.attempts[i] = attempt
		}
	}

	result := Result{StoppedAtGroup: -1}
	groupCount := len(wave.Groups)
	for g := 0; g < groupCount; g++ {
		if err := ctx.Err(); err != nil {
			return s.finish(slots, result, fmt.Errorf("gatesched: %w", err))
		}
		var runnable []*slot
		for _, sl := range slots {
			if sl.group != g {
				continue
			}
			if s.needsRun(sl, wave) {
				runnable = append(runnable, sl)
			}
		}
		invoked, err := s.runGroup(ctx, wave, runnable, verify)
		result.Invoked = append(result.Invoked, invoked...)
		if err != nil {
			if errors.Is(err, ErrResultChanged) {
				return abort(err)
			}
			return Result{}, err
		}
		if err := verify(); err != nil {
			return abort(err)
		}
		if s.groupBlocks(slots, g) {
			result.StoppedAtGroup = g
			break
		}
	}
	return s.finish(slots, result, nil)
}

// plan validates the wave and builds one slot per gate reference.
func (s *Scheduler) plan(wave Wave) (slotList, error) {
	key := wave.Key
	switch {
	case key.TaskID == "" || key.Stage == "" || key.Visit <= 0 || key.Scope == "":
		return nil, errors.New("gatesched: ключ волны требует task, stage, visit>0 и scope")
	case key.ArtifactSHA256 == "":
		return nil, errors.New("gatesched: хеш результата обязателен")
	case len(wave.Groups) == 0:
		return nil, errors.New("gatesched: цепочка гейтов пуста")
	}
	if errs := wave.Groups.ValidateShape(); len(errs) > 0 {
		return nil, fmt.Errorf("gatesched: цепочка гейтов некорректна: %w", errors.Join(errs...))
	}
	var slots slotList
	for g, group := range wave.Groups {
		for _, ref := range group {
			def, ok := wave.Definitions[ref.Key()]
			if !ok {
				return nil, fmt.Errorf("gatesched: определение %s не закреплено для волны", ref.Key())
			}
			if _, ok := s.runners[def.Kind]; !ok {
				return nil, fmt.Errorf("gatesched: нет исполнителя для вида %q (гейт %s)", def.Kind, ref.Key())
			}
			slots = append(slots, &slot{ref: ref, def: def, group: g})
		}
	}
	return slots, nil
}

type slotList []*slot

// needsRun decides from the recorded history whether the gate is invoked now.
func (s *Scheduler) needsRun(sl *slot, wave Wave) bool {
	latest, ok := sl.latest()
	if !ok {
		return true
	}
	switch latest.Execution {
	case workflow.GateExecutionSucceeded:
		return false
	case workflow.GateExecutionPending:
		return true // resume the open attempt
	case workflow.GateExecutionError, workflow.GateExecutionTimeout:
		return wave.Retry[sl.ref.InstanceName()]
	case workflow.GateExecutionCanceled:
		// Canceled means "no result was obtained" (interruption, context
		// cancellation): it is never a conclusion, so run again.
		return true
	}
	return true
}

// runGroup runs the given gates in parallel with the concurrency limit and
// waits for all of them.
func (s *Scheduler) runGroup(ctx context.Context, wave Wave, gates []*slot, verify func() error) ([]string, error) {
	if len(gates) == 0 {
		return nil, nil
	}
	sem := make(chan struct{}, s.opts.MaxParallel)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		invoked   []string
		fatal     error
		panicked  bool
		panicking any
	)
	setFatal := func(err error) {
		mu.Lock()
		if fatal == nil {
			fatal = err
		}
		mu.Unlock()
	}
	for _, sl := range gates {
		sl := sl
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic of a gate is a crash of the controller, exactly as when
			// the gate ran inline: it is carried to the caller's goroutine and
			// re-raised once the rest of the group settled. The attempt stays
			// "running" in the journal, which the next run reports honestly as
			// interrupted. Turning it into an error would hide a bug.
			defer func() {
				if recovered := recover(); recovered != nil {
					mu.Lock()
					if !panicked {
						panicked, panicking = true, recovered
					}
					mu.Unlock()
				}
			}()
			id, err := s.runOne(ctx, wave, sl)
			if id != "" {
				mu.Lock()
				invoked = append(invoked, id)
				mu.Unlock()
			}
			if err != nil {
				setFatal(err)
				return
			}
			if err := verify(); err != nil {
				setFatal(err)
			}
		}()
	}
	wg.Wait()
	if panicked {
		panic(panicking)
	}
	sort.Strings(invoked)
	return invoked, fatal
}

// runOne performs one attempt: record start, invoke, record the terminal or
// waiting state. The returned ID is non-empty when the runner was invoked.
func (s *Scheduler) runOne(ctx context.Context, wave Wave, sl *slot) (string, error) {
	latest, has := sl.latest()
	resumed := has && latest.Execution == workflow.GateExecutionPending
	var attempt workflow.GateAttempt
	if resumed {
		attempt = latest
	} else {
		attempt = s.newAttempt(wave, sl)
		if err := s.journal.Record(Record{Type: RecordStarted, Key: wave.Key, Attempt: attempt}); err != nil {
			return "", fmt.Errorf("gatesched: запись старта попытки %s: %w", attempt.AttemptID, err)
		}
	}
	in := Invocation{Attempt: attempt, Ref: sl.ref, Def: sl.def, Resumed: resumed}
	outcome := s.invoke(ctx, in)
	outcome = normalize(outcome)
	finished := attempt
	finished.Reason = outcome.Reason
	finished.Evidence = outcome.Evidence
	if outcome.Execution == workflow.GateExecutionPending {
		finished.Execution = workflow.GateExecutionPending
		finished.Verdict = ""
		if !(resumed && latest.Reason == finished.Reason) {
			if err := s.journal.Record(Record{Type: RecordWaiting, Key: wave.Key, Attempt: finished}); err != nil {
				return attempt.AttemptID, fmt.Errorf("gatesched: запись ожидания попытки %s: %w", attempt.AttemptID, err)
			}
		}
		s.store(sl, finished)
		return attempt.AttemptID, nil
	}
	finished.Execution = outcome.Execution
	finished.Verdict = outcome.Verdict
	finished.FinishedAt = s.opts.Clock()
	if err := finished.Validate(); err != nil {
		finished.Execution = workflow.GateExecutionError
		finished.Verdict = ""
		finished.Reason = "исполнитель гейта вернул недопустимый исход: " + err.Error()
	}
	if err := s.journal.Record(Record{Type: RecordFinished, Key: wave.Key, Attempt: finished}); err != nil {
		return attempt.AttemptID, fmt.Errorf("gatesched: запись исхода попытки %s: %w", attempt.AttemptID, err)
	}
	s.store(sl, finished)
	return attempt.AttemptID, nil
}

// store replaces or appends the attempt in the slot; each slot is touched by
// exactly one goroutine per wave.
func (s *Scheduler) store(sl *slot, attempt workflow.GateAttempt) {
	for i := range sl.attempts {
		if sl.attempts[i].AttemptID == attempt.AttemptID {
			sl.attempts[i] = attempt
			return
		}
	}
	sl.attempts = append(sl.attempts, attempt)
}

func (s *Scheduler) newAttempt(wave Wave, sl *slot) workflow.GateAttempt {
	return workflow.GateAttempt{
		TaskID: wave.Key.TaskID, Stage: wave.Key.Stage, Visit: wave.Key.Visit,
		ArtifactSHA256: wave.Key.ArtifactSHA256,
		Gate:           sl.ref.Gate, GateVersion: sl.ref.Version, GateName: sl.ref.InstanceName(),
		Mode:      sl.ref.Mode.Effective(),
		AttemptID: AttemptID(wave.Key, sl.ref.InstanceName(), len(sl.attempts)+1),
		Executor:  sl.def.Kind, Group: sl.group,
		Execution: workflow.GateExecutionRunning,
		StartedAt: s.opts.Clock(),
	}
}

// AttemptID is the deterministic ID of the n-th (1-based) attempt of a gate
// instance on a wave key. Determinism lets a redelivered or replayed record be
// recognized as the same attempt.
func AttemptID(key WaveKey, gateName string, n int) string {
	sha := key.ArtifactSHA256
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("gate-%s-v%d-%s-%s-%d", key.Stage, key.Visit, sha, gateName, n)
}

// invoke runs the runner of the gate's kind.
func (s *Scheduler) invoke(ctx context.Context, in Invocation) Outcome {
	return s.runners[in.Def.Kind].Run(ctx, in)
}

func normalize(outcome Outcome) Outcome {
	outcome.Reason = strings.TrimSpace(outcome.Reason)
	switch outcome.Execution {
	case workflow.GateExecutionSucceeded:
		if !outcome.Verdict.Valid() {
			return Outcome{Execution: workflow.GateExecutionError, Reason: "исполнитель завершил гейт без допустимого вердикта"}
		}
	case workflow.GateExecutionError, workflow.GateExecutionTimeout, workflow.GateExecutionCanceled:
		outcome.Verdict = ""
	case workflow.GateExecutionPending:
		outcome.Verdict = ""
	default:
		return Outcome{Execution: workflow.GateExecutionError, Reason: fmt.Sprintf("исполнитель вернул неизвестное состояние %q", outcome.Execution)}
	}
	return outcome
}

// groupBlocks reports whether the chain must stop after group g.
func (s *Scheduler) groupBlocks(slots slotList, g int) bool {
	for _, sl := range slots {
		if sl.group != g {
			continue
		}
		latest, ok := sl.latest()
		if !ok || latest.Blocking() {
			return true
		}
	}
	return false
}

// finish builds the Result from the slot state. A gate without any attempt
// did not start (an earlier group blocked, or the wave was interrupted).
func (s *Scheduler) finish(slots slotList, result Result, cause error) (Result, error) {
	var failed, technical, waiting bool
	for _, sl := range slots {
		latest, ok := sl.latest()
		if !ok {
			result.NotStarted = append(result.NotStarted, sl.ref.InstanceName())
			continue
		}
		result.Attempts = append(result.Attempts, latest)
		if !latest.Blocking() {
			if latest.Mode.Effective() == workflow.GateModeAdvisory && !passed(latest) {
				result.Warnings = append(result.Warnings, latest)
			}
			continue
		}
		result.Blocking = append(result.Blocking, latest)
		switch {
		case latest.Execution == workflow.GateExecutionPending:
			waiting = true
		case latest.Execution == workflow.GateExecutionSucceeded:
			failed = true
		default:
			technical = true
		}
	}
	switch {
	case failed:
		result.Status = StatusFailed
	case technical:
		result.Status = StatusError
	case waiting:
		result.Status = StatusWaiting
	default:
		result.Status = StatusPassed
	}
	return result, cause
}

func passed(a workflow.GateAttempt) bool {
	return a.Execution == workflow.GateExecutionSucceeded &&
		(a.Verdict == workflow.GateVerdictPass || a.Verdict == workflow.GateVerdictNotApplicable)
}
