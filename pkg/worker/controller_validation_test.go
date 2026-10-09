package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

type validationApprovalStore struct {
	values  []approval.PendingApproval
	loadErr error
	listErr error
}

func (s *validationApprovalStore) Create(value approval.PendingApproval) (approval.PendingApproval, error) {
	return value, nil
}

func (s *validationApprovalStore) Load(runID, approvalID string) (approval.PendingApproval, error) {
	if s.loadErr != nil {
		return approval.PendingApproval{}, s.loadErr
	}
	for _, value := range s.values {
		if value.RunID == runID && value.ID == approvalID {
			return value, nil
		}
	}
	return approval.PendingApproval{}, errors.New("approval not found")
}

func (s *validationApprovalStore) List(string) ([]approval.PendingApproval, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]approval.PendingApproval(nil), s.values...), nil
}

func (s *validationApprovalStore) HasAuthenticatedControllerDecision(value approval.PendingApproval) bool {
	return controllerDecisionMarkedAuthenticated(value)
}

func validationApproval(runID, id, attemptID, planHash string) approval.PendingApproval {
	return approval.PendingApproval{
		RunID: runID, ID: id, AttemptID: attemptID, Trigger: "delivery_plan", SubjectHash: planHash,
		Status: approval.StatusResolved, ResolvedAction: "approve",
		FromStage: "deployer", ToStage: "deployer",
	}
}

func validationTrustedApproval(runID, id, attemptID, planHash string) approval.PendingApproval {
	value := validationApproval(runID, id, attemptID, planHash)
	value.Decisions = []approval.Decision{{
		ApprovalID: id, ActorID: "release-manager", ActorRole: "release_manager",
		Action: "approve", SubjectHash: planHash, ControllerAuthenticated: true,
	}}
	return value
}

func TestValidateWorkerDeliveryEventPolicies(t *testing.T) {
	const (
		runID    = "delivery-validator-run"
		attempt  = "attempt-current"
		planHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	target := filepath.Join(string(filepath.Separator), "tmp", "delivery-validator-target")
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	statePath := filepath.Join(target, ".ai-team", "delivery", "feature.json")
	resolved := validationTrustedApproval(runID, "approval-delivery", attempt, planHash)
	untrustedResolved := validationApproval(runID, "approval-delivery", attempt, planHash)
	start := evidence.Event{Type: "attempt_started", AttemptID: attempt}
	approved := evidence.Event{Type: "delivery_plan_approved", AttemptID: attempt, Data: map[string]any{"plan_hash": planHash}}
	deferred := evidence.Event{Type: "delivery_deferred", AttemptID: attempt, Data: map[string]any{"plan_hash": planHash, "state_path": statePath}}
	gate := map[string]any{"approval_id": "gate-1", "subject_hash": "gate-hash", "action": "approve"}
	gateApproval := approval.PendingApproval{ID: "gate-1", SubjectHash: "gate-hash", Status: approval.StatusResolved, ResolvedAction: "approve"}

	cases := []struct {
		name      string
		server    *workerAPIServer
		event     evidence.Event
		prior     []evidence.Event
		runDir    string
		wantError bool
	}{
		{name: "unrelated event", server: &workerAPIServer{}, event: evidence.Event{Type: "attempt_started"}},
		{name: "approved hash flag exact", server: &workerAPIServer{approvedPlanHash: planHash}, event: evidence.Event{Type: "delivery_plan_approved", Data: map[string]any{"mode": "hash_flag", "plan_hash": planHash}}},
		{name: "approved hash flag missing authority", server: &workerAPIServer{}, event: evidence.Event{Type: "delivery_plan_approved", Data: map[string]any{"mode": "hash_flag", "plan_hash": planHash}}, wantError: true},
		{name: "approved hash flag mismatch", server: &workerAPIServer{approvedPlanHash: strings.Repeat("b", 64)}, event: evidence.Event{Type: "delivery_plan_approved", Data: map[string]any{"mode": "hash_flag", "plan_hash": planHash}}, wantError: true},
		{name: "unsupported approval mode", server: &workerAPIServer{}, event: evidence.Event{Type: "delivery_plan_approved", Data: map[string]any{"mode": "worker", "plan_hash": planHash}}, wantError: true},
		{name: "resolved plan approval exact", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{values: []approval.PendingApproval{resolved}}}, event: evidence.Event{Type: "delivery_plan_approved", AttemptID: attempt, Data: map[string]any{"mode": "resolved_approval", "plan_hash": planHash}}},
		{name: "resolved plan approval without provenance", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{values: []approval.PendingApproval{untrustedResolved}}}, event: evidence.Event{Type: "delivery_plan_approved", AttemptID: attempt, Data: map[string]any{"mode": "resolved_approval", "plan_hash": planHash}}, wantError: true},
		{name: "resolved plan approval list failure", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{listErr: errors.New("approval database unavailable")}}, event: evidence.Event{Type: "delivery_plan_approved", AttemptID: attempt, Data: map[string]any{"mode": "resolved_approval", "plan_hash": planHash}}, wantError: true},
		{name: "resolved plan approval wrong attempt", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{values: []approval.PendingApproval{validationApproval(runID, "approval-delivery", "another-attempt", planHash)}}}, event: evidence.Event{Type: "delivery_plan_approved", AttemptID: attempt, Data: map[string]any{"mode": "resolved_approval", "plan_hash": planHash}}, wantError: true},
		{name: "deferred path outside prepared directory", server: &workerAPIServer{}, event: evidence.Event{Type: "delivery_deferred", Data: map[string]any{"state_path": filepath.Join(target, "elsewhere.json")}}, runDir: runDir, wantError: true},
		{name: "deferred attempt missing", server: &workerAPIServer{}, event: deferred, prior: []evidence.Event{{Type: "attempt_started", AttemptID: "other-attempt"}}, runDir: runDir, wantError: true},
		{name: "deferred attempt finished", server: &workerAPIServer{}, event: deferred, prior: []evidence.Event{start, {Type: "attempt_finished", AttemptID: attempt}, approved}, runDir: runDir, wantError: true},
		{name: "deferred attempt abandoned", server: &workerAPIServer{}, event: deferred, prior: []evidence.Event{start, {Type: "attempt_abandoned", AttemptID: attempt}, approved}, runDir: runDir, wantError: true},
		{name: "deferred approval event missing", server: &workerAPIServer{}, event: deferred, prior: []evidence.Event{start}, runDir: runDir, wantError: true},
		{name: "deferred plan hash mismatch", server: &workerAPIServer{}, event: deferred, prior: []evidence.Event{start, {Type: "delivery_plan_approved", AttemptID: attempt, Data: map[string]any{"plan_hash": strings.Repeat("b", 64)}}}, runDir: runDir, wantError: true},
		{name: "deferred controller job hash", server: &workerAPIServer{approvedPlanHash: planHash}, event: deferred, prior: []evidence.Event{start, approved}, runDir: runDir},
		{name: "deferred resolved approval exact", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{values: []approval.PendingApproval{resolved}}}, event: deferred, prior: []evidence.Event{start, approved}, runDir: runDir},
		{name: "deferred resolved approval without provenance", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{values: []approval.PendingApproval{untrustedResolved}}}, event: deferred, prior: []evidence.Event{start, approved}, runDir: runDir, wantError: true},
		{name: "deferred approval list failure", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{listErr: errors.New("approval database unavailable")}}, event: deferred, prior: []evidence.Event{start, approved}, runDir: runDir, wantError: true},
		{name: "deferred approval absent", server: &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &validationApprovalStore{}}, event: deferred, prior: []evidence.Event{start, approved}, runDir: runDir, wantError: true},
		{name: "ratification gates missing", server: &workerAPIServer{}, event: evidence.Event{Type: "deferred_gates_ratified", Data: map[string]any{}}, wantError: true},
		{name: "ratification gates empty", server: &workerAPIServer{}, event: evidence.Event{Type: "deferred_gates_ratified", Data: map[string]any{"gates": []any{}}}, wantError: true},
		{name: "ratification gate malformed", server: &workerAPIServer{approvals: &validationApprovalStore{}}, event: evidence.Event{Type: "deferred_gates_ratified", Data: map[string]any{"gates": []any{"invalid"}}}, wantError: true},
		{name: "ratification approval list failure", server: &workerAPIServer{approvals: &validationApprovalStore{listErr: errors.New("approval database unavailable")}}, event: evidence.Event{Type: "deferred_gates_ratified", Data: map[string]any{"gates": []any{gate}}}, wantError: true},
		{name: "ratification gate mismatch", server: &workerAPIServer{approvals: &validationApprovalStore{}}, event: evidence.Event{Type: "deferred_gates_ratified", Data: map[string]any{"gates": []any{gate}}}, wantError: true},
		{name: "ratification gate exact", server: &workerAPIServer{approvals: &validationApprovalStore{values: []approval.PendingApproval{gateApproval}}}, event: evidence.Event{Type: "deferred_gates_ratified", Data: map[string]any{"gates": []any{gate}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.server.validateWorkerDeliveryEvent(tc.event, tc.prior, tc.runDir)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateWorkerDeliveryEvent() error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestValidateWorkerApprovalEventPolicies(t *testing.T) {
	const runID, approvalID, attemptID = "approval-validator-run", "approval-1", "attempt-1"
	base := approval.PendingApproval{
		RunID: runID, ID: approvalID, AttemptID: attemptID, SubjectHash: "subject-hash",
		Status: approval.StatusPending, ResolvedAction: "approve", FromStage: "reviewer", ToStage: "coder", Trigger: "review",
	}
	event := func(eventType string, values map[string]any) evidence.Event {
		return evidence.Event{Type: eventType, AttemptID: attemptID, Data: values}
	}
	requestData := map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "status": string(approval.StatusPending)}
	resolved := base
	resolved.Status = approval.StatusResolved
	decidedData := map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "status": string(approval.StatusResolved), "resolved_action": "approve"}
	reusedData := map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "prior_status": string(base.Status), "from_stage": base.FromStage, "to_stage": base.ToStage, "trigger": base.Trigger}

	cases := []struct {
		name      string
		value     approval.PendingApproval
		loadErr   error
		event     evidence.Event
		wantError bool
	}{
		{name: "unrelated event bypasses approval store", event: evidence.Event{Type: "stage_started"}},
		{name: "approval id absent", value: base, event: event("approval_requested", map[string]any{}), wantError: true},
		{name: "approval id has wrong type", value: base, event: event("approval_requested", map[string]any{"approval_id": 7}), wantError: true},
		{name: "approval id empty", value: base, event: event("approval_requested", map[string]any{"approval_id": ""}), wantError: true},
		{name: "authority load failure", value: base, loadErr: errors.New("approval database unavailable"), event: event("approval_requested", requestData), wantError: true},
		{name: "foreign run identity", value: func() approval.PendingApproval { v := base; v.RunID = "another-run"; return v }(), event: event("approval_requested", requestData), wantError: true},
		{name: "foreign approval identity", value: func() approval.PendingApproval { v := base; v.ID = "another-approval"; return v }(), event: event("approval_requested", requestData), wantError: true},
		{name: "subject identity mismatch", value: func() approval.PendingApproval { v := base; v.SubjectHash = "other-subject"; return v }(), event: event("approval_requested", requestData), wantError: true},
		{name: "requested pending approval", value: base, event: event("approval_requested", requestData)},
		{name: "request may race with resolved decision", value: resolved, event: event("approval_requested", requestData)},
		{name: "requested attempt mismatch", value: func() approval.PendingApproval { v := base; v.AttemptID = "other-attempt"; return v }(), event: event("approval_requested", requestData), wantError: true},
		{name: "requested status mismatch", value: base, event: event("approval_requested", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "status": "resolved"}), wantError: true},
		{name: "requested controller state is invalid", value: func() approval.PendingApproval { v := base; v.Status = approval.Status("invalid"); return v }(), event: event("approval_requested", requestData), wantError: true},
		{name: "decision matches resolved state", value: resolved, event: event("approval_decided", decidedData)},
		{name: "decision attempt mismatch", value: func() approval.PendingApproval { v := resolved; v.AttemptID = "other-attempt"; return v }(), event: event("approval_decided", decidedData), wantError: true},
		{name: "decision controller status not resolved", value: base, event: event("approval_decided", decidedData), wantError: true},
		{name: "decision event status mismatch", value: resolved, event: event("approval_decided", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "status": "pending", "resolved_action": "approve"}), wantError: true},
		{name: "decision action missing", value: resolved, event: event("approval_decided", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "status": string(approval.StatusResolved)}), wantError: true},
		{name: "decision action mismatch", value: resolved, event: event("approval_decided", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "status": string(approval.StatusResolved), "resolved_action": "reject"}), wantError: true},
		{name: "reused approval matches prior record", value: base, event: event("approval_reused", reusedData)},
		{name: "reused prior status mismatch", value: base, event: event("approval_reused", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "prior_status": "resolved", "from_stage": base.FromStage, "to_stage": base.ToStage, "trigger": base.Trigger}), wantError: true},
		{name: "reused source stage mismatch", value: base, event: event("approval_reused", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "prior_status": string(base.Status), "from_stage": "other", "to_stage": base.ToStage, "trigger": base.Trigger}), wantError: true},
		{name: "reused target stage mismatch", value: base, event: event("approval_reused", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "prior_status": string(base.Status), "from_stage": base.FromStage, "to_stage": "other", "trigger": base.Trigger}), wantError: true},
		{name: "reused trigger mismatch", value: base, event: event("approval_reused", map[string]any{"approval_id": approvalID, "subject_hash": base.SubjectHash, "prior_status": string(base.Status), "from_stage": base.FromStage, "to_stage": base.ToStage, "trigger": "other"}), wantError: true},
		{name: "reused requires current attempt", value: base, event: func() evidence.Event { e := event("approval_reused", reusedData); e.AttemptID = ""; return e }(), wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &validationApprovalStore{values: []approval.PendingApproval{tc.value}, loadErr: tc.loadErr}
			server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: store}
			err := server.validateWorkerApprovalEvent(tc.event)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateWorkerApprovalEvent() error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestValidateWorkerTerminalEventOperationScope(t *testing.T) {
	cases := []struct {
		name      string
		operation Operation
		event     evidence.Event
		wantError bool
	}{
		{name: "unrelated event", operation: OperationStart, event: evidence.Event{Type: "attempt_finished"}},
		{name: "run canceled outside cancel", operation: OperationStart, event: evidence.Event{Type: "run_canceled"}, wantError: true},
		{name: "run canceled by cancel", operation: OperationCancel, event: evidence.Event{Type: "run_canceled"}},
		{name: "canceled finish outside cancel", operation: OperationResume, event: evidence.Event{Type: "run_finished", Data: map[string]any{"status": string(workflow.RunCanceled)}}, wantError: true},
		{name: "cancel operation must end canceled", operation: OperationCancel, event: evidence.Event{Type: "run_finished", Data: map[string]any{"status": string(workflow.RunCompleted)}}, wantError: true},
		{name: "cancel finishes canceled", operation: OperationCancel, event: evidence.Event{Type: "run_finished", Data: map[string]any{"status": string(workflow.RunCanceled)}}},
		{name: "ordinary successful finish", operation: OperationStart, event: evidence.Event{Type: "run_finished", Data: map[string]any{"status": string(workflow.RunCompleted)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &workerAPIServer{scope: workerAPIScope{Operation: tc.operation}}
			err := server.validateWorkerTerminalEvent(tc.event)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateWorkerTerminalEvent() error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestValidateControllerDeliveryClaimsPolicies(t *testing.T) {
	const (
		runID    = "legacy-claims-run"
		attempt  = "attempt-delivery"
		planHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	approved := func(mode, attemptID, hash string) evidence.Event {
		return evidence.Event{Type: "delivery_plan_approved", AttemptID: attemptID, Data: map[string]any{"mode": mode, "plan_hash": hash}}
	}
	deferred := func(attemptID, hash string) evidence.Event {
		return evidence.Event{Type: "delivery_deferred", AttemptID: attemptID, Data: map[string]any{"plan_hash": hash}}
	}
	exact := validationTrustedApproval(runID, "approval-1", attempt, planHash)
	wrong := exact
	wrong.AttemptID = "another-attempt"

	cases := []struct {
		name      string
		job       Job
		events    []evidence.Event
		approvals interface {
			List(string) ([]approval.PendingApproval, error)
		}
		wantError bool
	}{
		{name: "empty journal", job: Job{RunID: runID}},
		{name: "unrelated journal events", job: Job{RunID: runID}, events: []evidence.Event{{Type: "stage_started"}}},
		{name: "approval lacks attempt", job: Job{RunID: runID, ApprovePlanHash: planHash}, events: []evidence.Event{approved("hash_flag", "", planHash)}, wantError: true},
		{name: "approval lacks plan hash", job: Job{RunID: runID}, events: []evidence.Event{approved("hash_flag", attempt, "")}, wantError: true},
		{name: "hash flag exact normalized job argument", job: Job{RunID: runID, ApprovePlanHash: " " + strings.ToUpper(planHash) + " "}, events: []evidence.Event{approved("hash_flag", attempt, planHash)}},
		{name: "hash flag does not match job authority", job: Job{RunID: runID}, events: []evidence.Event{approved("hash_flag", attempt, planHash)}, wantError: true},
		{name: "resolved approval authority unavailable", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, wantError: true},
		{name: "resolved approval list fails", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, approvals: &validationApprovalStore{listErr: errors.New("approval database unavailable")}, wantError: true},
		{name: "resolved approval without authenticated provenance", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, approvals: &validationApprovalStore{values: []approval.PendingApproval{validationApproval(runID, "approval-1", attempt, planHash)}}, wantError: true},
		{name: "resolved approval exact", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, approvals: &validationApprovalStore{values: []approval.PendingApproval{exact}}},
		{name: "resolved approval wrong attempt", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, approvals: &validationApprovalStore{values: []approval.PendingApproval{wrong}}, wantError: true},
		{name: "resolved approval foreign run", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, approvals: &validationApprovalStore{values: []approval.PendingApproval{func() approval.PendingApproval { v := exact; v.RunID = "other-run"; return v }()}}, wantError: true},
		{name: "resolved approval unsupported status", job: Job{RunID: runID}, events: []evidence.Event{approved("resolved_approval", attempt, planHash)}, approvals: &validationApprovalStore{values: []approval.PendingApproval{func() approval.PendingApproval { v := exact; v.Status = approval.StatusPending; return v }()}}, wantError: true},
		{name: "unsupported historical mode", job: Job{RunID: runID}, events: []evidence.Event{approved("worker", attempt, planHash)}, wantError: true},
		{name: "deferred must match preceding attempt approval", job: Job{RunID: runID, ApprovePlanHash: planHash}, events: []evidence.Event{approved("hash_flag", attempt, planHash), deferred("another-attempt", planHash)}, wantError: true},
		{name: "deferred must match preceding plan hash", job: Job{RunID: runID, ApprovePlanHash: planHash}, events: []evidence.Event{approved("hash_flag", attempt, planHash), deferred(attempt, strings.Repeat("b", 64))}, wantError: true},
		{name: "deferred exact hash flag claim", job: Job{RunID: runID, ApprovePlanHash: planHash}, events: []evidence.Event{approved("hash_flag", attempt, planHash), deferred(attempt, planHash)}},
		{name: "deferred missing identity", job: Job{RunID: runID}, events: []evidence.Event{deferred(attempt, planHash)}, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateControllerDeliveryClaims(tc.job, tc.events, tc.approvals)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateControllerDeliveryClaims() error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestValidateWorkerSocketPathConfinement(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "wk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "controller.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close test Unix socket: %v", err)
		}
	}()

	regularFile := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(regularFile, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	cases := []struct {
		name              string
		path              string
		target, home, tmp string
		wantError         bool
	}{
		{name: "valid socket outside writable binds", path: socketPath},
		{name: "empty", path: "", wantError: true},
		{name: "relative", path: "relative.sock", wantError: true},
		{name: "unclean", path: socketPath + string(filepath.Separator) + "..", wantError: true},
		{name: "under target bind", path: socketPath, target: socketDir, wantError: true},
		{name: "under home bind", path: socketPath, home: socketDir, wantError: true},
		{name: "under temporary bind", path: socketPath, tmp: socketDir, wantError: true},
		{name: "under run mask", path: filepath.Join(string(filepath.Separator), "run", "controller.sock"), wantError: true},
		{name: "under dev mask", path: filepath.Join(string(filepath.Separator), "dev", "controller.sock"), wantError: true},
		{name: "missing socket", path: filepath.Join(outside, "missing.sock"), wantError: true},
		{name: "regular file", path: regularFile, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkerSocketPath(tc.path, "controller socket", tc.target, tc.home, tc.tmp)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateWorkerSocketPath() error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestCreateControllerWorkerSocketDirAvoidsWritableBinds(t *testing.T) {
	t.Run("creates a private external directory", func(t *testing.T) {
		target, home, temp := t.TempDir(), t.TempDir(), t.TempDir()
		dir, err := createControllerWorkerSocketDir(target, home, temp)
		if err != nil {
			t.Fatalf("create private worker socket directory: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("socket directory mode info=%v err=%v", info, err)
		}
		for _, root := range []string{target, home, temp} {
			if isWithinPath(filepath.Clean(root), dir) {
				t.Fatalf("socket directory %q is inside writable root %q", dir, root)
			}
		}
	})
	t.Run("fails if every temporary parent is worker writable", func(t *testing.T) {
		if dir, err := createControllerWorkerSocketDir(string(filepath.Separator), "", ""); err == nil {
			_ = os.RemoveAll(dir)
			t.Fatalf("created socket directory %q beneath a bind covering every temporary parent", dir)
		}
	})
}

func TestIsWithinPathUsesPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, parent, path string
		want               bool
	}{
		{name: "empty parent", parent: "", path: "/tmp/file"},
		{name: "empty path", parent: "/tmp/root", path: ""},
		{name: "same directory", parent: "/tmp/root", path: "/tmp/root", want: true},
		{name: "child directory", parent: "/tmp/root", path: "/tmp/root/sub/file", want: true},
		{name: "prefix sibling", parent: "/tmp/root", path: "/tmp/root-other/file"},
		{name: "parent traversal", parent: "/tmp/root/sub", path: "/tmp/root/file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWithinPath(tc.parent, tc.path); got != tc.want {
				t.Fatalf("isWithinPath(%q, %q) = %v, want %v", tc.parent, tc.path, got, tc.want)
			}
		})
	}
}

func TestWorkerAPIRequestBodyLimits(t *testing.T) {
	cases := []struct {
		method string
		size   int
		want   bool
	}{
		{method: "brief.list", size: 0, want: true},
		{method: "brief.list", size: workerAPIMaxBody, want: true},
		{method: "brief.list", size: workerAPIMaxBody + 1},
		{method: "brief.list", size: -1},
		{method: "candidate.evidence.write", size: workerAPIMaxCandidateEvidenceBody, want: true},
		{method: "candidate.evidence.read", size: workerAPIMaxCandidateEvidenceBody, want: true},
		{method: "candidate.evidence.write", size: workerAPIMaxCandidateEvidenceBody + 1},
		{method: "attempt_manifest.write", size: workerAPIMaxAttemptManifestEnvelope, want: true},
		{method: "attempt_manifest.read", size: workerAPIMaxAttemptManifestEnvelope, want: true},
		{method: "attempt_manifest.read", size: workerAPIMaxAttemptManifestEnvelope + 1},
		{method: "event_log.append", size: workerAPIMaxEventAppendEnvelope, want: true},
		{method: "event_log.append", size: workerAPIMaxEventAppendEnvelope + 1},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.method, tc.size), func(t *testing.T) {
			if got := workerAPIRequestWithinLimit(tc.method, tc.size); got != tc.want {
				t.Fatalf("workerAPIRequestWithinLimit(%q, %d) = %v, want %v", tc.method, tc.size, got, tc.want)
			}
		})
	}
}

func TestServeWorkerAPISetupFailuresFailClosed(t *testing.T) {
	target := t.TempDir()
	blockedTarget := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blockedTarget, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedTarget, ".ai-team", "state"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	missingTarget := filepath.Join(t.TempDir(), "does-not-exist")
	cases := []struct {
		name     string
		runID    string
		target   string
		shortKey bool
	}{
		{name: "unsafe run id", runID: "../outside", target: target},
		{name: "short random capability", runID: "serve-short-random", target: target, shortKey: true},
		{name: "unresolvable target", runID: "serve-missing-target", target: missingTarget},
		{name: "brief storage setup failure", runID: "serve-blocked-briefs", target: blockedTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			random := bytes.NewReader(make([]byte, 32))
			if tc.shortKey {
				random = bytes.NewReader([]byte("short"))
			}
			job := Job{RunID: tc.runID, Operation: OperationStart, TargetDir: tc.target}
			server, err := serveWorkerAPI(job, &apiRecorderSpy{}, &validationApprovalStore{}, listener, "", random, "task")
			if err == nil {
				if server != nil {
					server.close()
				}
				t.Fatal("serveWorkerAPI accepted invalid setup fixture")
			}
			if server != nil {
				server.close()
			}
		})
	}
	engine := &ProcessEngine{}
	optionErr := WithLinuxBubblewrapIsolation()(engine)
	if optionErr == nil && !engine.bubblewrap {
		t.Fatal("successful bubblewrap option did not enable worker isolation")
	}
}

func TestServeWorkerAPIControllerStorageFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocked string
	}{
		{name: "event log reservation", blocked: filepath.Join(".ai-team", "state", "events")},
		{name: "usage reservation", blocked: filepath.Join(".ai-team", "state", "usage")},
		{name: "attempt manifest reservation", blocked: filepath.Join(".ai-team", "state", "attempt-manifests")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			const runID = "serve-storage-failure"
			if tc.name != "event log reservation" {
				if err := (evidence.ControllerEventStore{TargetDir: target}).Reserve(runID); err != nil {
					t.Fatalf("reserve empty controller event log for fixture: %v", err)
				}
			}
			blockedPath := filepath.Join(target, tc.blocked)
			if err := os.MkdirAll(filepath.Dir(blockedPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blockedPath, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			job := Job{RunID: runID, Operation: OperationStart, TargetDir: target}
			server, err := serveWorkerAPI(job, &apiRecorderSpy{}, &validationApprovalStore{}, listener,
				filepath.Join(target, "worker.sock"), bytes.NewReader(make([]byte, 32)), "task")
			if err == nil {
				if server != nil {
					server.close()
				}
				t.Fatal("serveWorkerAPI accepted an obstructed controller storage root")
			}
			if server != nil {
				server.close()
			}
		})
	}
}

type validationRoundTripper func(*http.Request) (*http.Response, error)

func (f validationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func eventLogForValidationPages(runID string, pages []workerAPIEventPage, status int) *workerAPIEventLog {
	index := 0
	client := &http.Client{Transport: validationRoundTripper(func(*http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		if status >= http.StatusBadRequest {
			recorder.WriteHeader(status)
			_, _ = recorder.WriteString("controller rejected request")
		} else {
			page := workerAPIEventPage{}
			if index < len(pages) {
				page = pages[index]
			}
			if err := json.NewEncoder(recorder).Encode(page); err != nil {
				return nil, err
			}
		}
		index++
		return recorder.Result(), nil
	})}
	return &workerAPIEventLog{port: &workerAPIPort{
		address: "http://unix", token: "test-token", socketPath: "/tmp/worker-api-test.sock",
		scope: workerAPIScope{RunID: runID}, client: client,
	}}
}

func TestWorkerAPIEventLogReadBytesRejectsMalformedAuthorityPages(t *testing.T) {
	const runID = "event-page-validation"
	cases := []struct {
		name   string
		pages  []workerAPIEventPage
		status int
	}{
		{name: "controller call error", status: http.StatusBadRequest},
		{name: "offset mismatch", pages: []workerAPIEventPage{{Snapshot: "s", Offset: 1, Total: 0}}},
		{name: "negative total", pages: []workerAPIEventPage{{Snapshot: "s", Total: -1}}},
		{name: "oversized total", pages: []workerAPIEventPage{{Snapshot: "s", Total: evidence.MaxEventLogSize + 1}}},
		{name: "data exceeds remaining total", pages: []workerAPIEventPage{{Snapshot: "s", Total: 1, Data: []byte("too long")}}},
		{name: "oversized page", pages: []workerAPIEventPage{{Snapshot: "s", Total: workerAPIEventReadPage + 1, Data: bytes.Repeat([]byte("x"), workerAPIEventReadPage+1)}}},
		{name: "snapshot missing", pages: []workerAPIEventPage{{Offset: 0, Total: 0}}},
		{name: "empty page before end", pages: []workerAPIEventPage{{Snapshot: "s", Total: 1}}},
		{name: "snapshot changes between pages", pages: []workerAPIEventPage{{Snapshot: "s1", Total: 2, Data: []byte("a")}, {Snapshot: "s2", Offset: 1, Total: 2, Data: []byte("b")}}},
		{name: "total changes between pages", pages: []workerAPIEventPage{{Snapshot: "s", Total: 2, Data: []byte("a")}, {Snapshot: "s", Offset: 1, Total: 3, Data: []byte("b")}}},
		{name: "complete but invalid event bytes", pages: []workerAPIEventPage{{Snapshot: "s", Total: 1, Data: []byte("x")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := eventLogForValidationPages(runID, tc.pages, tc.status)
			if _, err := log.ReadBytes(runID); err == nil {
				t.Fatal("ReadBytes accepted invalid controller page sequence")
			}
		})
	}

	if _, err := (*workerAPIEventLog)(nil).ReadBytes(runID); err == nil {
		t.Fatal("nil event log accepted ReadBytes")
	}
	if _, err := (&workerAPIEventLog{}).ReadBytes(runID); err == nil {
		t.Fatal("event log without a controller-capable port accepted ReadBytes")
	}
	unsupported := eventLogForValidationPages(runID, nil, http.StatusOK)
	unsupported.port.address = "http://127.0.0.1:12345"
	if _, err := unsupported.ReadBytes(runID); err == nil {
		t.Fatal("loopback port exposed controller event authority")
	}
	if _, err := eventLogForValidationPages(runID, nil, http.StatusOK).ReadBytes("another-run"); err == nil {
		t.Fatal("ReadBytes accepted a different run identity")
	}
	if _, err := (*workerAPIEventLog)(nil).Read(runID); err == nil {
		t.Fatal("Read did not propagate an unavailable event API")
	}
	if _, err := (*workerAPIEventLog)(nil).Append(runID, evidence.Event{}, 0, ""); err == nil {
		t.Fatal("Append accepted an unavailable event API")
	}
	if _, err := eventLogForValidationPages(runID, nil, http.StatusOK).Append("another-run", evidence.Event{}, 0, ""); err == nil {
		t.Fatal("Append accepted a different run identity")
	}
}

type brokenResponseBody struct{}

func (brokenResponseBody) Read([]byte) (int, error) {
	return 0, errors.New("injected body read failure")
}
func (brokenResponseBody) Close() error { return nil }

func TestWorkerAPIPortCallWithRandomRejectsTransportAndResponseFailures(t *testing.T) {
	newPort := func(roundTrip validationRoundTripper) *workerAPIPort {
		return &workerAPIPort{
			address: "http://unix", token: "test-token", socketPath: "/tmp/worker-api-test.sock",
			scope: workerAPIScope{RunID: "api-call-errors"}, client: &http.Client{Transport: roundTrip},
		}
	}
	response := func(status int, body io.ReadCloser) validationRoundTripper {
		return func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body, Header: make(http.Header), Request: request}, nil
		}
	}
	cases := []struct {
		name string
		port *workerAPIPort
		rnd  io.Reader
		out  any
	}{
		{name: "transport failure", port: newPort(func(*http.Request) (*http.Response, error) { return nil, errors.New("injected transport error") }), rnd: bytes.NewReader(make([]byte, 32))},
		{name: "response body failure", port: newPort(response(http.StatusOK, brokenResponseBody{})), rnd: bytes.NewReader(make([]byte, 32))},
		{name: "response exceeds method limit", port: newPort(response(http.StatusOK, io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), workerAPIMaxBody+1))))), rnd: bytes.NewReader(make([]byte, 32))},
		{name: "non-success status", port: newPort(response(http.StatusConflict, io.NopCloser(strings.NewReader("conflict")))), rnd: bytes.NewReader(make([]byte, 32))},
		{name: "invalid JSON result", port: newPort(response(http.StatusOK, io.NopCloser(strings.NewReader("not-json")))), rnd: bytes.NewReader(make([]byte, 32)), out: new(map[string]any)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.port.callWithRandom("test.method", workerAPICall{}, tc.out, tc.rnd); err == nil {
				t.Fatal("callWithRandom accepted injected transport or response failure")
			}
		})
	}
	if err := newPort(nil).callWithRandom("test.method", workerAPICall{}, nil, bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("callWithRandom accepted an incomplete request nonce")
	}
}

func TestWriteControllerCandidateEvidenceRejectsUnsafeStoredState(t *testing.T) {
	const runID = "candidate-write-validation"
	document := pipeline.CandidateEvidence{
		SchemaVersion: 1, RunID: runID, Purpose: "semantic_code_review",
		WorkspaceSHA256: strings.Repeat("a", 64), ChangedFiles: []pipeline.CandidateFile{},
		Checks: []pipeline.CandidateCheck{}, Attempts: []pipeline.CandidateAttempt{},
	}
	if err := writeControllerCandidateEvidence("", runID, "review-candidate.json", document); err == nil {
		t.Fatal("candidate evidence write accepted an empty controller root")
	}
	invalid := document
	invalid.RunID = "another-run"
	if err := writeControllerCandidateEvidence(t.TempDir(), runID, "review-candidate.json", invalid); err == nil {
		t.Fatal("candidate evidence write accepted a document for another run")
	}

	t.Run("rejects malformed stored document", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, runID, "review-candidate.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeControllerCandidateEvidence(root, runID, "review-candidate.json", document); err == nil {
			t.Fatal("candidate write accepted malformed pre-existing controller state")
		}
	})
	t.Run("rejects invalid stored identity", func(t *testing.T) {
		root := t.TempDir()
		stored := document
		stored.RunID = "another-run"
		data, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, runID, "review-candidate.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeControllerCandidateEvidence(root, runID, "review-candidate.json", document); err == nil {
			t.Fatal("candidate write accepted a stored identity for another run")
		}
	})
	t.Run("rejects symlinked stored document", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, runID, "review-candidate.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "candidate.json")
		if err := os.WriteFile(outside, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := writeControllerCandidateEvidence(root, runID, "review-candidate.json", document); err == nil {
			t.Fatal("candidate write followed a symlinked stored document")
		}
	})
	t.Run("rejects non-directory evidence parent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, runID), []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeControllerCandidateEvidence(root, runID, "review-candidate.json", document); err == nil {
			t.Fatal("candidate write traversed a non-directory run parent")
		}
	})
	t.Run("rejects symlinked evidence root", func(t *testing.T) {
		root := t.TempDir()
		alias := filepath.Join(t.TempDir(), "evidence-alias")
		if err := os.Symlink(root, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := writeControllerCandidateEvidence(alias, runID, "review-candidate.json", document); err == nil {
			t.Fatal("candidate write created controller evidence through a symlinked root")
		}
	})
}

func TestWriteControllerCandidateEvidenceEnforcesPrettyPrintedStorageLimit(t *testing.T) {
	const runID = "candidate-evidence-size-limit"
	files := make([]pipeline.CandidateFile, 150_000)
	for index := range files {
		files[index] = pipeline.CandidateFile{Path: "a", Fingerprint: "x", Mode: "100644"}
	}
	document := pipeline.CandidateEvidence{
		SchemaVersion: 1, RunID: runID, Purpose: "semantic_code_review",
		WorkspaceSHA256: strings.Repeat("a", 64), ChangedFiles: files,
		Checks: []pipeline.CandidateCheck{}, Attempts: []pipeline.CandidateAttempt{},
	}
	root := t.TempDir()
	err := writeControllerCandidateEvidence(root, runID, "review-candidate.json", document)
	if err == nil || !strings.Contains(err.Error(), "candidate evidence exceeds controller storage limit") {
		t.Fatalf("oversized pretty-printed authority record was not rejected at the storage boundary: %v", err)
	}
	path := filepath.Join(root, runID, "review-candidate.json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected candidate evidence left a partial authority record: %v", err)
	}
}

type validationEventLog struct {
	events    []evidence.Event
	readErr   error
	appendErr error
}

func (l *validationEventLog) Read(string) ([]evidence.Event, error) {
	if l.readErr != nil {
		return nil, l.readErr
	}
	return append([]evidence.Event(nil), l.events...), nil
}

func (l *validationEventLog) ReadBytes(string) ([]byte, error) {
	if l.readErr != nil {
		return nil, l.readErr
	}
	return nil, nil
}

func (l *validationEventLog) Append(_ string, event evidence.Event, _ uint64, _ string) (evidence.Event, error) {
	if l.appendErr != nil {
		return evidence.Event{}, l.appendErr
	}
	return event, nil
}

func TestWorkerAPIDispatchEventAndManifestFailures(t *testing.T) {
	const runID = "dispatch-validation-run"
	t.Run("event append operation scope", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: Operation("inspect")}, usageAllowed: true, eventLogs: &validationEventLog{}}
		if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: evidence.Event{Type: "run_started"}}); err == nil {
			t.Fatal("read-only operation appended lifecycle events")
		}
	})
	t.Run("event append requires type", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationStart}, usageAllowed: true, eventLogs: &validationEventLog{}}
		if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID}); err == nil {
			t.Fatal("event append accepted an empty event type")
		}
	})
	t.Run("event read failure", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationStart}, usageAllowed: true,
			eventLogs: &validationEventLog{readErr: errors.New("controller journal unavailable")}}
		if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: evidence.Event{Type: "run_started"}}); err == nil {
			t.Fatal("event append continued after controller journal read failure")
		}
	})
	t.Run("event commit failure", func(t *testing.T) {
		target := t.TempDir()
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationStart, TargetDir: target}, usageAllowed: true,
			eventLogs: &validationEventLog{appendErr: errors.New("controller journal commit failed")}}
		if _, err := server.dispatch("event_log.append", workerAPICall{RunID: runID, Event: evidence.Event{Type: "run_started"}}); err == nil {
			t.Fatal("event append ignored controller journal commit failure")
		}
	})
	t.Run("attempt manifest reservation failure", func(t *testing.T) {
		target := t.TempDir()
		if err := os.MkdirAll(filepath.Join(target, ".ai-team", "state"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, ".ai-team", "state", "attempt-manifests"), []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationStart, TargetDir: target}, usageAllowed: true,
			attemptManifests: evidence.ControllerAttemptManifestStore{TargetDir: target}}
		if _, err := server.dispatch("attempt_manifest.write", workerAPICall{}); err == nil {
			t.Fatal("attempt manifest write ignored a corrupt reservation root")
		}
	})
	t.Run("attempt manifest read target failure", func(t *testing.T) {
		server := &workerAPIServer{scope: workerAPIScope{RunID: runID, Operation: OperationResume, TargetDir: filepath.Join(t.TempDir(), "missing")}, usageAllowed: true}
		if _, err := server.dispatch("attempt_manifest.read", workerAPICall{A: "attempt-1"}); err == nil {
			t.Fatal("attempt manifest read accepted an unresolvable target")
		}
	})
}

func TestWorkerAPIDispatchReturnsControllerAppendFailureForExactRetry(t *testing.T) {
	target := t.TempDir()
	const runID = "dispatch-exact-retry-run"
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UTC()
	local, err := evidence.Start(filepath.Join(target, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: "event-append-retry", TargetDir: target, StartedAt: startedAt,
		ConfigSnapshot: json.RawMessage(`{}`), WorkflowSnapshot: json.RawMessage(`{"schema_version":1,"stages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Append(evidence.Event{Type: "run_started", Timestamp: startedAt}); err != nil {
		t.Fatal(err)
	}
	events, err := evidence.VerifyEventLog(filepath.Join(local.RunDir(), "events.jsonl"), runID)
	if err != nil || len(events) != 1 {
		t.Fatalf("read exact retry fixture: events=%d err=%v", len(events), err)
	}
	retry := events[0]
	retry.Sequence, retry.RunID, retry.SHA256, retry.PreviousSHA256 = 0, "", "", ""
	appendErr := errors.New("controller journal commit failed")
	server := &workerAPIServer{
		scope:        workerAPIScope{RunID: runID, Operation: OperationStart, TargetDir: target},
		usageAllowed: true,
		eventLogs:    &validationEventLog{events: events, appendErr: appendErr},
	}
	_, err = server.dispatch("event_log.append", workerAPICall{
		RunID: runID, Event: retry, ExpectedSequence: 0, ExpectedPreviousSHA256: events[0].PreviousSHA256,
	})
	if !errors.Is(err, appendErr) {
		t.Fatalf("valid idempotent retry did not reach the durable append boundary: %v", err)
	}
}

func TestWorkerAPIHandleRejectsMalformedAndCanceledRequests(t *testing.T) {
	const runID = "handle-validation-run"
	newServer := func() *workerAPIServer {
		return &workerAPIServer{
			scope: workerAPIScope{RunID: runID, Operation: OperationStart}, token: "handle-token",
			recorder: &apiRecorderSpy{}, requestSlots: make(chan struct{}, 1), nonces: make(map[string]time.Time),
		}
	}
	requestBody := func(method string, scope workerAPIScope, payload json.RawMessage) []byte {
		data, err := json.Marshal(workerAPIRequest{workerAPIScope: scope, Method: method, Payload: payload,
			Nonce: strings.Repeat("a", 64), IssuedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	callBody, err := json.Marshal(workerAPICall{})
	if err != nil {
		t.Fatal(err)
	}
	validServer := newServer()
	validBody := requestBody("recorder.run_attached", validServer.scope, callBody)
	largeUnknownServer := newServer()
	largePayload, err := json.Marshal(workerAPICall{A: strings.Repeat("x", workerAPIMaxBody)})
	if err != nil {
		t.Fatal(err)
	}
	largeUnknownBody := requestBody("unknown.large.method", largeUnknownServer.scope, largePayload)
	malformedLargeServer := newServer()
	malformedLarge := bytes.Repeat([]byte("x"), workerAPIMaxBody+1)

	cases := []struct {
		name       string
		server     *workerAPIServer
		method     string
		path       string
		authority  string
		body       io.ReadCloser
		wantStatus int
	}{
		{name: "wrong HTTP method", server: newServer(), method: http.MethodGet, path: "/v1/call", authority: "Bearer handle-token", wantStatus: http.StatusNotFound},
		{name: "wrong route", server: newServer(), method: http.MethodPost, path: "/other", authority: "Bearer handle-token", wantStatus: http.StatusNotFound},
		{name: "missing capability", server: newServer(), method: http.MethodPost, path: "/v1/call", wantStatus: http.StatusUnauthorized},
		{name: "request body read error", server: newServer(), method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", body: brokenResponseBody{}, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "request body exceeds envelope cap", server: newServer(), method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), workerAPIMaxEventAppendEnvelope+1))), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "malformed large request", server: malformedLargeServer, method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", body: io.NopCloser(bytes.NewReader(malformedLarge)), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "large unsupported method", server: largeUnknownServer, method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", body: io.NopCloser(bytes.NewReader(largeUnknownBody)), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "invalid payload", server: newServer(), method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", wantStatus: http.StatusBadRequest,
			body: func() io.ReadCloser {
				server := newServer()
				return io.NopCloser(bytes.NewReader(requestBody("unknown", server.scope, json.RawMessage(`"not an object"`))))
			}()},
		{name: "scope mismatch", server: newServer(), method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", wantStatus: http.StatusForbidden,
			body: func() io.ReadCloser {
				server := newServer()
				scope := server.scope
				scope.Operation = OperationCancel
				return io.NopCloser(bytes.NewReader(requestBody("unknown", scope, callBody)))
			}()},
		{name: "invalid nonce", server: newServer(), method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", wantStatus: http.StatusUnauthorized,
			body: func() io.ReadCloser {
				server := newServer()
				data := requestBody("unknown", server.scope, callBody)
				var request workerAPIRequest
				_ = json.Unmarshal(data, &request)
				request.Nonce = "invalid"
				data, _ = json.Marshal(request)
				return io.NopCloser(bytes.NewReader(data))
			}()},
		{name: "unknown API method", server: newServer(), method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", wantStatus: http.StatusBadRequest,
			body: func() io.ReadCloser {
				server := newServer()
				return io.NopCloser(bytes.NewReader(requestBody("unknown", server.scope, callBody)))
			}()},
		{name: "successful nil result is JSON", server: validServer, method: http.MethodPost, path: "/v1/call", authority: "Bearer handle-token", body: io.NopCloser(bytes.NewReader(validBody)), wantStatus: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.body != nil {
				request.Body = tc.body
			}
			if tc.authority != "" {
				request.Header.Set("Authorization", tc.authority)
			}
			response := httptest.NewRecorder()
			tc.server.handle(response, request)
			if response.Code != tc.wantStatus {
				t.Fatalf("handle status = %d, want %d; body=%s", response.Code, tc.wantStatus, response.Body.String())
			}
			if tc.name == "successful nil result is JSON" && response.Body.String() != "{}\n" {
				t.Fatalf("nil dispatch result body = %q, want empty JSON object", response.Body.String())
			}
		})
	}
	t.Run("canceled request releases saturated request slot", func(t *testing.T) {
		server := newServer()
		server.requestSlots <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest(http.MethodPost, "/v1/call", nil).WithContext(ctx)
		request.Header.Set("Authorization", "Bearer handle-token")
		response := httptest.NewRecorder()
		server.handle(response, request)
		if len(server.requestSlots) != 1 {
			t.Fatal("canceled request changed the active request slot")
		}
	})
}
