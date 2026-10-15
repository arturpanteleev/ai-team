package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

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
		raw := fmt.Sprintf("%+v", entry)
		if !strings.Contains(raw, actorDev) || !strings.Contains(raw, "403") {
			t.Fatalf("a denial records the actor and the status: %s", raw)
		}
		denied = append(denied, raw)
	}
	if len(denied) != 1 || !strings.Contains(denied[0], "sync_tracker_now") || !strings.Contains(denied[0], "token_id") {
		t.Fatalf("the team journal must hold the denied sync with the token id: %v", denied)
	}
	// The denied cancel names a task, so it lands in that task's journal.
	events := runEventData(t, env.srv, "run-a", mcpActionEvent)
	if len(events) != 1 || events[0]["tool"] != "cancel_task" || events[0]["denied"] != true ||
		events[0]["status"] != float64(403) || events[0]["actor_id"] != actorDev || events[0]["token_id"] == nil {
		t.Fatalf("task journal denial = %v", events)
	}
}
