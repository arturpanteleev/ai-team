package evidence

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
)

// reconcileMissingAgentFinishedEvent repairs the single durable gap left by a
// process stop after attempt_finished but before agent_finished. The
// attempt_finished event and its manifest digest are the authority for the
// completion status; agent_started carries the optional action identity.
func reconcileMissingAgentFinishedEvent(runDir, runID string, eventSource EventLog, manifestSource AttemptManifestSource, events []Event) ([]Event, error) {
	type incompleteCompletion struct {
		started  Event
		finished Event
	}
	started := make(map[string]Event)
	finished := make(map[string]Event)
	completed := make(map[string]Event)
	terminal := false
	for _, event := range events {
		switch event.Type {
		case "agent_started":
			started[event.AttemptID] = event
		case "agent_finished":
			finished[event.AttemptID] = event
		case "attempt_finished":
			completed[event.AttemptID] = event
		case "run_finished":
			terminal = true
		}
	}
	var missing []incompleteCompletion
	for attemptID, attemptFinished := range completed {
		agentStarted, hadStarted := started[attemptID]
		if hadStarted && finished[attemptID].Type == "" {
			missing = append(missing, incompleteCompletion{started: agentStarted, finished: attemptFinished})
		}
	}
	if len(missing) == 0 {
		return events, nil
	}
	if terminal {
		return nil, errors.New("cannot reconcile agent_finished after terminal run_finished")
	}
	if len(missing) != 1 {
		return nil, fmt.Errorf("cannot safely reconcile %d missing agent_finished events", len(missing))
	}
	completion := missing[0]
	status, statusOK := completion.finished.Data["status"].(string)
	if !statusOK || status == "" || completion.finished.Stage != completion.started.Stage || completion.finished.Timestamp.IsZero() {
		return nil, errors.New("cannot reconcile agent_finished from incomplete attempt events")
	}
	data := map[string]any{"status": status}
	for _, key := range []string{"action", "approval_id"} {
		if value, exists := completion.started.Data[key]; exists {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("cannot reconcile agent_finished: agent_started %s is not a string", key)
			}
			data[key] = text
		}
	}
	if value, exists := completion.finished.Data["error"]; exists {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("cannot reconcile agent_finished: attempt_finished error is not a string")
		}
		data["error"] = text
	}
	if manifestDigest, exists := completion.finished.Data["manifest_sha256"]; exists {
		wantDigest, ok := manifestDigest.(string)
		if !ok || !validSHA256(wantDigest) {
			return nil, errors.New("cannot reconcile agent_finished: attempt manifest digest is invalid")
		}
		digest, _, err := attemptManifestDigest(manifestSource, runDir, runID, completion.finished.AttemptID)
		if err != nil || digest != wantDigest {
			return nil, fmt.Errorf("cannot reconcile agent_finished: attempt manifest %s identity mismatch", completion.finished.AttemptID)
		}
	}
	if eventSource == nil {
		path := filepath.Join(runDir, "events.jsonl")
		resolved, reserved, err := defaultEventLogSource(path, runID)
		if err != nil {
			return nil, fmt.Errorf("resolve event log for agent_finished recovery: %w", err)
		}
		if reserved {
			eventSource = resolved
		} else {
			eventSource = newFileEventLog(path)
		}
	}
	lastHash := chainGenesis(runID)
	if len(events) > 0 {
		lastHash = events[len(events)-1].SHA256
	}
	appended, appendErr := eventSource.Append(runID, Event{
		Type: "agent_finished", Stage: completion.finished.Stage,
		AttemptID: completion.finished.AttemptID, Timestamp: completion.finished.Timestamp, Data: data,
	}, uint64(len(events)), lastHash)
	if appendErr != nil {
		latest, readErr := eventSource.Read(runID)
		if readErr != nil {
			return nil, errors.Join(appendErr, fmt.Errorf("read event log after recovery append: %w", readErr))
		}
		for _, event := range latest {
			if event.Type == "agent_finished" && event.AttemptID == completion.finished.AttemptID {
				if event.Stage == completion.finished.Stage && event.Timestamp.Equal(completion.finished.Timestamp) && reflect.DeepEqual(event.Data, data) {
					return latest, nil
				}
				return nil, fmt.Errorf("agent_finished recovery append conflicted with existing attempt %s", completion.finished.AttemptID)
			}
		}
		return nil, appendErr
	}
	if appended.Type != "agent_finished" || appended.AttemptID != completion.finished.AttemptID || appended.Sequence != uint64(len(events)+1) {
		return nil, errors.New("event log returned an invalid recovered agent_finished event")
	}
	return eventSource.Read(runID)
}
