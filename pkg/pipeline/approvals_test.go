package pipeline

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

func TestPriorApprovalIn(t *testing.T) {
	subject := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := func() approval.PendingApproval {
		return approval.PendingApproval{
			ID: "approval-x", RunID: "run-1", AttemptID: "attempt-1",
			FromStage: "approver", ToStage: "deployer", Trigger: "graph_outcome:passed",
			SubjectHash: subject, RequiredRoles: []string{"operator"},
			Actions: []string{"approve", "reject"},
			Targets: map[string]string{"approve": "deployer", "reject": "$stop"},
		}
	}
	t.Run("пустой список", func(t *testing.T) {
		if got := priorApprovalIn(nil, "approver", "deployer", "graph_outcome:passed", subject, false); got != nil {
			t.Fatalf("ожидался nil: %+v", got)
		}
	})
	t.Run("resolved approve повторно не запрашивается", func(t *testing.T) {
		value := base()
		value.Status = approval.StatusResolved
		value.ResolvedAction = "approve"
		if got := priorApprovalIn([]approval.PendingApproval{value}, "approver", "deployer", "graph_outcome:passed", subject, false); got == nil {
			t.Fatal("resolved approval того же subject должен находиться")
		}
	})
	t.Run("другой subject не совпадает", func(t *testing.T) {
		value := base()
		value.Status = approval.StatusResolved
		value.ResolvedAction = "approve"
		if got := priorApprovalIn([]approval.PendingApproval{value}, "approver", "deployer", "graph_outcome:passed", subject[:63]+"b", false); got != nil {
			t.Fatal("другой subject не должен совпадать")
		}
	})
	t.Run("pending deferred гейт того же перехода не дублируется", func(t *testing.T) {
		value := base()
		value.Status = approval.StatusPending
		value.Deferred = true
		list := []approval.PendingApproval{value}
		if got := priorApprovalIn(list, "approver", "deployer", "graph_outcome:passed", subject, true); got == nil {
			t.Fatal("pending deferred гейт того же subject должен находиться")
		}
		if got := priorApprovalIn(list, "approver", "deployer", "graph_outcome:passed", subject, false); got != nil {
			t.Fatal("deferred гейт не должен совпадать с обычным запросом")
		}
	})
	t.Run("resolved к другому target не переиспользуется", func(t *testing.T) {
		value := base()
		value.Status = approval.StatusResolved
		value.ResolvedAction = "reject"
		if got := priorApprovalIn([]approval.PendingApproval{value}, "approver", "deployer", "graph_outcome:passed", subject, false); got != nil {
			t.Fatal("resolved reject к $stop не должен совпадать с переходом к deployer")
		}
	})
	t.Run("resolved action вне Actions ребра не переиспользуется", func(t *testing.T) {
		value := base()
		value.Status = approval.StatusResolved
		value.ResolvedAction = "override_approve"
		value.Targets = map[string]string{"override_approve": "deployer"}
		value.Actions = []string{"approve"}
		if got := priorApprovalIn([]approval.PendingApproval{value}, "approver", "deployer", "graph_outcome:passed", subject, false); got != nil {
			t.Fatal("resolved action вне текущих actions не должен переиспользоваться")
		}
	})
}
