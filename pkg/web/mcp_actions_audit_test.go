package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// grantedMCPActions lists the journal records of actions that were carried out,
// leaving the authorization denials (marked denied) aside.
func grantedMCPActions(t *testing.T, srv *Server, runID string) []map[string]any {
	t.Helper()
	var granted []map[string]any
	for _, record := range runEventData(t, srv, runID, mcpActionEvent) {
		if record["denied"] != true {
			granted = append(granted, record)
		}
	}
	return granted
}

// A server failure after the dispatch is remembered like in REST: the action
// may already have had side effects, so a retry with the same key replays the
// answer and does not run the action a second time.
func TestMCPServerErrorAfterDispatchIsCachedForTheKey(t *testing.T) {
	env := newMCPActionEnv(t, nil, map[string]lifecycle.State{"run-a": {Phase: lifecycle.PhaseWaiting, NextStage: "build"}})
	owner := env.as(t, actorOwner)
	owner.do("set_task_tracker_link", map[string]any{"run_id": "run-a", "issue_key": "APP-1"})
	if _, err := env.srv.syncTrackerInbound(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	// The tracker fails exactly once; a real second run would succeed.
	env.tracker.FailNext("changes", errors.New("tracker is down"), 1)
	args := map[string]any{mcpIdempotencyKey: "sync-5xx"}
	first := owner.refused("sync_tracker_now", args)
	if first.Status != 502 {
		t.Fatalf("the injected failure must surface as 502: %+v", first)
	}
	again := owner.refused("sync_tracker_now", args)
	if again.Status != 502 {
		t.Fatalf("a retry with the same key must replay the stored 5xx, not run again: %+v", again)
	}
	// A new key is a new request and now succeeds.
	owner.do("sync_tracker_now", nil)
}

// A refusal for lack of rights is journaled too: who tried which tool, with
// which token and what answer, and never with the arguments.
func TestMCPAuthorizationDenialIsAudited(t *testing.T) {
	env := newMCPActionEnv(t, nil, map[string]lifecycle.State{"run-a": {Phase: lifecycle.PhaseWaiting, NextStage: "build"}})
	dev := env.as(t, actorDev)
	if failure := dev.refused("sync_tracker_now", nil); failure.Status != 403 {
		t.Fatalf("a developer must be refused: %+v", failure)
	}
	if failure := dev.refused("cancel_task", map[string]any{"run_id": "run-a"}); failure.Status != 403 {
		t.Fatalf("a developer must not cancel: %+v", failure)
	}
	audit, err := env.srv.store.ListTeamAudit(100)
	if err != nil {
		t.Fatal(err)
	}
	var denied []string
	for _, entry := range audit {
		if entry.Action != mcpActionEvent {
			continue
		}
		raw := entry.ActorID + " " + string(entry.Details)
		if !strings.Contains(raw, actorDev) || !strings.Contains(raw, "403") {
			t.Fatalf("a denial records the actor and the status: %s", raw)
		}
		denied = append(denied, raw)
	}
	if len(denied) != 2 || !strings.Contains(denied[0], "token_id") || !strings.Contains(denied[1], "token_id") {
		t.Fatalf("the team journal must hold both denials with the token id: %v", denied)
	}
	joined := strings.Join(denied, "\n")
	if !strings.Contains(joined, "sync_tracker_now") || !strings.Contains(joined, "cancel_task") || !strings.Contains(joined, "run-a") {
		t.Fatalf("the denied cancel names the claimed run as data: %v", denied)
	}
	// A refusal never writes into the journal of the run named in its arguments.
	if events := runEventData(t, env.srv, "run-a", mcpActionEvent); len(events) != 0 {
		t.Fatalf("denied call leaked into the run journal: %v", events)
	}
}

// A denied call that names a run that does not exist (or one the caller has no
// right to) must not create or touch any run journal: the claimed id is only
// data in the team audit.
func TestMCPDeniedCallForUnknownRunGoesToTeamAudit(t *testing.T) {
	env := newMCPActionEnv(t, nil, map[string]lifecycle.State{"run-a": {Phase: lifecycle.PhaseWaiting, NextStage: "build"}})
	dev := env.as(t, actorDev)
	if failure := dev.refused("cancel_task", map[string]any{"run_id": "run-does-not-exist"}); failure.Status != 403 {
		t.Fatalf("a developer must be refused: %+v", failure)
	}
	if events := runEventData(t, env.srv, "run-does-not-exist", mcpActionEvent); len(events) != 0 {
		t.Fatalf("a journal appeared for an unknown run: %v", events)
	}
	audit, err := env.srv.store.ListTeamAudit(100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range audit {
		if entry.Action == mcpActionEvent && strings.Contains(string(entry.Details), "run-does-not-exist") && strings.Contains(string(entry.Details), `"denied":true`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("team audit lacks the denied record: %+v", audit)
	}
}
