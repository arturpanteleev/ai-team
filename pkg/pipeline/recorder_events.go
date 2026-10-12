package pipeline

import "github.com/arturpanteleev/ai-team/pkg/evidence"

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
