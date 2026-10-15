package gatesched

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Evidence event types written by EventJournal. They are controller-owned
// extension events: replay of the run lifecycle ignores them, the hash chain
// protects them, and the web projection may show them as the gate history.
const (
	EventAttemptStarted  = "gate_attempt_started"
	EventAttemptWaiting  = "gate_attempt_waiting"
	EventAttemptFinished = "gate_attempt_finished"
	EventWaveAborted     = "gate_wave_aborted"
	// EventPinsRecorded is written once at task start with the exact gate
	// definitions (ID, version, kind, digest) the task is bound to.
	EventPinsRecorded = "gate_pins_recorded"
)

// EventSink is the part of the evidence store the journal needs.
type EventSink struct {
	// Read returns the verified event log; found is false when there is none yet.
	Read func() (events []evidence.Event, found bool, err error)
	// Append appends one event durably. It must be safe for concurrent use.
	Append func(evidence.Event) error
	// Projected, when set, receives every appended event for secondary
	// projections (SQLite/WebSocket). It must not fail the append.
	Projected func(evidence.Event)
}

// EventJournal is the evidence-backed Journal. Each record is one event in the
// hash-chained run log; Load folds the log back into attempt states.
type EventJournal struct {
	sink EventSink
}

// NewEventJournal returns a journal over the given evidence sink.
func NewEventJournal(sink EventSink) *EventJournal { return &EventJournal{sink: sink} }

func eventType(t RecordType) string {
	switch t {
	case RecordStarted:
		return EventAttemptStarted
	case RecordWaiting:
		return EventAttemptWaiting
	case RecordFinished:
		return EventAttemptFinished
	default:
		return EventWaveAborted
	}
}

// Record implements Journal.
func (j *EventJournal) Record(rec Record) error {
	data := map[string]any{
		"scope":           rec.Key.Scope,
		"visit":           rec.Key.Visit,
		"artifact_sha256": rec.Key.ArtifactSHA256,
		"task_id":         rec.Key.TaskID,
	}
	if rec.Type == RecordAborted {
		data["reason"] = rec.Note
	} else {
		encoded, err := json.Marshal(rec.Attempt)
		if err != nil {
			return err
		}
		var attempt map[string]any
		if err := json.Unmarshal(encoded, &attempt); err != nil {
			return err
		}
		data["attempt"] = attempt
		data["gate_attempt_id"] = rec.Attempt.AttemptID
		data["gate"] = rec.Attempt.Gate
		data["gate_name"] = rec.Attempt.GateName
		data["execution"] = string(rec.Attempt.Execution)
		if rec.Attempt.Verdict != "" {
			data["verdict"] = string(rec.Attempt.Verdict)
		}
		data["severity"] = severityOf(rec.Attempt)
		data["mode"] = string(rec.Attempt.Mode.Effective())
		if rec.Attempt.Mode.Effective() == workflow.GateModeAdvisory && !passed(rec.Attempt) &&
			rec.Attempt.Execution.Terminal() {
			// A recommendation that did not pass is a visible warning, never a
			// silent success; the transition is not blocked by it.
			data["advisory_warning"] = true
		}
	}
	at := time.Now().UTC()
	switch {
	case rec.Type == RecordStarted && !rec.Attempt.StartedAt.IsZero():
		at = rec.Attempt.StartedAt
	case rec.Type == RecordFinished && !rec.Attempt.FinishedAt.IsZero():
		at = rec.Attempt.FinishedAt
	}
	// The event is addressed to the gate attempt (unique per attempt and
	// record type); the stage attempt it belongs to is data["scope"].
	attemptID := rec.Attempt.AttemptID
	if rec.Type == RecordAborted {
		attemptID = rec.Key.Scope
	}
	event := evidence.Event{
		Type: eventType(rec.Type), Stage: rec.Key.Stage, AttemptID: attemptID,
		Timestamp: at.UTC(), Data: data,
	}
	if err := j.sink.Append(event); err != nil {
		return fmt.Errorf("запись %s: %w", event.Type, err)
	}
	if j.sink.Projected != nil {
		j.sink.Projected(event)
	}
	return nil
}

// severityOf is the projection severity of an attempt record: a clean state is
// info, a negative verdict a warning, a technical failure an error.
func severityOf(attempt workflow.GateAttempt) string {
	switch attempt.Execution {
	case workflow.GateExecutionError, workflow.GateExecutionTimeout, workflow.GateExecutionCanceled:
		return "error"
	case workflow.GateExecutionSucceeded:
		if attempt.Verdict == workflow.GateVerdictFail {
			return "warning"
		}
	}
	return "info"
}

// Load implements Journal.
func (j *EventJournal) Load(key WaveKey) ([]workflow.GateAttempt, error) {
	events, found, err := j.sink.Read()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return FoldAttempts(events, key)
}

func isAttemptEvent(eventType string) bool {
	return eventType == EventAttemptStarted || eventType == EventAttemptWaiting || eventType == EventAttemptFinished
}

// FoldAttempts rebuilds the latest state of every gate attempt recorded for
// the key from an event log, in order of first appearance. Events of other
// stages, scopes, visits or hashes are ignored: this is what keeps an outcome
// of an old result version from being counted for a new one. A terminal
// record is final: a redelivered older record cannot reopen the attempt.
func FoldAttempts(events []evidence.Event, key WaveKey) ([]workflow.GateAttempt, error) {
	var order []string
	latest := map[string]workflow.GateAttempt{}
	for _, event := range events {
		if !isAttemptEvent(event.Type) || event.Stage != key.Stage {
			continue
		}
		attempt, scope, err := decodeAttempt(event)
		if err != nil {
			return nil, fmt.Errorf("событие %s #%d: %w", event.Type, event.Sequence, err)
		}
		if scope != key.Scope || attempt.Visit != key.Visit || attempt.ArtifactSHA256 != key.ArtifactSHA256 || attempt.TaskID != key.TaskID {
			continue
		}
		previous, seen := latest[attempt.AttemptID]
		if !seen {
			order = append(order, attempt.AttemptID)
		} else if previous.Execution.Terminal() {
			continue
		}
		latest[attempt.AttemptID] = attempt
	}
	out := make([]workflow.GateAttempt, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out, nil
}

func decodeAttempt(event evidence.Event) (workflow.GateAttempt, string, error) {
	raw, ok := event.Data["attempt"]
	if !ok {
		return workflow.GateAttempt{}, "", errors.New("нет данных попытки")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return workflow.GateAttempt{}, "", err
	}
	var attempt workflow.GateAttempt
	if err := json.Unmarshal(encoded, &attempt); err != nil {
		return workflow.GateAttempt{}, "", err
	}
	scope, _ := event.Data["scope"].(string)
	if attempt.AttemptID == "" || scope == "" {
		return workflow.GateAttempt{}, "", errors.New("неполная запись попытки")
	}
	return attempt, scope, nil
}

// AllAttempts folds every gate attempt of a stage (any hash, visit or scope;
// all stages when stage is empty) into a chronological history for API and UI.
// Each attempt appears once, with its latest state.
func AllAttempts(events []evidence.Event, stage string) ([]workflow.GateAttempt, error) {
	var order []string
	latest := map[string]workflow.GateAttempt{}
	for _, event := range events {
		if !isAttemptEvent(event.Type) || (stage != "" && event.Stage != stage) {
			continue
		}
		attempt, scope, err := decodeAttempt(event)
		if err != nil {
			return nil, fmt.Errorf("событие %s #%d: %w", event.Type, event.Sequence, err)
		}
		id := scope + "/" + attempt.AttemptID
		previous, seen := latest[id]
		if !seen {
			order = append(order, id)
		} else if previous.Execution.Terminal() {
			continue
		}
		latest[id] = attempt
	}
	out := make([]workflow.GateAttempt, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out, nil
}

// RecordedWave is the folded record of one wave: the attempts recorded for one
// result version (hash) of one visit of a stage, in order of first appearance.
type RecordedWave struct {
	Key      WaveKey
	Attempts []workflow.GateAttempt
}

// RecordedWaves folds the attempts of an event log into waves, ordered by the
// first attempt of each wave. A wave is the unit a transition decision is
// made on (Resolve); an older wave of the same stage stays in the list as
// history and can never resolve a newer version.
func RecordedWaves(events []evidence.Event) ([]RecordedWave, error) {
	var order []WaveKey
	waves := map[WaveKey]*RecordedWave{}
	attempts := map[WaveKey]map[string]int{}
	for _, event := range events {
		if !isAttemptEvent(event.Type) {
			continue
		}
		attempt, scope, err := decodeAttempt(event)
		if err != nil {
			return nil, fmt.Errorf("событие %s #%d: %w", event.Type, event.Sequence, err)
		}
		key := WaveKey{TaskID: attempt.TaskID, Stage: attempt.Stage, Visit: attempt.Visit, Scope: scope, ArtifactSHA256: attempt.ArtifactSHA256}
		wave, seen := waves[key]
		if !seen {
			wave = &RecordedWave{Key: key}
			waves[key] = wave
			attempts[key] = map[string]int{}
			order = append(order, key)
		}
		if index, known := attempts[key][attempt.AttemptID]; known {
			if wave.Attempts[index].Execution.Terminal() {
				continue // a terminal record is final
			}
			wave.Attempts[index] = attempt
			continue
		}
		attempts[key][attempt.AttemptID] = len(wave.Attempts)
		wave.Attempts = append(wave.Attempts, attempt)
	}
	out := make([]RecordedWave, 0, len(order))
	for _, key := range order {
		out = append(out, *waves[key])
	}
	return out, nil
}

// MemoryJournal is an in-memory Journal for tests and tools. It keeps the full
// record log so tests can assert what was written and in which order.
type MemoryJournal struct {
	mu      sync.Mutex
	records []Record
	// FailOn makes Record fail for the given type (test hook).
	FailOn RecordType
}

// Records returns a copy of the record log.
func (m *MemoryJournal) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record(nil), m.records...)
}

// Record implements Journal.
func (m *MemoryJournal) Record(rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailOn != "" && rec.Type == m.FailOn {
		return fmt.Errorf("memory journal: запись %s отклонена", rec.Type)
	}
	m.records = append(m.records, rec)
	return nil
}

// Load implements Journal.
func (m *MemoryJournal) Load(key WaveKey) ([]workflow.GateAttempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var order []string
	latest := map[string]workflow.GateAttempt{}
	for _, rec := range m.records {
		if rec.Type == RecordAborted || rec.Key != key {
			continue
		}
		id := rec.Attempt.AttemptID
		previous, seen := latest[id]
		if !seen {
			order = append(order, id)
		} else if previous.Execution.Terminal() {
			continue
		}
		latest[id] = rec.Attempt
	}
	out := make([]workflow.GateAttempt, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out, nil
}
