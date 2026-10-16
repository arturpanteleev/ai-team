package approvaltest

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

func TestTrustedStoreRequiresControllerProvenance(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value := approval.PendingApproval{ID: "a", SubjectHash: "h", Status: approval.StatusResolved, ResolvedAction: "approve",
		Decisions: []approval.Decision{{ApprovalID: "a", SubjectHash: "h", Action: "approve", ControllerAuthenticated: true}}}
	if !store.HasAuthenticatedControllerDecision(value) {
		t.Fatal("a controller decision must be trusted")
	}
	value.Decisions[0].ControllerAuthenticated = false
	if store.HasAuthenticatedControllerDecision(value) {
		t.Fatal("a decision without controller provenance must not be trusted")
	}
	if store.HasAuthenticatedControllerDecision(approval.PendingApproval{}) {
		t.Fatal("an unresolved approval must not be trusted")
	}
}
