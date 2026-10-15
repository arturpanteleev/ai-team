package humandecision

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

type auditRecord struct {
	eventType string
	data      map[string]any
}

type fakePorts struct {
	mu        sync.Mutex
	store     *approval.Store
	selfErr   error
	auditErr  error
	audits    []auditRecord
	decisions int
}

func (f *fakePorts) Approvals(runID string) ([]approval.PendingApproval, error) {
	return f.store.List(runID)
}

func (f *fakePorts) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	f.mu.Lock()
	f.decisions++
	f.mu.Unlock()
	return f.store.Decide(runID, approvalID, decision)
}

func (f *fakePorts) SelfApprovalViolation(string, approval.PendingApproval, string) error {
	return f.selfErr
}

func (f *fakePorts) PinRevisions(string, approval.PendingApproval) (map[string]string, error) {
	return nil, nil
}

func (f *fakePorts) Audit(_ string, eventType string, _ time.Time, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audits = append(f.audits, auditRecord{eventType, data})
	return nil
}

const runID = "run-1"

var subject = strings.Repeat("a", 64)

func newPending(t *testing.T, quorum string, roles ...string) (*fakePorts, approval.PendingApproval) {
	t.Helper()
	store, err := approval.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Create(approval.PendingApproval{
		Kind: approval.KindApprove, RunID: runID, AttemptID: "attempt-1", FromStage: "product_spec", ToStage: "tech_design",
		Trigger: "graph_outcome:passed", SubjectHash: subject, RequiredRoles: roles, Quorum: quorum,
		Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": "tech_design", "reject": "product_spec"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fakePorts{store: store}, value
}

func actor(id string, roles ...string) Actor {
	return Actor{ID: id, Authenticated: true, HasRole: func(role string) bool {
		for _, held := range roles {
			if held == role {
				return true
			}
		}
		return false
	}}
}

func request(value approval.PendingApproval, who Actor, action string) Request {
	return Request{RunID: runID, ApprovalID: value.ID, Action: action, SubjectHash: subject, Actor: who, Channel: ChannelREST}
}

func TestDecideRecordsDecisionAndAudit(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAny, "product_owner")
	req := request(pending, actor("po-1", "product_owner"), "approve")
	req.RequestKey = "key-1"
	got, err := New(ports).Decide(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Resolved || got.Replayed || got.Role != "product_owner" || got.Approval.ResolvedAction != "approve" {
		t.Fatalf("result = %+v", got)
	}
	if len(ports.audits) != 1 || ports.audits[0].eventType != AuditEvent {
		t.Fatalf("audits = %+v", ports.audits)
	}
	data := ports.audits[0].data
	for key, want := range map[string]any{
		"actor_id": "po-1", "actor_role": "product_owner", "action": "approve", "subject_hash": subject,
		"channel": "rest", "stage": "product_spec", "target": "tech_design", "request_key": "key-1",
		"resolved": true, "hash_confirmed_by_actor": true,
	} {
		if data[key] != want {
			t.Fatalf("audit %s = %v, want %v (%v)", key, data[key], want, data)
		}
	}
}

func TestDecideRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Request, *fakePorts)
		want   error
	}{
		{"unknown approval", func(r *Request, _ *fakePorts) { r.ApprovalID = "nope" }, ErrNotFound},
		{"action not offered", func(r *Request, _ *fakePorts) { r.Action = "return_to_intent" }, ErrActionNotAllowed},
		{"hash missing", func(r *Request, _ *fakePorts) { r.SubjectHash = "" }, ErrHashRequired},
		{"stale hash", func(r *Request, _ *fakePorts) { r.SubjectHash = strings.Repeat("b", 64) }, ErrHashMismatch},
		{"wrong role", func(r *Request, _ *fakePorts) { r.Actor = actor("dev-1", "developer") }, ErrRoleForbidden},
		{"no role check", func(r *Request, _ *fakePorts) { r.Actor.HasRole = nil }, ErrRoleForbidden},
		{"self approval", func(_ *Request, p *fakePorts) { p.selfErr = errors.New("автор этапа") }, ErrSelfApproval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ports, pending := newPending(t, approval.QuorumAny, "product_owner")
			req := request(pending, actor("po-1", "product_owner"), "approve")
			tc.mutate(&req, ports)
			_, err := New(ports).Decide(context.Background(), req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if ports.decisions != 0 || len(ports.audits) != 0 {
				t.Fatalf("a refused decision must store and audit nothing: %d decisions, %d audits", ports.decisions, len(ports.audits))
			}
		})
	}
}

func TestSelfApprovalOnlyAppliesToApproval(t *testing.T) {
	// A person may reject (or return) their own work; only confirming it is
	// forbidden.
	ports, pending := newPending(t, approval.QuorumAny, "product_owner")
	ports.selfErr = errors.New("автор этапа")
	req := request(pending, actor("po-1", "product_owner"), "reject")
	req.Comment = "переделаю"
	if _, err := New(ports).Decide(context.Background(), req); err != nil {
		t.Fatalf("reject of one's own work must be allowed: %v", err)
	}
}

func TestRedeliveryDoesNotDecideTwice(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAny, "product_owner")
	service := New(ports)
	req := request(pending, actor("po-1", "product_owner"), "approve")
	if _, err := service.Decide(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	again, err := service.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("an identical redelivered request must succeed idempotently: %v", err)
	}
	if !again.Replayed || !again.Resolved || ports.decisions != 1 || len(ports.audits) != 2 ||
		ports.audits[0].eventType != AuditEvent || ports.audits[1].eventType != ReplayAuditEvent {
		t.Fatalf("replay = %+v, decisions %d, audits %+v", again, ports.decisions, ports.audits)
	}
	// A different action by the same person on a closed request is refused.
	other := request(pending, actor("po-1", "product_owner"), "reject")
	if _, err := service.Decide(context.Background(), other); !errors.Is(err, ErrNotPending) {
		t.Fatalf("a closed request must refuse another action: %v", err)
	}
}

func TestQuorumAllNeedsDistinctVoters(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAll, "product_owner", "architect")
	service := New(ports)
	first, err := service.Decide(context.Background(), request(pending, actor("po-1", "product_owner"), "approve"))
	if err != nil || first.Resolved {
		t.Fatalf("one of two roles must not resolve the approval: %+v %v", first, err)
	}
	// The covered role cannot be voted again, by anyone.
	if _, err := service.Decide(context.Background(), request(pending, actor("po-2", "product_owner"), "approve")); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("a covered role must not take a second vote: %v", err)
	}
	if _, err := service.Decide(context.Background(), request(pending, actor("po-1", "product_owner"), "reject")); !errors.Is(err, ErrAlreadyVoted) {
		t.Fatalf("the same person must not change a recorded vote: %v", err)
	}
	last, err := service.Decide(context.Background(), request(pending, actor("arch-1", "architect"), "approve"))
	if err != nil || !last.Resolved || last.Role != "architect" {
		t.Fatalf("the second voter completes the quorum in the remaining role: %+v %v", last, err)
	}
}

func TestAuditFailureIsReportedWithTheStoredDecision(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAny, "product_owner")
	ports.auditErr = errors.New("disk full")
	got, err := New(ports).Decide(context.Background(), request(pending, actor("po-1", "product_owner"), "approve"))
	if !errors.Is(err, ErrAuditUnavailable) || !got.Resolved {
		t.Fatalf("the caller must learn that the decision was stored without audit: %+v %v", got, err)
	}
}

func TestDecideRefusesNonTransitionApprovals(t *testing.T) {
	ports, _ := newPending(t, approval.QuorumAny, "product_owner")
	other, err := ports.store.Create(approval.PendingApproval{
		Kind: approval.KindApprove, RunID: runID, AttemptID: "attempt-1", FromStage: "implementation", ToStage: "implementation",
		Trigger: "delivery_plan", SubjectHash: subject, RequiredRoles: []string{"product_owner"}, Quorum: approval.QuorumAny,
		Actions: []string{"approve"}, Targets: map[string]string{"approve": "implementation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := request(other, actor("po-1", "product_owner"), "approve")
	if _, err := New(ports).Decide(context.Background(), req); !errors.Is(err, ErrNotTransition) {
		t.Fatalf("the delivery plan has its own path: %v", err)
	}
}

func TestOneOwnerClosesMultiRoleQuorumAll(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAll, "product_owner", "architect")
	service := New(ports)
	owner := actor("owner", "product_owner", "architect")
	first, err := service.Decide(context.Background(), request(pending, owner, "approve"))
	if err != nil || first.Resolved || first.Replayed {
		t.Fatalf("first call covers one role: %+v %v", first, err)
	}
	second, err := service.Decide(context.Background(), request(pending, owner, "approve"))
	if err != nil || !second.Resolved || second.Replayed || second.Role == first.Role {
		t.Fatalf("second call must cover the other role and close the quorum: %+v %v", second, err)
	}
	if len(second.Approval.Decisions) != 2 {
		t.Fatalf("decisions = %+v", second.Approval.Decisions)
	}
	// All roles covered: the same request is a replay, another action is refused.
	again, err := service.Decide(context.Background(), request(pending, owner, "approve"))
	if err != nil || !again.Replayed || !again.Resolved {
		t.Fatalf("third identical call is a replay: %+v %v", again, err)
	}
	if _, err := service.Decide(context.Background(), request(pending, owner, "reject")); !errors.Is(err, ErrNotPending) {
		t.Fatalf("a closed request refuses another action: %v", err)
	}
}

func TestSameActorOtherRoleIsNeverSilentReplay(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAll, "product_owner", "architect", "qa")
	service := New(ports)
	owner := actor("owner", "product_owner", "architect")
	if _, err := service.Decide(context.Background(), request(pending, owner, "approve")); err != nil {
		t.Fatal(err)
	}
	got, err := service.Decide(context.Background(), request(pending, owner, "approve"))
	if err != nil || got.Replayed || got.Resolved || len(got.Approval.Decisions) != 2 {
		t.Fatalf("second role vote must be recorded, not replayed: %+v %v", got, err)
	}
	// Both of the actor's roles are covered; qa is still missing.
	if _, err := service.Decide(context.Background(), request(pending, owner, "reject")); !errors.Is(err, ErrAlreadyVoted) {
		t.Fatalf("a different action must be ErrAlreadyVoted: %v", err)
	}
	replay, err := service.Decide(context.Background(), request(pending, owner, "approve"))
	if err != nil || !replay.Replayed || replay.Resolved {
		t.Fatalf("redelivery with all own roles covered: %+v %v", replay, err)
	}
}

func TestReplayInOtherClosedRoleIsRefused(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAll, "product_owner", "architect", "qa")
	service := New(ports)
	if _, err := service.Decide(context.Background(), request(pending, actor("ann", "product_owner"), "approve")); err != nil {
		t.Fatal(err)
	}
	// Another person closes the architect seat.
	if _, err := service.Decide(context.Background(), request(pending, actor("bob", "architect"), "approve")); err != nil {
		t.Fatal(err)
	}
	// Ann repeats the request, now claiming the architect role that Bob closed:
	// not a replay of her product_owner decision.
	got, err := service.Decide(context.Background(), request(pending, actor("ann", "architect"), "approve"))
	if !errors.Is(err, ErrAlreadyVoted) || got.Replayed {
		t.Fatalf("other closed role must be refused: %+v %v", got, err)
	}
	// In her own role the same request stays a replay.
	own, err := service.Decide(context.Background(), request(pending, actor("ann", "product_owner"), "approve"))
	if err != nil || !own.Replayed || own.Role != "product_owner" {
		t.Fatalf("own role replay: %+v %v", own, err)
	}
}

func TestParallelIdenticalRequestsAuditOnce(t *testing.T) {
	ports, pending := newPending(t, approval.QuorumAny, "product_owner")
	service := New(ports)
	req := request(pending, actor("po-1", "product_owner"), "approve")
	const n = 8
	var wg sync.WaitGroup
	results := make([]Result, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = service.Decide(context.Background(), req)
		}(i)
	}
	wg.Wait()
	decided, replayed := 0, 0
	for _, a := range ports.audits {
		switch a.eventType {
		case AuditEvent:
			decided++
		case ReplayAuditEvent:
			replayed++
		}
	}
	for i, err := range errs {
		if err != nil || !results[i].Resolved {
			t.Fatalf("request %d: %+v %v", i, results[i], err)
		}
	}
	if decided != 1 || replayed != n-1 {
		t.Fatalf("decided %d, replayed %d (audits %d)", decided, replayed, len(ports.audits))
	}
}
