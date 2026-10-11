package control

import (
	"context"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

func checkDecisionApproval(runID, approvalID, stageID, function string) approval.PendingApproval {
	actions := []string{"new_version", "override", "return"}
	targets := map[string]string{
		"new_version": stageID, "override": stageID, "return": stageID,
	}
	return approval.PendingApproval{
		ID: approvalID, RunID: runID, AttemptID: "attempt-check", Status: approval.StatusPending,
		Kind: approval.KindCheckDecision, FromStage: stageID, ToStage: stageID,
		Trigger: approval.StageCheckTrigger, SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{function}, Quorum: approval.QuorumAny,
		Actions: actions, Targets: targets,
	}
}

func TestControllerOverrideStageCheckRequiresReason(t *testing.T) {
	engine := &fakeEngine{}
	store := newRecordingApprovalStore(checkDecisionApproval("run-override", "approval-override", "tech_design", "architect"))
	controller, _ := newStageActionFixture(t, stageActionState("run-override", "tech_design", "approval-override", lifecycle.PhaseWaiting), engine, store)

	for _, reason := range []string{"", "   ", "\n\t"} {
		if err := controller.OverrideStageCheck(context.Background(), "run-override", "tech_design", reason, "actor-1", "architect"); err == nil {
			t.Fatalf("override with reason %q must fail", reason)
		}
	}
	if len(store.decisions) != 0 {
		t.Fatalf("no decision must be recorded for empty reasons: %+v", store.decisions)
	}
}

func TestControllerOverrideStageCheckRejectsNonCheckDecision(t *testing.T) {
	engine := &fakeEngine{}
	store := newRecordingApprovalStore(approval.PendingApproval{
		ID: "approval-input", RunID: "run-override", AttemptID: "attempt-1", Status: approval.StatusPending,
		Kind: approval.KindInput, FromStage: "tech_design", ToStage: "tech_design", Trigger: "human_input",
		SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"architect"}, Quorum: approval.QuorumAny,
		Actions: []string{"reject", "submit"}, Targets: map[string]string{"reject": "tech_design", "submit": "tech_design"},
	})
	controller, _ := newStageActionFixture(t, stageActionState("run-override", "tech_design", "approval-input", lifecycle.PhaseWaiting), engine, store)

	if err := controller.OverrideStageCheck(context.Background(), "run-override", "tech_design", "ok", "actor-1", "architect"); err == nil {
		t.Fatal("override of a non-check_decision approval must fail")
	}
}

func TestControllerOverrideStageCheckRejectsWrongStage(t *testing.T) {
	engine := &fakeEngine{}
	store := newRecordingApprovalStore(checkDecisionApproval("run-override", "approval-override", "tech_design", "architect"))
	controller, _ := newStageActionFixture(t, stageActionState("run-override", "tech_design", "approval-override", lifecycle.PhaseWaiting), engine, store)

	if err := controller.OverrideStageCheck(context.Background(), "run-override", "qa", "ok", "actor-1", "architect"); err == nil {
		t.Fatal("override of a non-current stage must fail")
	}
}

func TestControllerOverrideStageCheckRequiresPendingDecision(t *testing.T) {
	engine := &fakeEngine{}
	store := newRecordingApprovalStore()
	controller, _ := newStageActionFixture(t, stageActionState("run-override", "tech_design", "", lifecycle.PhaseWaiting), engine, store)

	if err := controller.OverrideStageCheck(context.Background(), "run-override", "tech_design", "ok", "actor-1", "architect"); err == nil {
		t.Fatal("override without a pending approval must fail")
	}
}

func TestControllerOverrideStageCheckResolvesAndResumes(t *testing.T) {
	engine := &fakeEngine{started: make(chan struct{}), release: make(chan struct{})}
	store := newRecordingApprovalStore(checkDecisionApproval("run-override", "approval-override", "tech_design", "architect"))
	controller, _ := newStageActionFixture(t, stageActionState("run-override", "tech_design", "approval-override", lifecycle.PhaseWaiting), engine, store)

	if err := controller.OverrideStageCheck(context.Background(), "run-override", "tech_design", "риск принят", "actor-1", "architect"); err != nil {
		t.Fatal(err)
	}
	if len(store.decisions) != 1 || store.decisions[0].Action != "override" ||
		store.decisions[0].ActorID != "actor-1" || store.decisions[0].ActorRole != "architect" ||
		store.decisions[0].Comment != "риск принят" || !store.decisions[0].ControllerAuthenticated {
		t.Fatalf("override decision mismatch: %+v", store.decisions)
	}
	<-engine.started
	close(engine.release)
	waitControllerIdle(t, controller, "run-override")
}
