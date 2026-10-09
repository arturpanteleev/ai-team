package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTakeStageRecordsOwnerAndTakeover(t *testing.T) {
	s := newTestStore(t)
	started := time.Date(2026, 10, 9, 8, 0, 0, 0, time.FixedZone("test", 7*60*60))
	run := &PipelineRun{RunID: "run-owner", Feature: "feature", Status: "waiting_for_approval", StartedAt: started}
	if err := s.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}

	firstAt := started.Add(time.Minute)
	first, changed, err := s.TakeStage(run.RunID, "product_spec", "input-a", "alice@example.test", "po", firstAt)
	if err != nil || !changed {
		t.Fatalf("first take = %+v changed=%v err=%v", first, changed, err)
	}
	if first.ApprovalID != "input-a" || first.ActorID != "alice@example.test" || first.ActorRole != "po" || !first.TakenAt.Equal(firstAt.UTC()) {
		t.Fatalf("unexpected first owner: %+v", first)
	}

	// The same user's retry after an uncertain HTTP response must not reset the
	// elapsed duration or create another event.
	retried, changed, err := s.TakeStage(run.RunID, "product_spec", "input-a", "alice@example.test", "po", firstAt.Add(time.Minute))
	if err != nil || changed || !retried.TakenAt.Equal(firstAt.UTC()) {
		t.Fatalf("same-owner retry = %+v changed=%v err=%v", retried, changed, err)
	}

	secondAt := firstAt.Add(3 * time.Minute)
	second, changed, err := s.TakeStage(run.RunID, "product_spec", "input-a", "bob@example.test", "architect", secondAt)
	if err != nil || !changed || second.ActorID != "bob@example.test" || !second.TakenAt.Equal(secondAt.UTC()) {
		t.Fatalf("takeover = %+v changed=%v err=%v", second, changed, err)
	}

	owners, err := s.GetStageOwners(run.RunID)
	if err != nil || owners["product_spec"].ApprovalID != "input-a" || owners["product_spec"].ActorID != "bob@example.test" || !owners["product_spec"].TakenAt.Equal(secondAt.UTC()) {
		t.Fatalf("current owners=%+v err=%v", owners, err)
	}
	events, err := s.GetEventsAfter(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "stage_taken" || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("events=%+v", events)
	}
	var takeover map[string]string
	if err := json.Unmarshal([]byte(events[1].DataJSON), &takeover); err != nil {
		t.Fatal(err)
	}
	if takeover["stage_id"] != "product_spec" || takeover["approval_id"] != "input-a" || takeover["actor_id"] != "bob@example.test" ||
		takeover["actor_name"] != "bob@example.test" || takeover["previous_actor_id"] != "alice@example.test" ||
		takeover["previous_actor_name"] != "alice@example.test" {
		t.Fatalf("takeover event omitted either participant: %+v", takeover)
	}
}

func TestTakeStageRejectsUnknownRun(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.TakeStage("missing", "stage", "approval", "alice", "product_owner", time.Now()); err == nil {
		t.Fatal("TakeStage accepted an unknown run")
	}
}
