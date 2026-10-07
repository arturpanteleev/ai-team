package approval

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerStoreRefusesPipelineDecisionCallsAtApplicationBoundary(t *testing.T) {
	controllerStore, err := NewSQLiteStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = controllerStore.Close() }()
	value, err := controllerStore.Create(PendingApproval{
		RunID: "worker-boundary-run", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "coder",
		Trigger: "stage_completed", SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{"reviewer"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "coder", "reject": "$stop"},
	})
	if err != nil {
		t.Fatal(err)
	}

	workerStore := NewWorkerStore(controllerStore)
	for _, mutate := range []func() error{
		func() error {
			_, err := workerStore.Decide(value.RunID, value.ID, Decision{
				ActorID: "worker", ActorRole: "reviewer", Action: "approve", SubjectHash: value.SubjectHash,
			})
			return err
		},
		func() error {
			_, err := workerStore.ResolveDeferred(value.RunID, value.ID, Decision{
				ActorID: "worker", ActorRole: "reviewer", Action: "approve", SubjectHash: value.SubjectHash,
			})
			return err
		},
	} {
		if err := mutate(); !errors.Is(err, ErrWorkerDecisionWrite) {
			t.Fatalf("worker-facing application port must reject decision mutation: %v", err)
		}
	}
	unchanged, err := controllerStore.Load(value.RunID, value.ID)
	if err != nil || unchanged.Status != StatusPending || len(unchanged.Decisions) != 0 {
		t.Fatalf("denied worker calls must leave the controller row pending: %+v err=%v", unchanged, err)
	}
}
