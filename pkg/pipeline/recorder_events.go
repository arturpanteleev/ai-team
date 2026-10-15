package pipeline

import (
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/logging"
)

var taskWarningEventTypes = map[string]struct{}{
	"check_error": {}, "check_overridden": {}, "description_missing": {}, "stage_skipped": {},
	"budget_exceeded": {},
	// The gate attempt history: every attempt shows its start, wait and
	// outcome with times in the task feed, and is repaired from evidence.
	gatesched.EventAttemptStarted: {}, gatesched.EventAttemptWaiting: {}, gatesched.EventAttemptFinished: {},
	gatesched.EventWaveAborted: {},
	// The request to run technically failed gates again (S06).
	gatesched.EventGateRetryRequested: {},
}

// eventRecorder is optional so external Recorder implementations retain
// source compatibility while web recorders can project typed warning events.
type eventRecorder interface {
	EventRecorded(runID string, event evidence.Event)
}

func (rs *runState) recordTypedEvent(event evidence.Event) {
	if rs.p.recorder == nil {
		return
	}
	if recorder, ok := rs.p.recorder.(eventRecorder); ok {
		recorder.EventRecorded(rs.runID, event)
	}
}

// replayTypedEvents repairs the secondary SQLite task feed from authoritative
// evidence. A process can stop after appending evidence but before recording
// the task event; StoreRecorder deduplicates this replay by evidence identity.
func (rs *runState) replayTypedEvents() {
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		logging.Printf("warning: cannot replay task warning events: %v\n", err)
		return
	}
	if !found {
		return
	}
	for _, event := range events {
		if _, ok := taskWarningEventTypes[event.Type]; ok {
			rs.recordTypedEvent(event)
		} else if flagged, _ := event.Data["warning"].(bool); flagged {
			// e.g. a passed check that carries advisory warnings
			rs.recordTypedEvent(event)
		}
	}
}
