package pipeline

import (
	"errors"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
)

type warningReplayEventSource struct{ events []evidence.Event }

func (s warningReplayEventSource) Read(string) ([]evidence.Event, error) { return s.events, nil }
func (s warningReplayEventSource) ReadBytes(string) ([]byte, error)      { return nil, nil }
func (warningReplayEventSource) Append(string, evidence.Event, uint64, string) (evidence.Event, error) {
	return evidence.Event{}, errors.New("append not expected in replay test")
}

type warningReplayRecorder struct{ projected []evidence.Event }

func (*warningReplayRecorder) ReconcileInterrupted(time.Time)                                      {}
func (*warningReplayRecorder) RunStarted(string, string, string, time.Time)                        {}
func (*warningReplayRecorder) RunResumed(string, time.Time)                                        {}
func (*warningReplayRecorder) RunAttached(string)                                                  {}
func (*warningReplayRecorder) RunPaused(string, string, time.Time)                                 {}
func (*warningReplayRecorder) RunCanceled(string, time.Time)                                       {}
func (*warningReplayRecorder) ApprovalRequested(string, string, string, time.Time, map[string]any) {}
func (*warningReplayRecorder) ApprovalDecided(string, string, string, time.Time, map[string]any)   {}
func (*warningReplayRecorder) TransitionSelected(string, string, time.Time, map[string]any)        {}
func (*warningReplayRecorder) StageStarted(string, string, string, int, time.Time)                 {}
func (*warningReplayRecorder) StageFinished(notifier.StageResult)                                  {}
func (*warningReplayRecorder) AttemptsInvalidated(string, []string, time.Time)                     {}
func (*warningReplayRecorder) RunFinished(string, string, time.Time)                               {}
func (r *warningReplayRecorder) EventRecorded(_ string, event evidence.Event) {
	r.projected = append(r.projected, event)
}

func TestReplayTypedEventsReprojectsDurableCheckError(t *testing.T) {
	warning := evidence.Event{
		Type: "check_error", Stage: "qa", AttemptID: "attempt-qa-1",
		Timestamp: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
		Data:      map[string]any{"status": "error", "error": "checker unavailable"},
	}
	recorder := &warningReplayRecorder{}
	state := &runState{
		runID: "run-1",
		p:     &Pipeline{recorder: recorder, eventLogSource: warningReplayEventSource{events: []evidence.Event{warning}}},
	}

	state.replayTypedEvents()
	if len(recorder.projected) != 1 || recorder.projected[0].Type != "check_error" ||
		recorder.projected[0].AttemptID != warning.AttemptID {
		t.Fatalf("durable check_error was not reprojected: %+v", recorder.projected)
	}

	// Reconciliation can run again on later resumes; the real StoreRecorder's
	// stable evidence key ensures this does not create another SQLite row.
	state.replayTypedEvents()
	if len(recorder.projected) != 2 {
		t.Fatalf("expected check_error replay on each reconciliation, got %+v", recorder.projected)
	}
}

var _ Recorder = (*warningReplayRecorder)(nil)
var _ eventRecorder = (*warningReplayRecorder)(nil)
