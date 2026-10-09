package approval

import (
	"errors"
)

// ErrWorkerDecisionWrite is returned when pipeline code running as a worker
// attempts to mutate a human approval decision. Workers may create pending
// approval requests and read controller decisions, but only the controller's
// full store is given to authenticated decision routes.
var ErrWorkerDecisionWrite = errors.New("worker application port rejects human approval decision writes")

type workerReadableApprovalStore interface {
	Create(PendingApproval) (PendingApproval, error)
	Load(runID, approvalID string) (PendingApproval, error)
	List(runID string) ([]PendingApproval, error)
}

// WorkerStore is an application-level capability wrapper for pipeline code
// running inside ai-team worker. It does not protect the underlying database
// file from a compromised process with filesystem access; deployment must not
// treat it as OS isolation.
type WorkerStore struct {
	store workerReadableApprovalStore
}

func NewWorkerStore(store workerReadableApprovalStore) *WorkerStore {
	return &WorkerStore{store: store}
}

func (s *WorkerStore) Create(value PendingApproval) (PendingApproval, error) {
	return s.store.Create(value)
}

func (s *WorkerStore) Load(runID, approvalID string) (PendingApproval, error) {
	return s.store.Load(runID, approvalID)
}

func (s *WorkerStore) List(runID string) ([]PendingApproval, error) {
	return s.store.List(runID)
}

func (s *WorkerStore) HasAuthenticatedControllerDecision(value PendingApproval) bool {
	if s == nil {
		return false
	}
	trusted, ok := s.store.(TrustedDecisionAuthority)
	return ok && trusted.HasAuthenticatedControllerDecision(value)
}

func (*WorkerStore) Decide(string, string, Decision) (PendingApproval, error) {
	return PendingApproval{}, ErrWorkerDecisionWrite
}

func (*WorkerStore) ResolveDeferred(string, string, Decision) (PendingApproval, error) {
	return PendingApproval{}, ErrWorkerDecisionWrite
}

var _ interface {
	Create(PendingApproval) (PendingApproval, error)
	Load(string, string) (PendingApproval, error)
	List(string) ([]PendingApproval, error)
	Decide(string, string, Decision) (PendingApproval, error)
	ResolveDeferred(string, string, Decision) (PendingApproval, error)
} = (*WorkerStore)(nil)

var _ TrustedDecisionAuthority = (*WorkerStore)(nil)
