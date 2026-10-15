package approval

import (
	"strings"
	"testing"
)

func TestRejectThatKeepsTheStageIsAFeedbackRouteAndNeedsAReason(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	subject := strings.Repeat("c", 64)
	value, err := store.Create(PendingApproval{
		Kind: KindApprove, RunID: "run-1", AttemptID: "attempt-1", FromStage: "product_spec", ToStage: "tech_design",
		Trigger: "graph_outcome:passed", SubjectHash: subject, RequiredRoles: []string{"product_owner"}, Quorum: QuorumAny,
		Actions: []string{"approve", "reject"}, FeedbackActions: []string{"reject"},
		Targets: map[string]string{"approve": "tech_design", "reject": "product_spec"},
	})
	if err != nil {
		t.Fatalf("a rejection that stays at the stage is a valid feedback action: %v", err)
	}
	if !IsFeedbackRoute(value, "reject") || IsFeedbackRoute(value, "approve") {
		t.Fatal("only the staying rejection is a feedback route")
	}
	decision := Decision{ActorID: "po-1", ActorRole: "product_owner", Action: "reject", SubjectHash: subject}
	if _, err := store.Decide("run-1", value.ID, decision); err == nil || !strings.Contains(err.Error(), "причина") {
		t.Fatalf("a refusal without a reason must be rejected: %v", err)
	}
	decision.Comment = "не хватает метрик"
	resolved, err := store.Decide("run-1", value.ID, decision)
	if err != nil || resolved.ResolvedAction != "reject" {
		t.Fatalf("a refusal with a reason resolves the approval: %+v %v", resolved, err)
	}
}

func TestRejectThatStopsTheRunNeedsNoReason(t *testing.T) {
	value := PendingApproval{FromStage: "product_spec", Targets: map[string]string{"reject": "stop"}}
	if IsFeedbackRoute(value, "reject") {
		t.Fatal("a rejection that ends the run is not a rework route")
	}
}
