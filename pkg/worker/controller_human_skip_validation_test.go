package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestControllerHumanInputAuthorizationRejectsMismatchedAuthority(t *testing.T) {
	base := workerHumanSkipApproval("human-auth-run", "human-auth-approval")
	tests := []struct {
		name      string
		store     func(approval.PendingApproval) *apiApprovalStore
		approval  func(*approval.PendingApproval)
		events    func([]evidence.Event) []evidence.Event
		stage     string
		id        string
		wantValid bool
	}{
		{name: "valid", wantValid: true},
		{name: "missing store", store: func(approval.PendingApproval) *apiApprovalStore { return nil }},
		{name: "missing id", id: ""},
		{name: "store load error", store: func(v approval.PendingApproval) *apiApprovalStore {
			return &apiApprovalStore{values: map[string]approval.PendingApproval{}, loadErr: errors.New("unavailable")}
		}},
		{name: "wrong run", approval: func(v *approval.PendingApproval) { v.RunID = "another-run" }},
		{name: "wrong id", approval: func(v *approval.PendingApproval) { v.ID = "other-approval" }},
		{name: "wrong kind", approval: func(v *approval.PendingApproval) { v.Kind = approval.KindApprove }},
		{name: "wrong trigger", approval: func(v *approval.PendingApproval) { v.Trigger = "graph_outcome:blocked" }},
		{name: "pending", approval: func(v *approval.PendingApproval) { v.Status = approval.StatusPending }},
		{name: "wrong from stage", approval: func(v *approval.PendingApproval) { v.FromStage = "other" }},
		{name: "wrong to stage", approval: func(v *approval.PendingApproval) { v.ToStage = "other" }},
		{name: "missing attempt", approval: func(v *approval.PendingApproval) { v.AttemptID = "" }},
		{name: "missing subject", approval: func(v *approval.PendingApproval) { v.SubjectHash = "" }},
		{name: "missing resolved time", approval: func(v *approval.PendingApproval) { v.ResolvedAt = time.Time{} }},
		{name: "missing action", approval: func(v *approval.PendingApproval) { v.ResolvedAction = "" }},
		{name: "action not listed", approval: func(v *approval.PendingApproval) { v.Actions = []string{"submit"} }},
		{name: "wrong target", approval: func(v *approval.PendingApproval) { v.Targets["skip"] = "other" }},
		{name: "no decision", approval: func(v *approval.PendingApproval) { v.Decisions = nil }},
		{name: "invalid payload", approval: func(v *approval.PendingApproval) { v.Payload = json.RawMessage(`{`) }},
		{name: "wrong payload kind", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"approve","stage_id":"optional","output_name":"out","output_path":"out.md"}`)
		}},
		{name: "wrong payload stage", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"input","stage_id":"elsewhere","output_name":"out","output_path":"out.md"}`)
		}},
		{name: "empty output name", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"input","stage_id":"optional","output_path":"out.md"}`)
		}},
		{name: "empty output path", approval: func(v *approval.PendingApproval) {
			v.Payload = json.RawMessage(`{"kind":"input","stage_id":"optional","output_name":"out"}`)
		}},
		{name: "decision approval mismatch", approval: func(v *approval.PendingApproval) { v.Decisions[0].ApprovalID = "other" }},
		{name: "decision subject mismatch", approval: func(v *approval.PendingApproval) { v.Decisions[0].SubjectHash = strings.Repeat("e", 64) }},
		{name: "decision action mismatch", approval: func(v *approval.PendingApproval) { v.Decisions[0].Action = "submit" }},
		{name: "decision actor missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].ActorID = " " }},
		{name: "decision role missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].ActorRole = " " }},
		{name: "decision time missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].DecidedAt = time.Time{} }},
		{name: "skip reason missing", approval: func(v *approval.PendingApproval) { v.Decisions[0].Comment = "  " }},
		{name: "decision role unauthorized", approval: func(v *approval.PendingApproval) { v.RequiredRoles = []string{"reviewer"} }},
		{name: "no decision event", events: func([]evidence.Event) []evidence.Event { return nil }},
		{name: "wrong event attempt", events: func(events []evidence.Event) []evidence.Event { events[0].AttemptID = "other-attempt"; return events }},
		{name: "wrong event identity", events: func(events []evidence.Event) []evidence.Event { events[0].Data["trigger"] = "other"; return events }},
		{name: "wrong event decisions", events: func(events []evidence.Event) []evidence.Event { events[0].Data["decisions"] = []any{}; return events }},
		{name: "wrong stage", stage: "other"},
		{name: "wrong lookup id", id: "other-approval"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := workerHumanSkipApproval(base.RunID, base.ID)
			value.Decisions = append([]approval.Decision(nil), value.Decisions...)
			value.Actions = append([]string(nil), value.Actions...)
			value.RequiredRoles = append([]string(nil), value.RequiredRoles...)
			value.Targets = map[string]string{"skip": "optional", "submit": "optional"}
			if tc.approval != nil {
				tc.approval(&value)
			}
			store := &apiApprovalStore{values: map[string]approval.PendingApproval{value.RunID + "/" + base.ID: value}}
			if tc.store != nil {
				store = tc.store(value)
			}
			if tc.name == "missing store" {
				store = nil
			}
			events := workerHumanAuthorizationEvents(value)
			if tc.events != nil {
				events = tc.events(events)
			}
			stage, id := "optional", base.ID
			if tc.stage != "" {
				stage = tc.stage
			}
			if tc.id != "" || tc.name == "missing id" {
				id = tc.id
			}
			var authority workerApprovalPort
			if store != nil {
				authority = store
			}
			server := &workerAPIServer{scope: workerAPIScope{RunID: base.RunID}, approvals: authority}
			_, _, err := server.authorizedHumanInput(events, stage, id)
			if tc.wantValid && err != nil {
				t.Fatalf("valid controller approval rejected: %v", err)
			}
			if !tc.wantValid && err == nil {
				t.Fatal("mismatched human input authority was accepted")
			}
		})
	}
}

func TestControllerHumanSkipRecoveryHasNoAgentAuthorityAndIgnoresCompletedRecords(t *testing.T) {
	server := &workerAPIServer{scope: workerAPIScope{RunID: "human-skip-scan", TargetDir: "/tmp/human-skip-scan"}}
	start := evidence.Event{Type: "attempt_started", Stage: "agent", AttemptID: "agent-skip", Data: map[string]any{
		"executor": "agent", "stage_action": "skip",
	}}
	finish := evidence.Event{Type: "attempt_finished", Stage: "agent", AttemptID: "agent-skip", Data: map[string]any{
		"executor": "agent", "outcome": "skipped",
	}}
	if err := server.validateMissingHumanSkipAuthorities([]evidence.Event{start, finish}); err == nil || !strings.Contains(err.Error(), "no authority to repair agent skip") {
		t.Fatalf("worker-backed controller accepted agent skip recovery without authority: %v", err)
	}
	if err := server.validateMissingHumanSkipAuthorities([]evidence.Event{start}); err != nil {
		t.Fatalf("unfinished attempt should not trigger skip recovery: %v", err)
	}
	warning := evidence.Event{Type: "stage_skipped", Stage: "agent", AttemptID: "agent-skip"}
	if err := server.validateMissingHumanSkipAuthorities([]evidence.Event{start, finish, warning}); err != nil {
		t.Fatalf("existing warning should not require a recovery authority lookup: %v", err)
	}

	humanServer := &workerAPIServer{scope: workerAPIScope{RunID: "human-skip-scan", TargetDir: t.TempDir()},
		approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{}}}
	humanStart := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "human-skip", Timestamp: time.Now().UTC(), Data: map[string]any{
		"executor": "human", "stage_action": "skip", "stage_skip_version": float64(evidence.StageSkipProtocolVersion),
		"stage_index": float64(1), "actor_id": "alice", "actor_role": "product_owner", "human_input_approval_id": "missing",
	}}
	humanFinish := evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: "human-skip", Timestamp: humanStart.Timestamp.Add(time.Second), Data: map[string]any{
		"executor": "human", "outcome": "skipped", "actor_id": "alice", "actor_role": "product_owner",
		"human_input_approval_id": "missing", "stage_skip_reason": "No document is needed.",
	}}
	if err := humanServer.validateMissingHumanSkipAuthorities([]evidence.Event{humanStart, humanFinish}); err == nil || !strings.Contains(err.Error(), "authorize human skip recovery") {
		t.Fatalf("recovery accepted a human skip without approval authority: %v", err)
	}
	value := workerHumanSkipApproval("human-skip-scan", "recovery-approval")
	humanStart.Data["human_input_approval_id"] = value.ID
	humanFinish.Data["human_input_approval_id"] = value.ID
	humanServer.approvals = &apiApprovalStore{values: map[string]approval.PendingApproval{value.RunID + "/" + value.ID: value}}
	events := append(workerHumanAuthorizationEvents(value), humanStart, humanFinish)
	if err := humanServer.validateMissingHumanSkipAuthorities(events); err == nil || !strings.Contains(err.Error(), "approval-matched empty-output manifest") {
		t.Fatalf("recovery accepted a human skip without its controller manifest: %v", err)
	}
}

func TestControllerHumanAppendHelpersRejectMalformedAndRetrySafely(t *testing.T) {
	server := &workerAPIServer{scope: workerAPIScope{RunID: "human-helper-run"}}
	for _, request := range []evidence.Event{
		{}, {Type: "wrong", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1, "human_input_approval_id": "id"}},
		{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1}},
		{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1, "human_input_approval_id": "id", "actor_id": "worker"}},
		{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"stage_index": 1, "human_input_approval_id": 9}},
	} {
		if _, err := server.appendControllerHumanAttemptStarted(request); err == nil {
			t.Fatalf("malformed human start was accepted: %+v", request)
		}
	}
	for _, request := range []evidence.Event{
		{}, {Type: "wrong", Stage: "optional", AttemptID: "attempt"},
		{Type: "attempt_finished", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"outcome": "skipped"}},
	} {
		if _, err := server.appendControllerHumanAttemptFinished(request); err == nil {
			t.Fatalf("malformed human finish was accepted: %+v", request)
		}
	}
	for _, request := range []evidence.Event{
		{}, {Type: "wrong", Stage: "optional", AttemptID: "attempt"},
		{Type: "stage_skipped", Stage: "optional", AttemptID: "attempt", Data: map[string]any{"reason": "worker"}},
	} {
		if _, err := server.appendControllerHumanStageSkip(request, nil); err == nil {
			t.Fatalf("malformed stage skip was accepted: %+v", request)
		}
	}

	stored := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "attempt", Timestamp: time.Unix(10, 0).UTC(),
		Data: map[string]any{"executor": "human", "stage_index": float64(1)}}
	server.eventLogs = nil
	if got, err := server.appendControllerHumanEvent([]evidence.Event{stored}, stored, false); err != nil || got.AttemptID != stored.AttemptID {
		t.Fatalf("exact controller event retry was not idempotent: event=%+v err=%v", got, err)
	}
	conflict := stored
	conflict.Data = map[string]any{"executor": "agent", "stage_index": float64(1)}
	if _, err := server.appendControllerHumanEvent([]evidence.Event{stored}, conflict, false); err == nil {
		t.Fatal("conflicting retry was accepted")
	}
	if _, err := server.appendControllerHumanEvent(nil, stored, false); err == nil {
		t.Fatal("controller append without an initialized run chain was accepted")
	}
	if _, err := server.appendControllerHumanEvent(nil, stored, true); err == nil {
		t.Fatal("stage skip append without an initialized run chain was accepted")
	}
	if controllerEventDataEqual(map[string]any{"unsupported": make(chan int)}, map[string]any{}) {
		t.Fatal("event data with non-serializable authority was treated as an idempotent retry")
	}
	badCandidate := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "bad-attempt", Timestamp: time.Now().UTC(), Data: map[string]any{
		"executor": "human", "stage_index": "not-a-number", "actor_id": "alice", "actor_role": "product_owner", "human_input_approval_id": "approval",
	}}
	chain := []evidence.Event{{Type: "run_started", RunID: server.scope.RunID, Sequence: 1, Timestamp: time.Now().UTC()}}
	server.scope.TargetDir = t.TempDir()
	server.eventLogs = &validationEventLog{events: chain}
	if _, err := server.appendControllerHumanEvent(chain, badCandidate, false); err == nil {
		t.Fatal("controller accepted an invalid human attempt event")
	}
	if event, ok := controllerEventForAttempt([]evidence.Event{stored}, stored.Type, stored.Stage, stored.AttemptID); !ok || event.AttemptID != stored.AttemptID {
		t.Fatal("controller event lookup did not find matching identity")
	}
	if _, ok := controllerEventForAttempt(nil, stored.Type, stored.Stage, stored.AttemptID); ok {
		t.Fatal("controller event lookup matched an absent event")
	}
	for _, tc := range []struct {
		value any
		want  int
	}{{1, 1}, {float64(2), 2}, {float64(1.5), 0}, {"1", 0}, {nil, 0}} {
		if got := numericWorkerVersion(tc.value); got != tc.want {
			t.Fatalf("numericWorkerVersion(%v)=%d want %d", tc.value, got, tc.want)
		}
	}
}

func TestControllerHumanDispatchEnforcesTransportAndLifecycleBoundaries(t *testing.T) {
	const runID = "human-dispatch-boundaries"
	request := workerAPICall{RunID: runID, Event: evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: "attempt"}}
	for _, tc := range []struct {
		name   string
		method string
		server *workerAPIServer
		call   workerAPICall
		want   string
	}{
		{name: "start requires trusted transport", method: "human_attempt.start", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}}, call: request, want: "bubblewrap Unix transport"},
		{name: "start run identity", method: "human_attempt.start", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: workerAPICall{Event: request.Event}, want: "run mismatch"},
		{name: "start operation", method: "human_attempt.start", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: request, want: "not allowed"},
		{name: "finish requires trusted transport", method: "human_attempt.finish", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}}, call: request, want: "bubblewrap Unix transport"},
		{name: "finish run identity", method: "human_attempt.finish", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: workerAPICall{Event: request.Event}, want: "run mismatch"},
		{name: "finish operation", method: "human_attempt.finish", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: request, want: "not allowed"},
		{name: "skip requires trusted transport", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}}, call: request, want: "bubblewrap Unix transport"},
		{name: "skip request identity", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: workerAPICall{Event: request.Event}, want: "only run, stage, and attempt identity"},
		{name: "skip operation", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationCancel}, usageAllowed: true, eventLogs: &validationEventLog{}}, call: request, want: "not allowed"},
		{name: "skip journal read", method: "stage_skip.complete", server: &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationResume}, usageAllowed: true,
			eventLogs: &validationEventLog{readErr: errors.New("journal read failed")}}, call: request, want: "read event chain before controller stage skip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.server.dispatch(tc.method, tc.call)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("dispatch error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestControllerHumanFinishAndSkipRequireTheirOwnEvidence(t *testing.T) {
	const runID, attemptID, approvalID = "human-finish-evidence", "attempt-evidence", "approval-evidence"
	value := workerHumanSkipApproval(runID, approvalID)
	started := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: attemptID,
		Timestamp: time.Now().UTC(), Data: map[string]any{"executor": "human", "stage_index": float64(1), "human_input_approval_id": approvalID}}
	authorized := workerHumanAuthorizationEvents(value)

	t.Run("finish requires event chain", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{}); err == nil || !strings.Contains(err.Error(), "invalid identity or fields") {
			t.Fatalf("malformed finish reached event chain: %v", err)
		}
	})
	t.Run("finish requires controller human start", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "no controller-owned start") {
			t.Fatalf("finish without a controller human start: %v", err)
		}
	})
	t.Run("finish fails closed when its journal cannot be read", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{readErr: errors.New("controller journal unavailable")}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "controller journal unavailable") {
			t.Fatalf("finish ignored a journal read failure: %v", err)
		}
	})
	t.Run("finish requires matching approval", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{events: []evidence.Event{started}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "approval authority is unavailable") {
			t.Fatalf("finish without an approval authority: %v", err)
		}
	})
	t.Run("finish requires controller manifest", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: t.TempDir()}, eventLogs: &validationEventLog{events: append(authorized, started)},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "read controller human attempt manifest") {
			t.Fatalf("finish without a controller manifest: %v", err)
		}
	})
	t.Run("finish rejects a manifest for another stage", func(t *testing.T) {
		target := t.TempDir()
		manifest := workerHumanSkipManifest(runID, attemptID, started.Timestamp, started.Timestamp.Add(time.Second), approvalID, "alice", "product_owner")
		manifest.Stage = "elsewhere"
		manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
		if err := manifestStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		if err := manifestStore.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: append(authorized, started)},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "manifest does not match") {
			t.Fatalf("finish accepted a manifest for another stage: %v", err)
		}
	})
	t.Run("skip decision requires a skipped manifest", func(t *testing.T) {
		target := t.TempDir()
		finishedAt := started.Timestamp.Add(time.Second)
		manifest := workerHumanSkipManifest(runID, attemptID, started.Timestamp, finishedAt, approvalID, "alice", "product_owner")
		manifest.Outcome, manifest.Status, manifest.Decision = string(workflow.OutcomePassed), string(workflow.OutcomePassed), string(workflow.DecisionApproved)
		manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
		if err := manifestStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		if err := manifestStore.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: append(authorized, started)},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "skip manifest") {
			t.Fatalf("skip approval accepted a passed manifest: %v", err)
		}
	})
	t.Run("submit decision cannot finish as skipped", func(t *testing.T) {
		target := t.TempDir()
		submit := workerHumanSubmitApproval(runID, approvalID)
		finishedAt := started.Timestamp.Add(time.Second)
		manifest := workerHumanSkipManifest(runID, attemptID, started.Timestamp, finishedAt, approvalID, "alice", "product_owner")
		manifestStore := evidence.ControllerAttemptManifestStore{TargetDir: target}
		if err := manifestStore.Reserve(runID); err != nil {
			t.Fatal(err)
		}
		if err := manifestStore.Write(runID, manifest); err != nil {
			t.Fatal(err)
		}
		events := append(workerHumanAuthorizationEvents(submit), started)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, TargetDir: target}, eventLogs: &validationEventLog{events: events},
			approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: submit}}}
		if _, err := server.appendControllerHumanAttemptFinished(evidence.Event{Type: "attempt_finished", Stage: "optional", AttemptID: attemptID}); err == nil || !strings.Contains(err.Error(), "cannot be skipped") {
			t.Fatalf("submit approval accepted a skipped manifest: %v", err)
		}
	})
	t.Run("skip warning requires human start", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, nil); err == nil || !strings.Contains(err.Error(), "requires a human attempt") {
			t.Fatalf("warning without a human start: %v", err)
		}
	})
	t.Run("skip warning requires approved skip action", func(t *testing.T) {
		submit := workerHumanSubmitApproval(runID, approvalID)
		events := workerHumanAuthorizationEvents(submit)
		events = append(events, started)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: submit}}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, events); err == nil || !strings.Contains(err.Error(), "not authorized") {
			t.Fatalf("submit approval authorized a skip warning: %v", err)
		}
	})
	t.Run("skip warning requires matching finished attempt", func(t *testing.T) {
		events := append(authorized, started)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, events); err == nil || !strings.Contains(err.Error(), "no matching approved finished attempt") {
			t.Fatalf("warning without an approved skipped finish: %v", err)
		}
	})
	t.Run("skip warning requires durable approval", func(t *testing.T) {
		unauthorized := started
		unauthorized.Data = map[string]any{"executor": "human", "stage_index": float64(1), "human_input_approval_id": "missing-approval"}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{}}}
		if _, err := server.appendControllerHumanStageSkip(evidence.Event{Type: "stage_skipped", Stage: "optional", AttemptID: attemptID}, []evidence.Event{unauthorized}); err == nil || !strings.Contains(err.Error(), "approval") {
			t.Fatalf("warning without a durable input approval: %v", err)
		}
	})
}

func TestControllerHumanStartReadsAuthorityBeforeCreatingAttempt(t *testing.T) {
	const runID, approvalID = "human-start-boundary", "approval-start-boundary"
	value := workerHumanSkipApproval(runID, approvalID)
	request := evidence.Event{Type: "attempt_started", Stage: "optional", AttemptID: "attempt-start-boundary",
		Data: map[string]any{"stage_index": float64(1), "human_input_approval_id": approvalID}}
	authority := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}
	t.Run("rejects extra actor authority fields", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
		forged := request
		forged.Data = map[string]any{"stage_index": float64(1), "actor_id": "worker"}
		if _, err := server.appendControllerHumanAttemptStarted(forged); err == nil || !strings.Contains(err.Error(), "unauthorized field") {
			t.Fatalf("worker actor field was accepted: %v", err)
		}
	})
	t.Run("rejects journal read failures", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{readErr: errors.New("journal unavailable")}}
		if _, err := server.appendControllerHumanAttemptStarted(request); err == nil || !strings.Contains(err.Error(), "journal unavailable") {
			t.Fatalf("human start ignored journal read failure: %v", err)
		}
	})
	t.Run("rejects a start without a matching durable approval event", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{}, approvals: authority}
		if _, err := server.appendControllerHumanAttemptStarted(request); err == nil || !strings.Contains(err.Error(), "matching durable approval_decided event") {
			t.Fatalf("human start without a durable approval event was accepted: %v", err)
		}
	})
	t.Run("zero timestamp is assigned only after authority is verified", func(t *testing.T) {
		events := workerHumanAuthorizationEvents(value)
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, eventLogs: &validationEventLog{events: events}, approvals: authority}
		if _, err := server.appendControllerHumanAttemptStarted(request); err != nil && strings.Contains(err.Error(), "predates") {
			t.Fatalf("server timestamp was checked before it was assigned: %v", err)
		}
	})
}

func workerHumanAuthorizationEvents(value approval.PendingApproval) []evidence.Event {
	return []evidence.Event{{Type: "approval_decided", AttemptID: value.AttemptID, Data: map[string]any{
		"approval_id": value.ID, "kind": string(value.Kind), "subject_hash": value.SubjectHash,
		"status": string(value.Status), "resolved_action": value.ResolvedAction,
		"from_stage": value.FromStage, "to_stage": value.ToStage, "trigger": value.Trigger,
		"decisions": value.Decisions,
	}}}
}

type failingHumanSkipAppendLog struct {
	evidence.EventLog
	appendErr error
}

func (l failingHumanSkipAppendLog) Append(string, evidence.Event, uint64, string) (evidence.Event, error) {
	return evidence.Event{}, l.appendErr
}
