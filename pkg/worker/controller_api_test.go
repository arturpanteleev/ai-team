package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

type apiApprovalStore struct {
	values      map[string]approval.PendingApproval
	createErr   error
	loadErr     error
	createCalls int
}

func (s *apiApprovalStore) Create(v approval.PendingApproval) (approval.PendingApproval, error) {
	s.createCalls++
	if s.createErr != nil {
		return approval.PendingApproval{}, s.createErr
	}
	if v.RunID == "" {
		return v, os.ErrInvalid
	}
	s.values[v.RunID+"/"+v.ID] = v
	return v, nil
}

func workerAPIRequestBody(t *testing.T, scope workerAPIScope, method string, call workerAPICall, nonce string, issuedAt time.Time) string {
	t.Helper()
	if nonce == "random" {
		var bytes [32]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			t.Fatal(err)
		}
		nonce = hex.EncodeToString(bytes[:])
	}
	payload, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(workerAPIRequest{workerAPIScope: scope, Method: method, Payload: payload, Nonce: nonce, IssuedAt: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func postWorkerAPIRequest(t *testing.T, server *workerAPIServer, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+server.listener.Addr().String()+"/v1/call", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+server.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestWorkerControllerAPIRejectsExpiredFutureMissingAndReplayedRequests(t *testing.T) {
	job := Job{RunID: "run", Operation: OperationStart, ExecutionID: strings.Repeat("e", ExecutionIDBytes*2)}
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	recorder := &apiRecorderSpy{}
	server, err := startWorkerAPIServer(job, recorder, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	scope := server.scope
	now := time.Now().UTC()
	for _, tc := range []struct {
		name     string
		nonce    string
		issuedAt time.Time
	}{
		{name: "expired", nonce: "random", issuedAt: now.Add(-workerAPIRequestTTL - time.Second)},
		{name: "future", nonce: "random", issuedAt: now.Add(workerAPIFutureSkew + time.Second)},
		{name: "missing nonce", nonce: "", issuedAt: now},
		{name: "malformed hex nonce", nonce: strings.Repeat("g", 64), issuedAt: now},
		{name: "missing timestamp", nonce: "random"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := workerAPIRequestBody(t, scope, "approval.create", workerAPICall{Approval: approval.PendingApproval{ID: tc.name}}, tc.nonce, tc.issuedAt)
			if status := postWorkerAPIRequest(t, server, body); status != http.StatusUnauthorized {
				t.Fatalf("status=%d want=%d", status, http.StatusUnauthorized)
			}
		})
	}
	if store.createCalls != 0 || len(store.values) != 0 || len(recorder.events) != 0 {
		t.Fatalf("invalid requests reached dispatch: create calls=%d approvals=%d events=%v", store.createCalls, len(store.values), recorder.events)
	}

	// The nonce contains hex letters so the replay changes only their case.
	nonce := strings.Repeat("ab", 32)
	body := workerAPIRequestBody(t, scope, "approval.create", workerAPICall{Approval: approval.PendingApproval{ID: "replay"}}, nonce, now)
	if status := postWorkerAPIRequest(t, server, body); status != http.StatusOK {
		t.Fatalf("initial request status=%d", status)
	}
	caseVariantBody := workerAPIRequestBody(t, scope, "approval.create", workerAPICall{Approval: approval.PendingApproval{ID: "replay"}}, strings.ToUpper(nonce), now)
	if status := postWorkerAPIRequest(t, server, caseVariantBody); status != http.StatusUnauthorized {
		t.Fatalf("case-variant replay status=%d want=%d", status, http.StatusUnauthorized)
	}
	if store.createCalls != 1 || len(store.values) != 1 || recorder.decisions != 0 {
		t.Fatalf("replayed request duplicated state: create calls=%d approvals=%d decisions=%d", store.createCalls, len(store.values), recorder.decisions)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("approval replay unexpectedly changed recorder state: %v", recorder.events)
	}
}

func TestWorkerControllerAPIConcurrentReplayDispatchesOnce(t *testing.T) {
	job := Job{RunID: "run", Operation: OperationStart, ExecutionID: strings.Repeat("c", ExecutionIDBytes*2)}
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	recorder := &apiRecorderSpy{}
	server, err := startWorkerAPIServer(job, recorder, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()

	const requests = 24
	now := time.Now().UTC()
	body := workerAPIRequestBody(t, server.scope, "approval.create", workerAPICall{Approval: approval.PendingApproval{ID: "concurrent-replay"}}, strings.Repeat("cd", 32), now)
	start := make(chan struct{})
	statuses := make(chan int, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, reqErr := http.NewRequest(http.MethodPost, "http://"+server.listener.Addr().String()+"/v1/call", strings.NewReader(body))
			if reqErr != nil {
				errs <- reqErr
				return
			}
			req.Header.Set("Authorization", "Bearer "+server.token)
			resp, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				errs <- doErr
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	close(errs)
	for reqErr := range errs {
		t.Errorf("concurrent request failed: %v", reqErr)
	}

	accepted, rejected := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			accepted++
		case http.StatusUnauthorized:
			rejected++
		default:
			t.Errorf("unexpected concurrent replay status %d", status)
		}
	}
	if accepted != 1 || rejected != requests-1 {
		t.Fatalf("concurrent replay statuses: accepted=%d rejected=%d; want 1 and %d", accepted, rejected, requests-1)
	}
	if store.createCalls != 1 || len(store.values) != 1 {
		t.Fatalf("concurrent replay duplicated approval side effect: create calls=%d approvals=%d", store.createCalls, len(store.values))
	}
}

func TestWorkerControllerAPINonceCacheIsBoundedAndFailsClosed(t *testing.T) {
	server := &workerAPIServer{nonces: make(map[string]time.Time)}
	now := time.Now().UTC()
	for i := range workerAPIMaxNonces {
		var raw [32]byte
		raw[28] = byte(i >> 24)
		raw[29] = byte(i >> 16)
		raw[30] = byte(i >> 8)
		raw[31] = byte(i)
		if !server.acceptRequest(now, hex.EncodeToString(raw[:]), now) {
			t.Fatalf("request %d unexpectedly rejected before capacity", i)
		}
	}
	if len(server.nonces) != workerAPIMaxNonces {
		t.Fatalf("nonce state grew to %d, max=%d", len(server.nonces), workerAPIMaxNonces)
	}
	var extra [32]byte
	extra[0] = 0xff
	if server.acceptRequest(now, hex.EncodeToString(extra[:]), now) {
		t.Fatal("request beyond nonce capacity was accepted")
	}
	refreshed := now.Add(workerAPIRequestTTL + time.Second)
	if !server.acceptRequest(refreshed, hex.EncodeToString(extra[:]), refreshed) {
		t.Fatal("valid request was rejected after expired nonce entries could be reclaimed")
	}
	if len(server.nonces) != 1 {
		t.Fatalf("expired nonce entries were not reclaimed: %d active entries", len(server.nonces))
	}
}
func (s *apiApprovalStore) Load(run, id string) (approval.PendingApproval, error) {
	if s.loadErr != nil {
		return approval.PendingApproval{}, s.loadErr
	}
	v, ok := s.values[run+"/"+id]
	if !ok {
		return v, os.ErrNotExist
	}
	return v, nil
}
func (s *apiApprovalStore) List(run string) ([]approval.PendingApproval, error) {
	var out []approval.PendingApproval
	for _, v := range s.values {
		if v.RunID == run {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *apiApprovalStore) Decide(string, string, approval.Decision) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, approval.ErrWorkerDecisionWrite
}
func (s *apiApprovalStore) ResolveDeferred(string, string, approval.Decision) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, approval.ErrWorkerDecisionWrite
}

func workerQuestionApproval(runID, approvalID, questions, answer string, status approval.Status) approval.PendingApproval {
	payload, _ := json.Marshal(workerQuestionPayload{Kind: "questions", Markdown: questions})
	value := approval.PendingApproval{
		SchemaVersion: approval.SchemaVersion, ID: approvalID, RunID: runID,
		AttemptID: "attempt-analyst", FromStage: "analyst", ToStage: "analyst",
		Trigger: "graph_outcome:blocked", SubjectHash: strings.Repeat("a", 64),
		RequiredRoles: []string{"product_owner"}, Quorum: approval.QuorumAny,
		Actions: []string{"answer_questions", "stop"},
		Targets: map[string]string{"answer_questions": "analyst", "stop": "$stop"},
		Status:  status, Payload: payload,
	}
	if status == approval.StatusResolved {
		now := time.Now().UTC()
		value.ResolvedAction = "answer_questions"
		value.ResolvedAt = now
		value.Decisions = []approval.Decision{{
			ApprovalID: approvalID, ActorID: "owner@example.com", ActorRole: "product_owner",
			Action: "answer_questions", Comment: answer, SubjectHash: value.SubjectHash, DecidedAt: now,
		}}
	}
	return value
}

type apiRecorderSpy struct {
	pipeline.Recorder
	finished  string
	decisions int
	events    []string
}

func (s *apiRecorderSpy) RunFinished(_, status string, _ time.Time) { s.finished = status }
func (s *apiRecorderSpy) ApprovalDecided(string, string, string, time.Time, map[string]any) {
	s.decisions++
}
func (s *apiRecorderSpy) ReconcileInterrupted(time.Time) { s.events = append(s.events, "reconcile") }
func (s *apiRecorderSpy) RunStarted(string, string, string, time.Time) {
	s.events = append(s.events, "run_started")
}
func (s *apiRecorderSpy) RunResumed(string, time.Time) { s.events = append(s.events, "run_resumed") }
func (s *apiRecorderSpy) RunAttached(string)           { s.events = append(s.events, "run_attached") }
func (s *apiRecorderSpy) RunPaused(string, string, time.Time) {
	s.events = append(s.events, "run_paused")
}
func (s *apiRecorderSpy) RunCanceled(string, time.Time) { s.events = append(s.events, "run_canceled") }
func (s *apiRecorderSpy) ApprovalRequested(string, string, string, time.Time, map[string]any) {
	s.events = append(s.events, "approval_requested")
}
func (s *apiRecorderSpy) TransitionSelected(string, string, time.Time, map[string]any) {
	s.events = append(s.events, "transition_selected")
}
func (s *apiRecorderSpy) StageStarted(string, string, string, int, time.Time) {
	s.events = append(s.events, "stage_started")
}
func (s *apiRecorderSpy) StageFinished(notifier.StageResult) {
	s.events = append(s.events, "stage_finished")
}
func (s *apiRecorderSpy) AttemptsInvalidated(string, []string, time.Time) {
	s.events = append(s.events, "attempts_invalidated")
}

func TestWorkerControllerAPIIsInvocationScopedAndCannotDecide(t *testing.T) {
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationResume, RunID: "run-active", ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	recorder := &apiRecorderSpy{}
	server, err := startWorkerAPIServer(job, recorder, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	workerApprovals := NewWorkerAPIApprovals(port).(*workerAPIApprovals)
	created, err := workerApprovals.Create(approval.PendingApproval{ID: "approval-1", Status: approval.StatusPending})
	if err != nil || created.RunID != "run-active" {
		t.Fatalf("scoped approval create: %+v, %v", created, err)
	}
	workerRecorder := NewWorkerAPIRecorder(port).(*workerAPIRecorder)
	at := time.Now()
	workerRecorder.ReconcileInterrupted(at)
	workerRecorder.RunStarted("run-active", "feature", "snapshot", at)
	workerRecorder.RunResumed("run-active", at)
	workerRecorder.RunAttached("run-active")
	workerRecorder.RunPaused("run-active", "waiting", at)
	workerRecorder.RunCanceled("run-active", at)
	workerRecorder.ApprovalRequested("run-active", "approval-1", "attempt-1", at, map[string]any{"kind": "plan"})
	workerRecorder.ApprovalDecided("run-active", "approval-1", "attempt-1", at, nil)
	workerRecorder.TransitionSelected("run-active", "attempt-1", at, map[string]any{"transition": "review"})
	workerRecorder.StageStarted("run-active", "attempt-1", "coder", 1, at)
	workerRecorder.StageFinished(notifier.StageResult{RunID: "run-active", AttemptID: "attempt-1", Err: errors.New("reported")})
	workerRecorder.AttemptsInvalidated("run-active", []string{"attempt-1"}, at)
	workerRecorder.RunFinished("run-active", "completed", at)
	if recorder.finished != "completed" {
		t.Fatalf("recorder event not relayed: %q", recorder.finished)
	}
	if len(recorder.events) != 10 {
		t.Fatalf("allowed recorder calls relayed %v", recorder.events)
	}

	for _, tc := range []struct {
		name, method string
		scope        workerAPIScope
		call         workerAPICall
	}{
		{name: "cross-run scope", method: "approval.list", scope: workerAPIScope{RunID: "other-run", Operation: job.Operation, ExecutionID: job.ExecutionID}},
		{name: "forged decision", method: "approval.decide", scope: server.scope, call: workerAPICall{RunID: "run-active", A: "approval-1", B: "approve"}},
		{name: "forged decision event", method: "recorder.approval_decided", scope: server.scope, call: workerAPICall{RunID: "run-active", A: "approval-1", B: "attempt-1", Data: map[string]any{"action": "approve"}}},
		{name: "other-run create", method: "approval.create", scope: server.scope, call: workerAPICall{Approval: approval.PendingApproval{ID: "forged", RunID: "other-run"}}},
		{name: "admin method", method: "admin.delete_user", scope: server.scope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.scope.RunID == "" {
				tc.scope = server.scope
			}
			body := workerAPIRequestBody(t, tc.scope, tc.method, tc.call, "random", time.Now().UTC())
			resp, err := port.client.Post(port.address+"/v1/call", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == 200 {
				t.Fatalf("hostile request %s unexpectedly accepted", tc.name)
			}
		})
	}
	decision, err := store.Load("run-active", "approval-1")
	if err != nil || decision.Status != approval.StatusPending || len(decision.Decisions) != 0 {
		t.Fatalf("worker changed approval decision: %+v %v", decision, err)
	}
	if recorder.decisions != 0 {
		t.Fatalf("worker forged an approval_decided event %d times", recorder.decisions)
	}
	if got, err := workerApprovals.Load("run-active", "approval-1"); err != nil || got.Status != approval.StatusPending {
		t.Fatalf("approval load: %+v, %v", got, err)
	}
	if got, err := workerApprovals.List("run-active"); err != nil || len(got) != 1 {
		t.Fatalf("approval list: %+v, %v", got, err)
	}
	if _, err := workerApprovals.Load("run-active", "missing"); err == nil {
		t.Fatal("missing approval should report an error")
	}
	if _, err := workerApprovals.Decide("run-active", "approval-1", approval.Decision{}); !errors.Is(err, approval.ErrWorkerDecisionWrite) {
		t.Fatalf("decision adapter allowed write: %v", err)
	}
	if _, err := workerApprovals.ResolveDeferred("run-active", "approval-1", approval.Decision{}); !errors.Is(err, approval.ErrWorkerDecisionWrite) {
		t.Fatalf("deferred decision adapter allowed write: %v", err)
	}
}

func TestWorkerUsageEnvelopeAPIIsWriteOnlyAndRunScoped(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "usage-api-run", TargetDir: target, ExecutionID: strings.Repeat("b", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	envelope := metrics.Build(job.RunID, "feature", started, started.Add(time.Second), nil, 0, "completed", metrics.Usage{})
	writer := NewWorkerAPIUsageEnvelopeWriter(port)
	if port.SupportsControllerUsageStore() {
		t.Fatal("loopback worker API must not establish controller usage authority")
	}
	if err := writer.WriteUsageEnvelope(envelope); err == nil {
		t.Fatal("unmasked worker must not publish authoritative controller usage")
	}
	if reserved, err := metrics.UsageEnvelopeReservation(target, job.RunID); err != nil || reserved {
		t.Fatalf("unmasked API must not create per-run controller reservation: reserved=%v err=%v", reserved, err)
	}
	if _, err := server.dispatch("usage.envelope.read", workerAPICall{A: job.RunID}); err == nil {
		t.Fatal("worker API must not expose a usage read operation")
	}
	wrongRun := envelope
	wrongRun.RunID = "other-run"
	if _, err := server.dispatch("usage.envelope.write", workerAPICall{Usage: wrongRun}); err == nil {
		t.Fatal("worker must not write usage for another run")
	}
	cancelJob := job
	cancelJob.Operation = OperationCancel
	cancelServer, err := startWorkerAPIServer(cancelJob, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer cancelServer.close()
	if _, err := cancelServer.dispatch("usage.envelope.write", workerAPICall{Usage: envelope}); err == nil {
		t.Fatal("cancel invocation must not publish a terminal usage envelope")
	}
}

func TestWorkerTerminalDeliveryRecordUsesScopedUnixAPI(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "terminal-api-run", TargetDir: target, ExecutionID: strings.Repeat("b", ExecutionIDBytes*2)}
	socketDir, err := os.MkdirTemp("/tmp", "api-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	socket := filepath.Join(socketDir, "controller.sock")
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}}, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://unix")
	t.Setenv(WorkerAPISocketEnv, socket)
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	if !port.SupportsControllerUsageStore() {
		t.Fatal("Unix scoped API should be accepted")
	}
	record := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: job.RunID, Feature: "feat", PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Now().UTC()}
	if err := NewWorkerAPITerminalRecordWriter(port).WriteTerminalRecord(record); err != nil {
		t.Fatalf("write over API: %v", err)
	}
	if _, ok, err := delivery.ReadControllerTerminalRecord(target, job.RunID); err != nil || !ok {
		t.Fatalf("controller record missing: found=%v err=%v", ok, err)
	}
	wrong := record
	wrong.RunID = "another-run"
	if _, err := server.dispatch("delivery.terminal_record.write", workerAPICall{TerminalRecord: wrong}); err == nil {
		t.Fatal("API accepted a record for another run")
	}
	conflict := record
	conflict.CommitSHA = strings.Repeat("b", 40)
	if err := NewWorkerAPITerminalRecordWriter(port).WriteTerminalRecord(conflict); err == nil {
		t.Fatal("API accepted conflicting overwrite")
	}
}

func TestWorkerAttestationUsesScopedUnixAPI(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "attest-api-run", TargetDir: target, ExecutionID: strings.Repeat("e", ExecutionIDBytes*2)}
	socketDir, err := os.MkdirTemp("/tmp", "attest-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("remove temporary worker API socket directory: %v", err)
		}
	})
	socket := filepath.Join(socketDir, "controller.sock")
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}}, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://unix")
	t.Setenv(WorkerAPISocketEnv, socket)
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	statement := &attest.Statement{Type: attest.StatementType, PredicateType: attest.PredicateTypeV1, Predicate: attest.Predicate{SchemaVersion: attest.PredicateSchemaVersion, RunID: job.RunID}}
	writer := NewWorkerAPIAttestationWriter(port)
	if err := writer.WriteAttestation(statement); err != nil {
		t.Fatalf("write attestation: %v", err)
	}
	if err := writer.WriteAttestation(statement); err != nil {
		t.Fatalf("idempotent write: %v", err)
	}
	if _, err := server.attestations.Read(job.RunID); err != nil {
		t.Fatalf("controller record missing: %v", err)
	}
	wrong := *statement
	wrong.Predicate.RunID = "other-run"
	if err := writer.WriteAttestation(&wrong); err == nil {
		t.Fatal("worker writer accepted other run")
	}
	if _, err := server.dispatch("attestation.write", workerAPICall{Attestation: wrong}); err == nil {
		t.Fatal("controller API accepted another run's statement")
	}
	invalid := *statement
	invalid.Predicate.SchemaVersion = 999
	if _, err := server.dispatch("attestation.write", workerAPICall{Attestation: invalid}); err == nil {
		t.Fatal("API accepted invalid schema")
	}
}

func TestWorkerContainmentReceiptUsesScopedUnixAPI(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "containment-api-run", TargetDir: target, ExecutionID: strings.Repeat("e", ExecutionIDBytes*2)}
	socketDir, err := os.MkdirTemp("/tmp", "containment-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("cleanup controller socket directory: %v", err)
		}
	})
	socket := filepath.Join(socketDir, "controller.sock")
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}}, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://unix")
	t.Setenv(WorkerAPISocketEnv, socket)
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	receipt := containment.DefaultTrustedLocalReceipt()
	writer := NewWorkerAPIContainmentReceiptWriter(port)
	if err := writer.WriteContainmentReceipt(receipt); err != nil {
		t.Fatalf("write containment receipt: %v", err)
	}
	if err := writer.WriteContainmentReceipt(receipt); err != nil {
		t.Fatalf("idempotent containment write: %v", err)
	}
	stored, err := server.containmentReceipts.Read(job.RunID)
	if err != nil || !stored.IsTrustedLocal() {
		t.Fatalf("controller receipt=%+v err=%v", stored, err)
	}
	invalid := containment.Receipt{Profile: "invalid"}
	if err := writer.WriteContainmentReceipt(invalid); err == nil {
		t.Fatal("writer accepted invalid receipt")
	}
	server.usageAllowed = false
	if _, err := server.dispatch("containment.write", workerAPICall{ContainmentReceipt: &receipt}); err == nil {
		t.Fatal("controller accepted write without Unix transport")
	}
	server.usageAllowed = true
	server.scope.Operation = OperationCancel
	if _, err := server.dispatch("containment.write", workerAPICall{ContainmentReceipt: &receipt}); err == nil {
		t.Fatal("controller accepted containment write for Cancel operation")
	}
	server.scope.Operation = OperationStart
	if _, err := server.dispatch("containment.write", workerAPICall{}); err == nil {
		t.Fatal("controller accepted a missing containment receipt")
	}
}

func TestWorkerContainmentReceiptWriterRejectsUnavailableTransport(t *testing.T) {
	receipt := containment.DefaultTrustedLocalReceipt()
	for _, writer := range []*workerAPIContainmentReceiptWriter{
		nil,
		{},
		{port: &workerAPIPort{address: "http://127.0.0.1:1234"}},
	} {
		if err := writer.WriteContainmentReceipt(receipt); err == nil {
			t.Fatal("WriteContainmentReceipt() unexpectedly succeeded without a Unix API")
		}
	}
}

func TestWorkerAttestationWriterRejectsUnavailableOrMismatchedScope(t *testing.T) {
	statement := &attest.Statement{Predicate: attest.Predicate{RunID: "attest-api-run"}}
	tests := []struct {
		name   string
		writer *workerAPIAttestationWriter
	}{
		{name: "nil writer"},
		{name: "nil port", writer: &workerAPIAttestationWriter{}},
		{name: "non Unix API", writer: &workerAPIAttestationWriter{port: &workerAPIPort{address: "http://127.0.0.1:1234", scope: workerAPIScope{RunID: statement.Predicate.RunID}}}},
		{name: "run mismatch", writer: &workerAPIAttestationWriter{port: &workerAPIPort{address: "http://unix", scope: workerAPIScope{RunID: "different-run"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.writer.WriteAttestation(statement); err == nil {
				t.Fatal("WriteAttestation() unexpectedly succeeded")
			}
		})
	}
	if err := (&workerAPIAttestationWriter{port: &workerAPIPort{address: "http://unix", scope: workerAPIScope{RunID: statement.Predicate.RunID}}}).WriteAttestation(nil); err == nil {
		t.Fatal("WriteAttestation() accepted a nil statement")
	}
}

func TestWorkerAttestationDispatchRejectsUnavailableTransportAndOperation(t *testing.T) {
	call := workerAPICall{Attestation: attest.Statement{Predicate: attest.Predicate{RunID: "attest-dispatch-run"}}}
	tests := []struct {
		name   string
		server *workerAPIServer
		want   string
	}{
		{
			name:   "non Unix transport",
			server: &workerAPIServer{scope: workerAPIScope{RunID: call.Attestation.Predicate.RunID, Operation: OperationStart}},
			want:   "require bubblewrap Unix transport",
		},
		{
			name: "missing controller store",
			server: &workerAPIServer{
				scope:        workerAPIScope{RunID: call.Attestation.Predicate.RunID, Operation: OperationStart},
				usageAllowed: true,
			},
			want: "require bubblewrap Unix transport",
		},
		{
			name: "disallowed operation",
			server: &workerAPIServer{
				scope:        workerAPIScope{RunID: call.Attestation.Predicate.RunID, Operation: OperationCancel},
				usageAllowed: true,
				attestations: attest.ControllerStore{TargetDir: t.TempDir()},
			},
			want: "not allowed for operation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.server.dispatch("attestation.write", call); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("dispatch error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestWorkerTerminalDeliveryRecordWriterRejectsUnavailableOrMismatchedScope(t *testing.T) {
	record := delivery.TerminalRecord{RunID: "terminal-api-run"}
	tests := []struct {
		name   string
		writer *workerAPITerminalRecordWriter
	}{
		{name: "nil writer"},
		{name: "nil port", writer: &workerAPITerminalRecordWriter{}},
		{name: "non Unix API", writer: &workerAPITerminalRecordWriter{port: &workerAPIPort{address: "http://127.0.0.1:1234"}}},
		{name: "run mismatch", writer: &workerAPITerminalRecordWriter{port: &workerAPIPort{address: "http://unix", scope: workerAPIScope{RunID: "different-run"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.writer.WriteTerminalRecord(record); err == nil {
				t.Fatal("WriteTerminalRecord unexpectedly succeeded")
			}
		})
	}
}

func TestWorkerTerminalDeliveryRecordDispatchRejectsUntrustedWrites(t *testing.T) {
	target := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "term-api-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "terminal-dispatch-run", TargetDir: target,
		ExecutionID: strings.Repeat("d", ExecutionIDBytes*2)}
	record := delivery.TerminalRecord{SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: job.RunID,
		Feature: "feat", PlanHash: strings.Repeat("c", 64), CommitSHA: strings.Repeat("a", 40), PerformedAt: time.Now().UTC()}

	loopback, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer loopback.close()
	if _, err := loopback.dispatch("delivery.terminal_record.write", workerAPICall{TerminalRecord: record}); err == nil {
		t.Fatal("loopback API accepted controller-owned delivery write")
	}

	socket := filepath.Join(socketDir, "cancel.sock")
	cancelJob := job
	cancelJob.Operation = OperationCancel
	cancelAPI, err := startWorkerAPIServerUnix(cancelJob, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}}, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelAPI.close()
	if _, err := cancelAPI.dispatch("delivery.terminal_record.write", workerAPICall{TerminalRecord: record}); err == nil {
		t.Fatal("cancel API accepted terminal delivery write")
	}

	unixJob := job
	unixJob.Operation = OperationStart
	unixAPI, err := startWorkerAPIServerUnix(unixJob, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}}, filepath.Join(socketDir, "start.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unixAPI.close()
	invalid := record
	invalid.SchemaVersion = -1
	if _, err := unixAPI.dispatch("delivery.terminal_record.write", workerAPICall{TerminalRecord: invalid}); err == nil {
		t.Fatal("Unix API accepted invalid terminal delivery record")
	}

	fileTarget, err := os.CreateTemp(target, "not-a-directory-")
	if err != nil {
		t.Fatal(err)
	}
	fileTargetPath := fileTarget.Name()
	if err := fileTarget.Close(); err != nil {
		t.Fatal(err)
	}
	unixAPI.scope.TargetDir = fileTargetPath
	if _, err := unixAPI.dispatch("delivery.terminal_record.write", workerAPICall{TerminalRecord: record}); err == nil {
		t.Fatal("Unix API accepted a non-directory target")
	}
}

func TestProcessEngineBubblewrapBuilderFailureStopsBeforeSpawn(t *testing.T) {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("Linux database-path assertion requires bubblewrap on PATH")
		}
	}
	target := t.TempDir()
	engine, err := NewProcessEngine(
		[]string{os.Args[0]}, target, filepath.Join(target, "missing-db-parent", "controller.db"),
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Execute through the controller API and egress setup, then exercise the
	// bubblewrap builder's failure path before the worker process can spawn.
	engine.bubblewrap = true
	_, err = engine.Cancel(pipeline.CancelConfig{RunID: "bubblewrap-build-failure", TargetDir: target})
	if runtime.GOOS == "linux" {
		if err == nil || !strings.Contains(err.Error(), "controller database path") {
			t.Fatalf("missing controller database parent must fail during bubblewrap construction before spawn, got %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "Linux bubblewrap worker isolation is only supported on Linux") {
		t.Fatalf("unsupported-platform bubblewrap builder failure was not propagated before spawn, got %v", err)
	}
}

func TestWorkerUsageEnvelopeDispatchRejectsInvalidAuthorityAndInput(t *testing.T) {
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationCancel, RunID: "usage-dispatch-run", TargetDir: target,
		ExecutionID: strings.Repeat("f", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	server.usageAllowed = true // Exercise the operation gate after transport admission.
	valid := metrics.Build(job.RunID, "feature", time.Now().UTC(), time.Now().UTC().Add(time.Second), nil, 0, "completed", metrics.Usage{})
	if _, err := server.dispatch("usage.envelope.write", workerAPICall{Usage: valid}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("cancel operation should be denied after transport admission, got %v", err)
	}

	job.Operation = OperationStart
	server.scope.Operation = job.Operation
	if _, err := server.dispatch("usage.envelope.write", workerAPICall{}); err == nil {
		t.Fatal("malformed usage envelope was accepted")
	}

	server.scope.TargetDir = filepath.Join(t.TempDir(), "missing-target")
	if _, err := server.dispatch("usage.envelope.write", workerAPICall{Usage: valid}); err == nil {
		t.Fatal("usage write accepted a target that does not exist")
	}
}

func TestWorkerUsageEnvelopeWriterRejectsUnavailableAndInvalidCalls(t *testing.T) {
	started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	valid := metrics.Build("usage-writer-run", "feature", started, started.Add(time.Second), nil, 0, "completed", metrics.Usage{})
	var nilWriter *workerAPIUsageEnvelopeWriter
	if err := nilWriter.WriteUsageEnvelope(valid); err == nil {
		t.Fatal("nil usage writer should fail closed")
	}
	if err := (&workerAPIUsageEnvelopeWriter{}).WriteUsageEnvelope(valid); err == nil {
		t.Fatal("usage writer without an API port should fail closed")
	}

	port := &workerAPIPort{scope: workerAPIScope{RunID: valid.RunID}}
	writer := NewWorkerAPIUsageEnvelopeWriter(port)
	if err := writer.WriteUsageEnvelope(metrics.UsageEnvelope{}); err == nil {
		t.Fatal("writer accepted an invalid usage envelope")
	}
}

func TestWorkerUsageEnvelopeAPIRequiresDurableStoreWriteForSuccess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preseed bool
	}{
		{name: "existing run cannot be rewritten", preseed: true},
		{name: "retry requires stored envelope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "usage-store-run", TargetDir: target,
				ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
			started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
			initial := metrics.Build(job.RunID, "feature", started, started.Add(time.Second), nil, 0, "completed", metrics.Usage{})
			store := metrics.FileUsageEnvelopeStore{}
			if err := store.Reserve(target, job.RunID); err != nil {
				t.Fatal(err)
			}
			if tc.preseed {
				if err := store.Write(target, job.RunID, initial); err != nil {
					t.Fatal(err)
				}
			}
			server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
			if err != nil {
				t.Fatal(err)
			}
			defer server.close()
			server.usageAllowed = true
			if tc.preseed {
				updated := initial
				updated.Outcome = "rewritten"
				if _, err := server.dispatch("usage.envelope.write", workerAPICall{Usage: updated}); err == nil {
					t.Fatal("start invocation rewrote previously persisted usage")
				}
				return
			}

			if _, err := server.dispatch("usage.envelope.write", workerAPICall{Usage: initial}); err != nil {
				t.Fatalf("initial envelope write: %v", err)
			}
			path, err := metrics.UsageEnvelopePath(target, job.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := server.dispatch("usage.envelope.write", workerAPICall{Usage: initial}); err == nil {
				t.Fatal("exact retry succeeded after its persisted state disappeared")
			}
		})
	}
}

func TestWorkerUsageEnvelopeUnixAdmissionCleansUpOnStoreErrors(t *testing.T) {
	approvals := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	for _, tc := range []struct {
		name               string
		job                Job
		corruptReservation bool
	}{
		{name: "invalid run identity", job: Job{RunID: "../invalid", TargetDir: t.TempDir()}},
		{name: "target unavailable", job: Job{RunID: "usage-target-error", TargetDir: filepath.Join(t.TempDir(), "missing")}},
		{name: "corrupt reservation", job: Job{RunID: "usage-reservation-error", TargetDir: t.TempDir()}, corruptReservation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.job.TargetDir
			job := tc.job
			job.SchemaVersion = SchemaVersion
			job.Operation = OperationStart
			job.ExecutionID = strings.Repeat("b", ExecutionIDBytes*2)
			socket := filepath.Join("/tmp", fmt.Sprintf("ai-team-usage-admission-%d-%s.sock", os.Getpid(), tc.name))
			_ = os.Remove(socket)
			if tc.corruptReservation {
				reservation := filepath.Join(target, ".ai-team", "state", "usage", job.RunID+".reserved.json")
				if err := os.MkdirAll(filepath.Dir(reservation), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(reservation, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, approvals, socket)
			if err == nil {
				server.close()
				t.Fatal("Unix API admission unexpectedly accepted invalid controller state")
			}
			if _, statErr := os.Lstat(socket); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed admission left socket behind: %v", statErr)
			}
		})
	}
}

func TestWorkerUsageEnvelopeUnixAPIAllowsExactRetryAndRecoveryAdvance(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "usage-unix-run", TargetDir: target, ExecutionID: strings.Repeat("d", ExecutionIDBytes*2)}
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	startServer := func(value Job) (*workerAPIServer, pipeline.UsageEnvelopeWriter) {
		t.Helper()
		socket := filepath.Join("/tmp", fmt.Sprintf("ai-team-usage-%d.sock", os.Getpid()))
		server, err := startWorkerAPIServerUnix(value, &apiRecorderSpy{}, store, socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(WorkerAPIAddressEnv, "http://unix")
		t.Setenv(WorkerAPISocketEnv, socket)
		t.Setenv(WorkerAPITokenEnv, server.token)
		port, err := NewWorkerAPIPort(value)
		if err != nil {
			t.Fatal(err)
		}
		if !port.SupportsControllerUsageStore() {
			t.Fatal("Unix worker API should enable controller usage store")
		}
		return server, NewWorkerAPIUsageEnvelopeWriter(port)
	}
	started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	first := metrics.Build(job.RunID, "feature", started, started.Add(time.Second), nil, 0, "completed", metrics.Usage{})
	server, writer := startServer(job)
	if err := writer.WriteUsageEnvelope(first); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteUsageEnvelope(first); err != nil {
		t.Fatalf("exact retry in same invocation should succeed: %v", err)
	}
	conflicting := first
	conflicting.Outcome = "rewritten"
	if err := writer.WriteUsageEnvelope(conflicting); err == nil {
		t.Fatal("same invocation must not change its submitted envelope")
	}
	server.close()

	recoverJob := job
	recoverJob.Operation = OperationRecover
	recoverJob.ExecutionID = strings.Repeat("e", ExecutionIDBytes*2)
	recoverServer, recoverWriter := startServer(recoverJob)
	defer recoverServer.close()
	updated := first
	updated.Outcome = "completed-after-recovery"
	updated.FinishedAt = updated.FinishedAt.Add(time.Minute)
	if err := recoverWriter.WriteUsageEnvelope(updated); err != nil {
		t.Fatalf("recovery invocation must advance a prior envelope: %v", err)
	}
	stored, err := metrics.ReadUsageEnvelope(target, job.RunID)
	if err != nil || stored.Outcome != updated.Outcome {
		t.Fatalf("stored recovered usage = %+v, %v", stored, err)
	}
}

func TestWorkerControllerAPICandidateMetadataIsRunAndTargetBound(t *testing.T) {
	canonicalTarget := t.TempDir()
	targetLink := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(canonicalTarget, targetLink); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(targetLink)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "candidate-api-run", TargetDir: targetLink,
		ExecutionID: strings.Repeat("c", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	store := NewWorkerAPICandidates(port)
	if _, err := store.Read(targetLink, job.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing controller metadata must preserve not-exist semantics for recovery, got %v", err)
	}
	metadata := candidate.Metadata{SchemaVersion: 1, RunID: job.RunID, ControlTarget: target,
		Worktree: filepath.Join(target, ".ai-team", "worktrees", job.RunID), BaseCommit: "base", BaseTree: "tree", CreatedAt: time.Now().UTC()}
	if err := store.Create(metadata); err != nil {
		t.Fatalf("create candidate metadata through API: %v", err)
	}
	loaded, err := store.Read(targetLink, job.RunID)
	if err != nil || loaded != metadata {
		t.Fatalf("read candidate metadata: %+v %v", loaded, err)
	}
	if _, err := store.Read(target, job.RunID); err != nil {
		t.Fatalf("API store rejected the canonical form of a scoped symlink target: %v", err)
	}
	if _, err := store.Read(t.TempDir(), job.RunID); err == nil {
		t.Fatal("API store accepted a different target")
	}
	if _, err := store.Read(target, "other-run"); err == nil {
		t.Fatal("API store accepted a different run")
	}
	for _, tc := range []struct {
		name     string
		metadata candidate.Metadata
	}{
		{"foreign run", func() candidate.Metadata { m := metadata; m.RunID = "other-run"; return m }()},
		{"foreign target", func() candidate.Metadata { m := metadata; m.ControlTarget = t.TempDir(); return m }()},
		{"traversal worktree", func() candidate.Metadata {
			m := metadata
			m.Worktree = filepath.Join(target, "..", "outside")
			return m
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Create(tc.metadata); err == nil {
				t.Fatal("accepted invalid candidate identity")
			}
		})
	}
}

func TestWorkerControllerAPICandidateCreateAndLoadResolveSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "real-parent")
	target := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(root, "parent-link")
	if err := os.Symlink(parent, linkParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkedTarget := filepath.Join(linkParent, "workspace")
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", append([]string{"-C", target}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", target}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "candidate-parent-link",
		TargetDir: linkedTarget, ExecutionID: strings.Repeat("e", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	store := NewWorkerAPICandidates(port)
	manager, available, err := candidate.CreateWithMetadataStore(context.Background(), linkedTarget, job.RunID, store)
	if err != nil || !available || manager == nil {
		t.Fatalf("create through symlinked parent: available=%v manager=%v err=%v", available, manager, err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Metadata().ControlTarget != canonicalTarget {
		t.Fatalf("candidate target was not canonicalized: got %q want %q", manager.Metadata().ControlTarget, canonicalTarget)
	}
	loaded, err := candidate.LoadWithMetadataStore(context.Background(), linkedTarget, job.RunID, store)
	if err != nil || loaded.Root() != manager.Root() {
		t.Fatalf("load through symlinked parent: manager=%v err=%v", loaded, err)
	}
}

func TestWorkerControllerAPICandidateMetadataCreateIsLimitedToStartAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		operation Operation
		allowed   bool
	}{{OperationStart, true}, {OperationRecover, true}, {OperationResume, false}, {OperationCancel, false}} {
		t.Run(string(tc.operation), func(t *testing.T) {
			target := t.TempDir()
			target, err := filepath.EvalSymlinks(target)
			if err != nil {
				t.Fatal(err)
			}
			job := Job{SchemaVersion: SchemaVersion, Operation: tc.operation, RunID: "candidate-operation-run", TargetDir: target,
				ExecutionID: strings.Repeat("d", ExecutionIDBytes*2)}
			server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
			if err != nil {
				t.Fatal(err)
			}
			defer server.close()
			t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
			t.Setenv(WorkerAPITokenEnv, server.token)
			port, err := NewWorkerAPIPort(job)
			if err != nil {
				t.Fatal(err)
			}
			store := NewWorkerAPICandidates(port)
			metadata := candidate.Metadata{SchemaVersion: 1, RunID: job.RunID, ControlTarget: target,
				Worktree: filepath.Join(target, ".ai-team", "worktrees", job.RunID), BaseCommit: "base", BaseTree: "tree", CreatedAt: time.Now().UTC()}
			err = store.Create(metadata)
			if tc.allowed {
				if err != nil {
					t.Fatalf("operation %s should publish candidate metadata: %v", tc.operation, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("operation %s unexpectedly published candidate metadata", tc.operation)
			}
			if _, readErr := (candidate.FileMetadataStore{}).Read(target, job.RunID); !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("rejected create changed controller state: %v", readErr)
			}
		})
	}
}

func TestWorkerControllerCandidateAbsenceIsControllerProvenAndScoped(t *testing.T) {
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	runID := "controller-absence-run"
	store := candidate.FileMetadataStore{}
	if err := store.MarkAbsent(target, runID); err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationResume, RunID: runID, TargetDir: target,
		ExecutionID: strings.Repeat("1", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	server.candidateAbsenceAllowed = true
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: runID}); err != nil {
		t.Fatalf("controller-established absence marker not readable: %v", err)
	}
	forged, err := server.dispatch("candidate.absence.create", workerAPICall{A: runID})
	if err == nil || forged != nil {
		t.Fatalf("worker created its own candidate absence marker: value=%v err=%v", forged, err)
	}
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: "foreign-run"}); err == nil {
		t.Fatal("candidate absence read accepted another run")
	}
	server.scope.Operation = OperationStart
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: runID}); err == nil {
		t.Fatal("candidate absence read accepted an unsupported operation")
	}
	server.scope.Operation = OperationResume
	server.scope.TargetDir = filepath.Join(target, "missing")
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: runID}); err == nil {
		t.Fatal("candidate absence read accepted a different/unavailable target")
	}
	server.scope.TargetDir = target
	server.candidateAbsenceAllowed = false
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: runID}); err == nil {
		t.Fatal("candidate absence read succeeded without controller admission")
	}
	server.candidateAbsenceAllowed = true
	server.scope.TargetDir = t.TempDir()
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: runID}); err == nil {
		t.Fatal("candidate absence read succeeded without the controller marker")
	}
	server.scope.TargetDir = target

	port := &WorkerAPIPort{scope: workerAPIScope{RunID: runID, Operation: OperationResume, TargetDir: target}}
	apiStore := NewWorkerAPICandidates(port)
	if err := apiStore.(candidate.AbsenceMarkerStore).MarkAbsent(target, runID); err == nil {
		t.Fatal("candidate absence adapter let worker pre-seed or replace a marker")
	}
	if err := apiStore.(candidate.AbsenceMarkerStore).ReadAbsent(target, "foreign-run"); err == nil {
		t.Fatal("candidate absence adapter accepted another run")
	}
	if err := apiStore.(candidate.AbsenceMarkerStore).ReadAbsent(filepath.Join(target, "missing"), runID); err == nil {
		t.Fatal("candidate absence adapter accepted another target")
	}
	otherTarget := t.TempDir()
	if err := apiStore.(candidate.AbsenceMarkerStore).ReadAbsent(otherTarget, runID); err == nil {
		t.Fatal("candidate absence adapter accepted a different valid target")
	}
	// Exercise the successful adapter path too: a correctly scoped worker read
	// must go through the controller API, which owns the durable marker.
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err = NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	apiStore = NewWorkerAPICandidates(port)
	if err := apiStore.(candidate.AbsenceMarkerStore).ReadAbsent(target, runID); err != nil {
		t.Fatalf("scoped candidate absence adapter read: %v", err)
	}
}

func TestPrepareCandidateAbsenceFailsClosedOnInvalidAndCorruptAdmission(t *testing.T) {
	ctx := context.Background()

	// Git detection errors must stop Start instead of treating an unavailable
	// target as a non-Git workspace.
	missing := filepath.Join(t.TempDir(), "missing")
	engine := &ProcessEngine{target: missing, bubblewrap: true}
	if allowed, err := engine.prepareCandidateAbsence(ctx, Job{Operation: OperationStart, RunID: "missing-target"}, nil); err == nil || allowed {
		t.Fatalf("Start accepted a missing target: allowed=%v err=%v", allowed, err)
	}

	// Recover requires the lifecycle store before consulting any candidate
	// evidence, and propagates errors reading that store.
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Operation: OperationRecover, RunID: "corrupt-lifecycle"}
	engine = &ProcessEngine{target: target, bubblewrap: true}
	if allowed, err := engine.prepareCandidateAbsence(ctx, job, nil); err == nil || allowed {
		t.Fatalf("Recover accepted a nil lifecycle store: allowed=%v err=%v", allowed, err)
	}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(target, ".ai-team", "state", "runs", job.RunID+".json")
	if err := os.WriteFile(statePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if allowed, err := engine.prepareCandidateAbsence(ctx, job, store); err == nil || allowed {
		t.Fatalf("Recover ignored corrupt lifecycle state: allowed=%v err=%v", allowed, err)
	}
}

func TestPrepareCandidateAbsenceResumeConsumesGitAndMetadataEvidence(t *testing.T) {
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	engine := &ProcessEngine{target: target, bubblewrap: true}
	job := Job{Operation: OperationResume, RunID: "resume-git-evidence"}
	metadata := candidate.FileMetadataStore{}
	if err := metadata.MarkGitAdmission(target, job.RunID); err != nil {
		t.Fatal(err)
	}
	if allowed, err := engine.prepareCandidateAbsence(context.Background(), job, nil); err != nil || allowed {
		t.Fatalf("Resume with Git admission evidence: allowed=%v err=%v", allowed, err)
	}

	job.RunID = "resume-metadata-evidence"
	if err := metadata.Create(candidate.Metadata{SchemaVersion: 1, RunID: job.RunID, ControlTarget: target,
		Worktree: filepath.Join(target, ".ai-team", "worktrees", job.RunID), BaseCommit: strings.Repeat("a", 40), BaseTree: strings.Repeat("b", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := engine.prepareCandidateAbsence(context.Background(), job, nil); err != nil || allowed {
		t.Fatalf("Resume with candidate metadata: allowed=%v err=%v", allowed, err)
	}

	job.RunID = "resume-missing-proof"
	if allowed, err := engine.prepareCandidateAbsence(context.Background(), job, nil); err != nil || allowed {
		t.Fatalf("Resume without absence proof should remain candidate-backed: allowed=%v err=%v", allowed, err)
	}
}

func TestPrepareCandidateAbsenceRejectsCorruptProofAndGitRecoveryWithoutProof(t *testing.T) {
	ctx := context.Background()
	makeTarget := func() string {
		t.Helper()
		target, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	assertRejected := func(name string, engine *ProcessEngine, job Job, store lifecycle.StorePort) {
		t.Helper()
		if allowed, err := engine.prepareCandidateAbsence(ctx, job, store); err == nil || allowed {
			t.Fatalf("%s must fail closed: allowed=%v err=%v", name, allowed, err)
		}
	}

	// A Git Start cannot publish its positive admission record when the
	// controller state path is obstructed by a regular file.
	gitTarget := makeTarget()
	if output, err := exec.Command("git", "-C", gitTarget, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.MkdirAll(filepath.Join(gitTarget, ".ai-team", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitTarget, ".ai-team", "state", "candidates"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRejected("Git Start with blocked admission path", &ProcessEngine{target: gitTarget}, Job{Operation: OperationStart, RunID: "blocked-git-admission"}, nil)

	// Recover returns an existing candidate's metadata as candidate-backed.
	target := makeTarget()
	job := Job{Operation: OperationRecover, RunID: "recover-existing-metadata"}
	metadata := candidate.FileMetadataStore{}
	if err := metadata.Create(candidate.Metadata{SchemaVersion: 1, RunID: job.RunID, ControlTarget: target,
		Worktree: filepath.Join(target, ".ai-team", "worktrees", job.RunID), BaseCommit: strings.Repeat("c", 40),
		BaseTree: strings.Repeat("d", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := (&ProcessEngine{target: target, bubblewrap: true}).prepareCandidateAbsence(ctx, job, store); err != nil || allowed {
		t.Fatalf("Recover with candidate metadata: allowed=%v err=%v", allowed, err)
	}

	// A corrupt controller proof is an error, never a reason to infer a new
	// kind of admission.
	corruptTarget := makeTarget()
	corruptStore, err := lifecycle.NewStore(corruptTarget)
	if err != nil {
		t.Fatal(err)
	}
	corruptJob := Job{Operation: OperationRecover, RunID: "corrupt-absence-proof"}
	markerPath := filepath.Join(corruptTarget, ".ai-team", "state", "candidates", corruptJob.RunID+".absent.json")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRejected("Recover with corrupt absence proof", &ProcessEngine{target: corruptTarget, bubblewrap: true}, corruptJob, corruptStore)

	// A repository discovered during Recover is still a normal candidate run;
	// the controller must not grant absence authority without an admission
	// record from Start.
	gitRecoverTarget := makeTarget()
	if output, err := exec.Command("git", "-C", gitRecoverTarget, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init for Recover: %v: %s", err, output)
	}
	gitRecoverStore, err := lifecycle.NewStore(gitRecoverTarget)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := (&ProcessEngine{target: gitRecoverTarget, bubblewrap: true}).prepareCandidateAbsence(ctx,
		Job{Operation: OperationRecover, RunID: "git-recover-without-proof"}, gitRecoverStore); err != nil || allowed {
		t.Fatalf("Recover with Git but no prior proof: allowed=%v err=%v", allowed, err)
	}

	// Resume likewise rejects a corrupt marker rather than silently trusting
	// metadata from the target volume.
	resumeTarget := makeTarget()
	resumeJob := Job{Operation: OperationResume, RunID: "corrupt-resume-proof"}
	resumePath := filepath.Join(resumeTarget, ".ai-team", "state", "candidates", resumeJob.RunID+".absent.json")
	if err := os.MkdirAll(filepath.Dir(resumePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resumePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRejected("Resume with corrupt absence proof", &ProcessEngine{target: resumeTarget, bubblewrap: true}, resumeJob, nil)

	// A corrupt Git admission record is also an error on Recover and Resume.
	for _, operation := range []Operation{OperationRecover, OperationResume} {
		corruptGitTarget := makeTarget()
		corruptGitStore, err := lifecycle.NewStore(corruptGitTarget)
		if err != nil {
			t.Fatal(err)
		}
		corruptGitJob := Job{Operation: operation, RunID: "corrupt-git-proof-" + string(operation)}
		gitProofPath := filepath.Join(corruptGitTarget, ".ai-team", "state", "candidates", corruptGitJob.RunID+".git-admitted.json")
		if err := os.MkdirAll(filepath.Dir(gitProofPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(gitProofPath, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertRejected("operation with corrupt Git admission", &ProcessEngine{target: corruptGitTarget, bubblewrap: true}, corruptGitJob, corruptGitStore)
	}

	// Corrupt candidate metadata is not equivalent to a missing record.
	corruptMetadataTarget := makeTarget()
	corruptMetadataStore, err := lifecycle.NewStore(corruptMetadataTarget)
	if err != nil {
		t.Fatal(err)
	}
	corruptMetadataJob := Job{Operation: OperationRecover, RunID: "corrupt-candidate-metadata"}
	metadataPath := filepath.Join(corruptMetadataTarget, ".ai-team", "state", "candidates", corruptMetadataJob.RunID+".json")
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRejected("Recover with corrupt candidate metadata", &ProcessEngine{target: corruptMetadataTarget, bubblewrap: true}, corruptMetadataJob, corruptMetadataStore)

	// A missing target is a detection error on Recover, while an intact
	// non-Git target with no prior evidence is rejected explicitly.
	validTarget := makeTarget()
	validStore, err := lifecycle.NewStore(validTarget)
	if err != nil {
		t.Fatal(err)
	}
	missingTarget := filepath.Join(t.TempDir(), "missing")
	assertRejected("Recover with missing target", &ProcessEngine{target: missingTarget, bubblewrap: true},
		Job{Operation: OperationRecover, RunID: "recover-missing-target"}, validStore)
	assertRejected("Recover without admission evidence", &ProcessEngine{target: validTarget, bubblewrap: true},
		Job{Operation: OperationRecover, RunID: "recover-no-proof"}, validStore)

	// The Start path separately refuses pre-seeded absence markers and reports
	// marker creation failures; both cases are security-relevant.
	preseedTarget := makeTarget()
	preseedJob := Job{Operation: OperationStart, RunID: "preseeded-nongit-marker"}
	if err := (candidate.FileMetadataStore{}).MarkAbsent(preseedTarget, preseedJob.RunID); err != nil {
		t.Fatal(err)
	}
	assertRejected("non-Git Start with pre-seeded marker", &ProcessEngine{target: preseedTarget, bubblewrap: true}, preseedJob, nil)
	assertRejected("non-Git Start with invalid run id", &ProcessEngine{target: makeTarget(), bubblewrap: true},
		Job{Operation: OperationStart, RunID: "../invalid"}, nil)
	blockedTarget := makeTarget()
	if err := os.MkdirAll(filepath.Join(blockedTarget, ".ai-team", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedTarget, ".ai-team", "state", "candidates"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRejected("non-Git Start with blocked marker path", &ProcessEngine{target: blockedTarget, bubblewrap: true},
		Job{Operation: OperationStart, RunID: "blocked-marker-path"}, nil)

	// Resume propagates malformed metadata instead of interpreting it as
	// missing candidate data.
	resumeMetadataTarget := makeTarget()
	resumeMetadataJob := Job{Operation: OperationResume, RunID: "corrupt-resume-metadata"}
	resumeMetadataPath := filepath.Join(resumeMetadataTarget, ".ai-team", "state", "candidates", resumeMetadataJob.RunID+".json")
	if err := os.MkdirAll(filepath.Dir(resumeMetadataPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resumeMetadataPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRejected("Resume with corrupt metadata", &ProcessEngine{target: resumeMetadataTarget, bubblewrap: true}, resumeMetadataJob, nil)

	// If Git is unavailable at recovery time, no absence conclusion can be
	// drawn from a failed repository probe.
	detectionTarget := makeTarget()
	detectionStore, err := lifecycle.NewStore(detectionTarget)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	assertRejected("Recover when Git detection is unavailable", &ProcessEngine{target: detectionTarget, bubblewrap: true},
		Job{Operation: OperationRecover, RunID: "recover-git-unavailable"}, detectionStore)

	// Unsupported operations and a bubblewrap Resume with no absence proof
	// preserve candidate-backed behavior instead of asserting absence.
	if allowed, err := (&ProcessEngine{target: validTarget, bubblewrap: true}).prepareCandidateAbsence(ctx,
		Job{Operation: OperationResume, RunID: "resume-no-proof"}, nil); err != nil || allowed {
		t.Fatalf("Resume without proof: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := (&ProcessEngine{target: validTarget, bubblewrap: true}).prepareCandidateAbsence(ctx,
		Job{Operation: Operation("unsupported"), RunID: "unsupported-operation"}, nil); err != nil || allowed {
		t.Fatalf("unsupported operation: allowed=%v err=%v", allowed, err)
	}
}

func TestGitCandidateCannotBeDowngradedByHidingGitMarkerAfterControllerSetup(t *testing.T) {
	target := t.TempDir()
	command := exec.Command("git", "-C", target, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	engine := &ProcessEngine{target: target}
	allowed, err := engine.prepareCandidateAbsence(context.Background(), Job{Operation: OperationStart, RunID: "git-target-forged-absence"}, nil)
	if err != nil || allowed {
		t.Fatalf("Git target absence admission: allowed=%v err=%v", allowed, err)
	}
	gitMarker := filepath.Join(target, ".git")
	hiddenMarker := filepath.Join(target, ".git.hidden")
	if err := os.Rename(gitMarker, hiddenMarker); err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "git-target-forged-absence", TargetDir: target,
		ExecutionID: strings.Repeat("2", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	server.candidateAbsenceAllowed = allowed
	if _, err := server.dispatch("candidate.absence.read", workerAPICall{A: job.RunID}); err == nil {
		t.Fatal("worker hid .git and forged candidate absence after controller Git admission")
	}
	if _, err := server.dispatch("candidate.absence.create", workerAPICall{A: job.RunID}); err == nil {
		t.Fatal("worker forged candidate absence through create endpoint")
	}
}

func TestRecoverCannotCreateAbsenceProofAfterGitStartAdmission(t *testing.T) {
	target := t.TempDir()
	command := exec.Command("git", "-C", target, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Operation: OperationStart, RunID: "recover-git-admission", TargetDir: canonicalTarget}
	engine := &ProcessEngine{target: canonicalTarget, bubblewrap: true}
	allowed, err := engine.prepareCandidateAbsence(context.Background(), job, nil)
	if err != nil || allowed {
		t.Fatalf("Git Start admission: allowed=%v err=%v", allowed, err)
	}

	if err := os.Rename(filepath.Join(canonicalTarget, ".git"), filepath.Join(canonicalTarget, ".git.hidden")); err != nil {
		t.Fatal(err)
	}
	lifecycleStore, err := lifecycle.NewStore(canonicalTarget)
	if err != nil {
		t.Fatal(err)
	}
	recoverJob := job
	recoverJob.Operation = OperationRecover
	allowed, err = engine.prepareCandidateAbsence(context.Background(), recoverJob, lifecycleStore)
	if err == nil || allowed {
		t.Fatalf("Recover must fail closed without prior proof after Git disappears: allowed=%v err=%v", allowed, err)
	}
	markerPath := filepath.Join(canonicalTarget, ".ai-team", "state", "candidates", job.RunID+".absent.json")
	if _, markerErr := os.Lstat(markerPath); !errors.Is(markerErr, os.ErrNotExist) {
		t.Fatalf("Recover must not create an absence marker: %v", markerErr)
	}
	if err := (candidate.FileMetadataStore{}).ReadGitAdmission(canonicalTarget, job.RunID); err != nil {
		t.Fatalf("Git Start admission evidence missing: %v", err)
	}
}

func TestRecoverRejectsPreseededAbsenceMarkerAfterGitStart(t *testing.T) {
	target := t.TempDir()
	command := exec.Command("git", "-C", target, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Operation: OperationStart, RunID: "recover-preseeded-absence", TargetDir: target}
	store := candidate.FileMetadataStore{}
	if err := store.MarkAbsent(target, job.RunID); err != nil {
		t.Fatalf("preseed absence marker: %v", err)
	}
	engine := &ProcessEngine{target: target, bubblewrap: true}
	allowed, err := engine.prepareCandidateAbsence(context.Background(), job, nil)
	if err == nil || allowed {
		t.Fatalf("Git Start must reject a pre-existing absence marker: allowed=%v err=%v", allowed, err)
	}
	if err := os.Rename(filepath.Join(target, ".git"), filepath.Join(target, ".git.hidden")); err != nil {
		t.Fatal(err)
	}
	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	job.Operation = OperationRecover
	allowed, err = engine.prepareCandidateAbsence(context.Background(), job, lifecycleStore)
	if err == nil || allowed {
		t.Fatalf("Recover must reject preseeded absence after Git Start even when .git is hidden: allowed=%v err=%v", allowed, err)
	}
}

func TestRecoverAcceptsPreexistingNonGitStartAbsenceMarker(t *testing.T) {
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Operation: OperationStart, RunID: "recover-nongit-admission", TargetDir: target}
	engine := &ProcessEngine{target: target, bubblewrap: true}
	allowed, err := engine.prepareCandidateAbsence(context.Background(), job, nil)
	if err != nil || !allowed {
		t.Fatalf("non-Git Start admission: allowed=%v err=%v", allowed, err)
	}

	job.Operation = OperationRecover
	allowed, err = engine.prepareCandidateAbsence(context.Background(), job, lifecycleStore)
	if err != nil || !allowed {
		t.Fatalf("Recover should trust the preexisting Start marker: allowed=%v err=%v", allowed, err)
	}
}

func TestProcessEngineCandidateAbsenceRequiresBubblewrapMask(t *testing.T) {
	target := t.TempDir()
	job := Job{Operation: OperationStart, RunID: "cloud-nongit-admission"}
	plainEngine := &ProcessEngine{target: target}
	allowed, err := plainEngine.prepareCandidateAbsence(context.Background(), job, nil)
	if err != nil || allowed {
		t.Fatalf("unmasked target must not establish absence authority: allowed=%v err=%v", allowed, err)
	}
	if err := (candidate.FileMetadataStore{}).ReadAbsent(target, job.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmasked start should not persist an authoritative marker: %v", err)
	}

	isolatedEngine := &ProcessEngine{target: target, bubblewrap: true}
	allowed, err = isolatedEngine.prepareCandidateAbsence(context.Background(), job, nil)
	if err != nil || !allowed {
		t.Fatalf("bubblewrap start should establish absence authority: allowed=%v err=%v", allowed, err)
	}
	if err := (candidate.FileMetadataStore{}).ReadAbsent(target, job.RunID); err != nil {
		t.Fatalf("controller did not persist durable marker: %v", err)
	}
	resume := job
	resume.Operation = OperationResume
	allowed, err = plainEngine.prepareCandidateAbsence(context.Background(), resume, nil)
	if err != nil || allowed {
		t.Fatalf("unmasked resume must not trust target marker: allowed=%v err=%v", allowed, err)
	}
	allowed, err = isolatedEngine.prepareCandidateAbsence(context.Background(), resume, nil)
	if err != nil || !allowed {
		t.Fatalf("bubblewrap resume should accept controller marker: allowed=%v err=%v", allowed, err)
	}
}

type candidateMetadataStoreStub struct {
	metadata  candidate.Metadata
	createErr error
	readErr   error
}

func (s *candidateMetadataStoreStub) Create(metadata candidate.Metadata) error {
	s.metadata = metadata
	return s.createErr
}

func (s *candidateMetadataStoreStub) Read(string, string) (candidate.Metadata, error) {
	return s.metadata, s.readErr
}

func TestWorkerControllerAPICandidateMetadataRejectsCorruptControllerState(t *testing.T) {
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "candidate-corrupt-state", TargetDir: target,
		ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	store := NewWorkerAPICandidates(port)
	metadata := candidate.Metadata{SchemaVersion: 1, RunID: job.RunID, ControlTarget: target,
		Worktree: filepath.Join(target, ".ai-team", "worktrees", job.RunID), BaseCommit: "base", BaseTree: "tree", CreatedAt: time.Now().UTC()}

	server.candidates = &candidateMetadataStoreStub{createErr: errors.New("controller disk is read-only")}
	if err := store.Create(metadata); err == nil || !strings.Contains(err.Error(), "controller disk is read-only") {
		t.Fatalf("controller persistence failure should reach the worker: %v", err)
	}

	// A corrupt durable record must be stopped at the controller boundary. It
	// must not be returned to the worker, where it could be mistaken for a
	// recoverable candidate identity.
	corrupt := metadata
	corrupt.ControlTarget = filepath.Join(target, "foreign")
	server.candidates = &candidateMetadataStoreStub{metadata: corrupt}
	if _, err := store.Read(target, job.RunID); err == nil || !strings.Contains(err.Error(), "candidate metadata") {
		t.Fatalf("controller returned corrupt candidate identity: %v", err)
	}
}

func TestWorkerAPICandidateAdapterRejectsUnavailableAndOutOfScopeRequests(t *testing.T) {
	var missing *workerAPICandidates
	if err := missing.Create(candidate.Metadata{}); err == nil {
		t.Fatal("nil candidate adapter accepted a create")
	}
	if _, err := missing.Read("/tmp/target", "run"); err == nil {
		t.Fatal("nil candidate adapter accepted a read")
	}
	if err := (NewWorkerAPICandidates(nil)).Create(candidate.Metadata{}); err == nil {
		t.Fatal("candidate adapter without a port accepted a create")
	}
	if _, err := (NewWorkerAPICandidates(nil)).Read("/tmp/target", "run"); err == nil {
		t.Fatal("candidate adapter without a port accepted a read")
	}

	target := t.TempDir()
	port := &workerAPIPort{scope: workerAPIScope{RunID: "candidate-scope", TargetDir: target}}
	store := NewWorkerAPICandidates(port)
	if _, err := store.Read(target, "another-run"); err == nil {
		t.Fatal("candidate adapter accepted a foreign run")
	}
	if _, err := store.Read(filepath.Join(target, "missing"), "candidate-scope"); err == nil {
		t.Fatal("candidate adapter accepted a different target")
	}
	if err := store.Create(candidate.Metadata{}); err == nil {
		t.Fatal("candidate adapter sent invalid metadata to the controller")
	}
	if _, err := store.Read(filepath.Join(target, "missing"), "candidate-scope"); err == nil {
		t.Fatal("candidate adapter accepted a target that does not exist")
	}
	missingScope := NewWorkerAPICandidates(&WorkerAPIPort{scope: workerAPIScope{RunID: "candidate-scope", TargetDir: filepath.Join(target, "missing")}})
	if err := missingScope.Create(candidate.Metadata{}); err == nil {
		t.Fatal("candidate adapter accepted a controller scope that does not exist")
	}
	if _, err := missingScope.Read(target, "candidate-scope"); err == nil {
		t.Fatal("candidate adapter accepted an invalid controller scope")
	}
}

func TestWorkerAPICandidateAdapterRejectsInvalidMetadataReturnedByPeer(t *testing.T) {
	target := t.TempDir()
	var err error
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "candidate-invalid-peer", TargetDir: target,
		ExecutionID: strings.Repeat("b", ExecutionIDBytes*2)}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schema_version":1,"run_id":"foreign-run"}`)
	}))
	defer peer.Close()
	t.Setenv(WorkerAPIAddressEnv, peer.URL)
	t.Setenv(WorkerAPITokenEnv, "test-capability")
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkerAPICandidates(port).Read(target, job.RunID); err == nil || !strings.Contains(err.Error(), "candidate metadata identity mismatch") {
		t.Fatalf("candidate adapter accepted invalid metadata from its controller peer: %v", err)
	}
}

func TestWorkerControllerAPICandidateDispatchFailsClosedOnScopeAndStoreErrors(t *testing.T) {
	target := t.TempDir()
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	runID := "candidate-dispatch-boundary"
	metadata := candidate.Metadata{SchemaVersion: 1, RunID: runID, ControlTarget: target,
		Worktree: filepath.Join(target, ".ai-team", "worktrees", runID), BaseCommit: "base", BaseTree: "tree", CreatedAt: time.Now().UTC()}
	server := &workerAPIServer{
		scope:      workerAPIScope{RunID: runID, Operation: OperationStart, TargetDir: target},
		candidates: &candidateMetadataStoreStub{createErr: errors.New("metadata volume unavailable")},
	}
	if _, err := server.dispatch("candidate.metadata.read", workerAPICall{RunID: "another-run", A: runID}); err == nil {
		t.Fatal("controller accepted a worker call carrying a foreign run identity")
	}
	server.scope.RunID = "invalid run id"
	if _, err := server.dispatch("candidate.metadata.read", workerAPICall{}); err == nil {
		t.Fatal("controller accepted an invalid run identity in its own scope")
	}
	server.scope.RunID = runID

	if _, err := server.dispatch("candidate.metadata.create", workerAPICall{CandidateMetadata: candidate.Metadata{}}); err == nil {
		t.Fatal("controller accepted metadata with no candidate identity")
	}
	targetFile, err := os.CreateTemp(t.TempDir(), "not-a-target-directory-")
	if err != nil {
		t.Fatal(err)
	}
	if err := targetFile.Close(); err != nil {
		t.Fatal(err)
	}
	server.scope.TargetDir = targetFile.Name()
	if _, err := server.dispatch("candidate.metadata.create", workerAPICall{CandidateMetadata: metadata}); err == nil {
		t.Fatal("controller accepted metadata when its target could not be canonicalized")
	}
	server.scope.TargetDir = target
	if _, err := server.dispatch("candidate.metadata.create", workerAPICall{CandidateMetadata: metadata}); err == nil || !strings.Contains(err.Error(), "metadata volume unavailable") {
		t.Fatalf("controller hid a metadata persistence error: %v", err)
	}
	if _, err := server.dispatch("candidate.metadata.read", workerAPICall{A: "another-run"}); err == nil {
		t.Fatal("controller returned candidate metadata for a different run")
	}

	server.candidates = &candidateMetadataStoreStub{readErr: errors.New("metadata volume unavailable")}
	if _, err := server.dispatch("candidate.metadata.read", workerAPICall{A: runID}); err == nil || !strings.Contains(err.Error(), "metadata volume unavailable") {
		t.Fatalf("controller hid a metadata read failure: %v", err)
	}

	server.scope.TargetDir = filepath.Join(target, "missing-controller-target")
	if _, err := server.dispatch("candidate.metadata.read", workerAPICall{A: runID}); err == nil {
		t.Fatal("controller read candidate metadata for an unavailable target")
	}
}

func TestWorkerControllerAPILifecycleScopedCreateResumeCheckpointRoundTrip(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "lifecycle-run", TargetDir: target, ExecutionID: strings.Repeat("f", ExecutionIDBytes*2)}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	server.lifecycle = store
	defer server.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+server.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := NewWorkerAPILifecycle(port)
	createdAt := time.Now().UTC().Add(-time.Minute)
	state := lifecycle.State{
		RunID: job.RunID, Feature: "feature", TargetDir: target, Task: "business request",
		Phase: lifecycle.PhaseRunning, NextStage: "analyst", ConfigSHA256: strings.Repeat("a", 64),
		WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: createdAt,
	}
	if err := checkpoints.Create(state); err != nil {
		t.Fatalf("controller create: %v", err)
	}
	loaded, err := checkpoints.Load(job.RunID)
	if err != nil || loaded.RunID != state.RunID || loaded.TargetDir != target || loaded.Phase != lifecycle.PhaseRunning || loaded.NextStage != "analyst" {
		t.Fatalf("controller load after create: state=%+v err=%v", loaded, err)
	}
	resumed := loaded
	resumed.Phase, resumed.NextStage, resumed.PendingApprovalID = lifecycle.PhaseWaiting, "analyst", "approval-1"
	if err := checkpoints.Save(loaded, resumed); err != nil {
		t.Fatalf("controller save checkpoint: %v", err)
	}
	reloaded, err := store.Load(job.RunID)
	if err != nil || reloaded.Phase != lifecycle.PhaseWaiting || reloaded.PendingApprovalID != "approval-1" || !reloaded.CreatedAt.Equal(loaded.CreatedAt) {
		t.Fatalf("controller-owned persisted checkpoint: state=%+v err=%v", reloaded, err)
	}
	staleNext := reloaded
	staleNext.Phase, staleNext.NextStage, staleNext.PendingApprovalID = lifecycle.PhaseTerminal, "", ""
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: loaded, Lifecycle: staleNext}); err == nil {
		t.Fatal("controller accepted stale previous checkpoint after a valid update")
	}
	afterStale, err := store.Load(job.RunID)
	if err != nil || !sameLifecycleState(afterStale, reloaded) {
		t.Fatalf("stale checkpoint changed persisted state: state=%+v err=%v", afterStale, err)
	}
	terminal := reloaded
	terminal.Phase, terminal.NextStage, terminal.PendingApprovalID = lifecycle.PhaseTerminal, "", ""
	if err := checkpoints.Save(reloaded, terminal); err != nil {
		t.Fatalf("controller save terminal checkpoint: %v", err)
	}
	terminal, err = store.Load(job.RunID)
	if err != nil {
		t.Fatalf("load terminal checkpoint: %v", err)
	}
	forgedPrevious := terminal
	forgedPrevious.Phase, forgedPrevious.NextStage = lifecycle.PhaseRunning, "analyst"
	forgedNext := forgedPrevious
	forgedNext.Phase, forgedNext.NextStage = lifecycle.PhaseResumable, "coder"
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: forgedPrevious, Lifecycle: forgedNext}); err == nil {
		t.Fatal("controller accepted forged nonterminal previous checkpoint for terminal state")
	}
	afterForgery, err := store.Load(job.RunID)
	if err != nil || !sameLifecycleState(afterForgery, terminal) {
		t.Fatalf("forged previous checkpoint changed persisted terminal state: state=%+v err=%v", afterForgery, err)
	}
	if _, err := checkpoints.Load("another-run"); err == nil {
		t.Fatal("worker port loaded another run")
	}

	wrongRun := state
	wrongRun.RunID = "another-run"
	if _, err := server.dispatch("lifecycle.create", workerAPICall{Lifecycle: wrongRun}); err == nil {
		t.Fatal("controller accepted lifecycle create for another run")
	}
	wrongTarget := state
	wrongTarget.TargetDir = filepath.Join(target, "elsewhere")
	if _, err := server.dispatch("lifecycle.create", workerAPICall{Lifecycle: wrongTarget}); err == nil {
		t.Fatal("controller accepted lifecycle create for another target")
	}
	if _, err := server.dispatch("lifecycle.load", workerAPICall{A: "another-run"}); err == nil {
		t.Fatal("controller accepted lifecycle load for another run")
	}
	server.lifecycle = wrongTargetLifecyclePort{}
	if _, err := server.dispatch("lifecycle.load", workerAPICall{A: job.RunID}); err == nil {
		t.Fatal("controller returned lifecycle state for another target")
	}
	wrongPrevious := loaded
	wrongPrevious.TargetDir = filepath.Join(target, "elsewhere")
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: wrongPrevious, Lifecycle: resumed}); err == nil {
		t.Fatal("controller accepted lifecycle save for another target")
	}
}

func TestWorkerControllerAPILifecycleRejectsUnavailableAndOutOfScopeCalls(t *testing.T) {
	target := t.TempDir()
	job := Job{RunID: "scoped", Operation: OperationResume, TargetDir: target, ExecutionID: strings.Repeat("9", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	state := lifecycle.State{RunID: job.RunID, TargetDir: target}
	for _, tc := range []struct {
		method string
		call   workerAPICall
	}{
		{"lifecycle.create", workerAPICall{Lifecycle: state}},
		{"lifecycle.load", workerAPICall{A: job.RunID}},
		{"lifecycle.save", workerAPICall{Previous: state, Lifecycle: state}},
	} {
		if _, err := server.dispatch(tc.method, tc.call); err == nil {
			t.Fatalf("%s succeeded without controller store", tc.method)
		}
	}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	server.lifecycle = store
	wrongRun := state
	wrongRun.RunID = "foreign"
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: wrongRun, Lifecycle: wrongRun}); err == nil {
		t.Fatal("controller accepted lifecycle save for another run")
	}
	wrongTarget := state
	wrongTarget.TargetDir = filepath.Join(target, "foreign")
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: state, Lifecycle: wrongTarget}); err == nil {
		t.Fatal("controller accepted lifecycle save with a different target")
	}
	server.lifecycle = lifecycleLoadErrorPort{err: errors.New("simulated checkpoint read failure")}
	if _, err := server.dispatch("lifecycle.load", workerAPICall{A: job.RunID}); err == nil {
		t.Fatal("controller hid lifecycle load failure")
	}
	server.lifecycle = wrongTargetLifecyclePort{}
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: state, Lifecycle: state}); err == nil {
		t.Fatal("controller accepted lifecycle store state for another target during save")
	}
	if _, err := server.dispatch("unknown.method", workerAPICall{}); err == nil {
		t.Fatal("controller accepted unknown worker API method")
	}
}

type wrongTargetLifecyclePort struct{}

func (wrongTargetLifecyclePort) Create(lifecycle.State) error { return nil }
func (wrongTargetLifecyclePort) Load(runID string) (lifecycle.State, error) {
	return lifecycle.State{RunID: runID, TargetDir: filepath.Join(os.TempDir(), "wrong-target")}, nil
}
func (wrongTargetLifecyclePort) Save(lifecycle.State, lifecycle.State) error { return nil }

type lifecycleLoadErrorPort struct{ err error }

func (p lifecycleLoadErrorPort) Create(lifecycle.State) error { return nil }
func (p lifecycleLoadErrorPort) Load(string) (lifecycle.State, error) {
	return lifecycle.State{}, p.err
}
func (p lifecycleLoadErrorPort) Save(lifecycle.State, lifecycle.State) error { return nil }

func TestWorkerControllerAPIRejectsMalformedAndUnauthenticatedRequests(t *testing.T) {
	job := Job{RunID: "run", Operation: OperationStart, ExecutionID: strings.Repeat("b", ExecutionIDBytes*2)}
	server, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	for _, tc := range []struct {
		name, method, path, auth, body string
		want                           int
	}{
		{"wrong method", http.MethodGet, "/v1/call", "", "", http.StatusNotFound},
		{"wrong path", http.MethodPost, "/admin", "Bearer " + server.token, "{}", http.StatusNotFound},
		{"missing auth", http.MethodPost, "/v1/call", "", "{}", http.StatusUnauthorized},
		{"unknown request field", http.MethodPost, "/v1/call", "Bearer " + server.token, `{"extra":true}`, http.StatusForbidden},
		{"unknown payload field", http.MethodPost, "/v1/call", "Bearer " + server.token, `{"run_id":"run","operation":"start","execution_id":"` + job.ExecutionID + `","method":"approval.list","payload":{"unexpected":1}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, "http://"+server.listener.Addr().String()+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.want)
			}
		})
	}
	tooLarge := strings.NewReader(strings.Repeat("x", workerAPIMaxBody+1))
	req, err := http.NewRequest(http.MethodPost, "http://"+server.listener.Addr().String()+"/v1/call", tooLarge)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+server.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", resp.StatusCode)
	}
	api, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}, createErr: errors.New("store unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	defer api.close()
	port := &workerAPIPort{address: "http://" + api.listener.Addr().String(), token: api.token, scope: api.scope, client: http.DefaultClient}
	if _, err := (&workerAPIApprovals{port: port}).Create(approval.PendingApproval{ID: "id"}); err == nil {
		t.Fatal("approval storage failure was hidden")
	}
}

func TestWorkerControllerAPIOptionsRejectMissingPortsAndEnvironment(t *testing.T) {
	if _, err := startWorkerAPIServer(Job{}, nil, nil); err == nil {
		t.Fatal("missing controller ports accepted")
	}
	if err := WithControllerAPI(nil, nil)(&ProcessEngine{}); err == nil {
		t.Fatal("empty controller API option accepted")
	}
	t.Setenv(WorkerAPIAddressEnv, "")
	t.Setenv(WorkerAPITokenEnv, "")
	if _, err := NewWorkerAPIPort(Job{}); err == nil {
		t.Fatal("missing API environment accepted")
	}
	t.Setenv(WorkerAPIAddressEnv, "http://localhost:1234")
	t.Setenv(WorkerAPITokenEnv, "token")
	if _, err := NewWorkerAPIPort(Job{}); err == nil {
		t.Fatal("non-loopback API address accepted")
	}
	if _, err := NewProcessEngine([]string{"worker"}, t.TempDir(), filepath.Join(t.TempDir(), "db"), func(*ProcessEngine) error { return errors.New("option failure") }); err == nil {
		t.Fatal("process engine accepted a failing option")
	}
}

func TestWorkerControllerAPIUsesPrivateUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain worker API transport is used only by Linux bubblewrap")
	}
	job := Job{RunID: "unix-run", Operation: OperationStart, ExecutionID: strings.Repeat("f", ExecutionIDBytes*2), TargetDir: t.TempDir()}
	socketDir, err := os.MkdirTemp("/tmp", "worker-api-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	socketPath := filepath.Join(socketDir, "api.sock")
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	if _, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, store, filepath.Join(socketDir, "missing", "api.sock")); err == nil {
		t.Fatal("Unix API socket setup failure was accepted")
	}
	server, err := startWorkerAPIServerUnix(job, &apiRecorderSpy{}, store, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("API socket mode=%#o want 0600", info.Mode().Perm())
	}
	t.Setenv(WorkerAPIAddressEnv, "http://unix")
	t.Setenv(WorkerAPISocketEnv, socketPath)
	t.Setenv(WorkerAPITokenEnv, server.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	var approvals []approval.PendingApproval
	if err := port.call("approval.list", workerAPICall{RunID: job.RunID}, &approvals); err != nil {
		t.Fatalf("scoped API call over unix socket failed: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("unexpected approvals from empty scoped store: %+v", approvals)
	}
	server.close()
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("API socket remained after server close: %v", err)
	}
}

func TestWorkerControllerAPIUnixSetupFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain worker API transport is used only by Linux bubblewrap")
	}
	job := Job{RunID: "unix-failure", Operation: OperationStart, ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	socketDir, err := os.MkdirTemp("/tmp", "worker-api-fail-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	socketPath := filepath.Join(socketDir, "api.sock")
	for _, tc := range []struct {
		name      string
		recorder  pipeline.Recorder
		approvals workerApprovalPort
		path      string
		want      string
	}{
		{name: "nil recorder", approvals: store, path: socketPath, want: "requires recorder"},
		{name: "nil approvals", recorder: &apiRecorderSpy{}, path: socketPath, want: "requires recorder"},
		{name: "relative socket path", recorder: &apiRecorderSpy{}, approvals: store, path: "relative.sock", want: "absolute and clean"},
		{name: "unclean socket path", recorder: &apiRecorderSpy{}, approvals: store, path: socketDir + "/../" + filepath.Base(socketDir) + "/api.sock", want: "absolute and clean"},
		{name: "socket parent unavailable", recorder: &apiRecorderSpy{}, approvals: store, path: filepath.Join(socketDir, "missing", "api.sock"), want: "listen on worker controller API socket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := startWorkerAPIServerUnix(job, tc.recorder, tc.approvals, tc.path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q setup failure, got %v", tc.want, err)
			}
		})
	}

	t.Run("TCP listen failure propagates", func(t *testing.T) {
		listenErr := errors.New("injected listener failure")
		_, err := startWorkerAPIServerWithListener(job, &apiRecorderSpy{}, store, func() (net.Listener, error) {
			return nil, listenErr
		}, strings.NewReader("entropy"))
		if !errors.Is(err, listenErr) {
			t.Fatalf("listener failure not propagated: %v", err)
		}
	})

	t.Run("chmod failure closes and removes socket", func(t *testing.T) {
		path := filepath.Join(socketDir, "chmod-failure.sock")
		chmodErr := errors.New("injected chmod failure")
		_, err := startWorkerAPIServerUnixWith(job, &apiRecorderSpy{}, store, path, net.Listen,
			func(string, os.FileMode) error { return chmodErr }, strings.NewReader("unused"))
		if !errors.Is(err, chmodErr) {
			t.Fatalf("socket chmod failure not propagated: %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket path remained after chmod failure: %v", err)
		}
	})

	t.Run("entropy failure closes and removes socket", func(t *testing.T) {
		path := filepath.Join(socketDir, "entropy-failure.sock")
		_, err := startWorkerAPIServerUnixWith(job, &apiRecorderSpy{}, store, path, net.Listen, os.Chmod, strings.NewReader(""))
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("entropy failure must abort API startup, got %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket path remained after entropy failure: %v", err)
		}
	})
}

type concurrentAPIRecorder struct {
	pipeline.Recorder
	active atomic.Int32
	max    atomic.Int32
}

func (r *concurrentAPIRecorder) RunStarted(string, string, string, time.Time) {
	active := r.active.Add(1)
	for current := r.max.Load(); active > current && !r.max.CompareAndSwap(current, active); current = r.max.Load() {
	}
	time.Sleep(15 * time.Millisecond)
	r.active.Add(-1)
}

func TestWorkerControllerAPISerializesConcurrentRecorderDispatch(t *testing.T) {
	job := Job{RunID: "run", Operation: OperationStart, ExecutionID: strings.Repeat("d", ExecutionIDBytes*2)}
	recorder := &concurrentAPIRecorder{}
	server, err := startWorkerAPIServer(job, recorder, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	var wait sync.WaitGroup
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := server.dispatch("recorder.run_started", workerAPICall{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if got := recorder.max.Load(); got != 1 {
		t.Fatalf("concurrent dispatch reached recorder %d times; want serialized invocation", got)
	}
}

func TestWorkerAPIClientAndDispatchRejectBadPeerResponsesAndScope(t *testing.T) {
	job := Job{RunID: "bound", Operation: OperationStart, ExecutionID: strings.Repeat("c", ExecutionIDBytes*2)}
	api, err := startWorkerAPIServer(job, &apiRecorderSpy{}, &apiApprovalStore{values: map[string]approval.PendingApproval{}})
	if err != nil {
		t.Fatal(err)
	}
	defer api.close()
	if _, err := api.dispatch("approval.list", workerAPICall{RunID: "other"}); err == nil {
		t.Fatal("dispatch accepted another run")
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"error status", "denied", http.StatusForbidden},
		{"malformed success body", "not-json", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer peer.Close()
			port := &workerAPIPort{address: peer.URL, token: "token", scope: api.scope, client: peer.Client()}
			var result approval.PendingApproval
			if err := port.call("approval.load", workerAPICall{}, &result); err == nil {
				t.Fatal("bad peer response accepted")
			}
		})
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", workerAPIMaxBody+1))
	}))
	defer large.Close()
	largePort := &workerAPIPort{address: large.URL, token: "token", scope: api.scope, client: large.Client()}
	if err := largePort.call("approval.list", workerAPICall{}, nil); err == nil {
		t.Fatal("oversized API response accepted")
	}
	port := &workerAPIPort{address: "http://127.0.0.1:1", token: "token", scope: api.scope, client: &http.Client{Timeout: time.Millisecond * 50}}
	if err := port.call("approval.list", workerAPICall{}, nil); err == nil {
		t.Fatal("connection failure not reported")
	}
	if err := port.call("bad payload", make(chan int), nil); err == nil {
		t.Fatal("unmarshalable API payload accepted")
	}
	badURL := &workerAPIPort{address: "http://127.0.0.1:1\n", token: "token", scope: api.scope, client: http.DefaultClient}
	if err := badURL.call("approval.list", workerAPICall{}, nil); err == nil {
		t.Fatal("invalid API URL accepted")
	}
	recorder := NewWorkerAPIRecorder(port).(*workerAPIRecorder)
	recorder.RunStarted(job.RunID, "feature", "snapshot", time.Now())
	if recorder.Error() == nil {
		t.Fatal("recorder transport failure was silently discarded")
	}
}

func TestProcessEngineControllerAPILaunchOmitsDatabasePath(t *testing.T) {
	target := t.TempDir()
	marker := filepath.Join(t.TempDir(), "argv.txt")
	environmentMarker := filepath.Join(t.TempDir(), "environment.json")
	t.Setenv("AI_TEAM_WORKER_ARGS_MARKER", marker)
	t.Setenv("AI_TEAM_WORKER_TEST_ENV_MARKER", environmentMarker)
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "echo")
	allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_ARGS_MARKER", "AI_TEAM_WORKER_TEST_ENV_MARKER", "AI_TEAM_WORKER_TEST_MODE")
	store := &apiApprovalStore{values: map[string]approval.PendingApproval{}}
	var factoryCalls int
	engine, err := NewProcessEngine([]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"}, target, filepath.Join(target, "controller-secret.db"), WithControllerAPI(func() pipeline.Recorder {
		factoryCalls++
		return &apiRecorderSpy{}
	}, store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(context.Background(), Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "api-run-two", TargetDir: target, Feature: "feature", Task: "task"}); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Execute(context.Background(), Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "api-run", TargetDir: target, Feature: "feature", Task: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 2 {
		t.Fatalf("controller recorder factory called %d times for 2 invocations", factoryCalls)
	}
	args, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "--db") || strings.Contains(string(args), "controller-secret.db") {
		t.Fatalf("worker launch exposed controller DB arguments: %s", args)
	}
	childEnvironment, err := os.ReadFile(environmentMarker)
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err := json.Unmarshal(childEnvironment, &environment); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{OpenAIEgressSocketEnv, OpenAIEgressTokenEnv, "AI_TEAM_OPENAI_EGRESS_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if _, exists := environment[name]; exists {
			t.Fatalf("unsandboxed controller-API worker received egress setting %s", name)
		}
	}
}

func TestProcessEngineFailsWhenWorkerRecorderAPIRejectsCall(t *testing.T) {
	target := t.TempDir()
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "api-failure")
	allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_TEST_MODE")
	engine, err := NewProcessEngine([]string{os.Args[0], "-test.run=^TestWorkerProtocolHelper$", "--"}, target, filepath.Join(target, "db"),
		WithControllerAPI(func() pipeline.Recorder { return &apiRecorderSpy{} }, &apiApprovalStore{values: map[string]approval.PendingApproval{}}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "api-failure-run", TargetDir: target, Feature: "feature", Task: "task"})
	var processErr *ProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("rejected recorder API call must fail the invocation, got result=%+v err=%v", result, err)
	}
	if result.Outcome != "" || !strings.Contains(processErr.Err.Error(), "worker controller API recorder failed") || !strings.Contains(processErr.Diagnostics, workerAPIErrorMarker) {
		t.Fatalf("API failure was not preserved as infrastructure failure: result=%+v err=%+v", result, processErr)
	}
}

func TestProcessExitCodePreservesChildExitAndUnknownErrors(t *testing.T) {
	if got := processExitCode(nil); got != 0 {
		t.Fatalf("successful child exit code=%d, want 0", got)
	}
	if got := processExitCode(errors.New("start failed")); got != -1 {
		t.Fatalf("non-exit child error code=%d, want -1", got)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestProcessExitCodeHelper$")
	command.Env = append(os.Environ(), "AI_TEAM_EXIT_HELPER=1")
	if err := command.Run(); err == nil {
		t.Fatal("exit helper unexpectedly succeeded")
	} else if got := processExitCode(err); got != 7 {
		t.Fatalf("child exit code=%d, want 7", got)
	}
}

func TestProcessExitCodeHelper(t *testing.T) {
	if os.Getenv("AI_TEAM_EXIT_HELPER") == "1" {
		os.Exit(7)
	}
}

func httpClient() *http.Client { return &http.Client{Timeout: time.Second * 5} }

func TestWorkerControllerBriefAPIIsRunScopedAndDurable(t *testing.T) {
	target := t.TempDir()
	const intention = "Увеличить выручку"
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "brief-run", TargetDir: target, Task: intention, ExecutionID: strings.Repeat("7", ExecutionIDBytes*2)}
	const approvalID = "approval-1"
	const questions = "Какая аудитория?"
	const answer = "B2B"
	approvalStore := &apiApprovalStore{values: map[string]approval.PendingApproval{
		job.RunID + "/" + approvalID: workerQuestionApproval(job.RunID, approvalID, questions, answer, approval.StatusResolved),
	}}
	api, err := startWorkerAPIServer(job, &apiRecorderSpy{}, approvalStore)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { api.close() }()
	t.Setenv(WorkerAPIAddressEnv, "http://"+api.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, api.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	briefs := NewWorkerAPIBriefs(port)
	forged := workerAPIRequestBody(t, api.scope, "brief.create_initial", workerAPICall{A: "чужое намерение"}, "random", time.Now().UTC())
	if status := postWorkerAPIRequest(t, api, forged); status == http.StatusOK {
		t.Fatal("worker API accepted an initial brief that differs from the controller task")
	}
	versions, err := briefs.List(job.RunID)
	if err != nil || len(versions) != 0 {
		t.Fatalf("forged initial brief created durable versions: versions=%+v err=%v", versions, err)
	}
	created, err := briefs.CreateInitial(job.RunID, intention)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version.ID == "" || created.Version.SHA256 == "" || !strings.Contains(string(created.Content), intention) {
		t.Fatalf("invalid initial brief response: %+v", created)
	}
	versions, err = briefs.List(job.RunID)
	if err != nil || len(versions) != 1 || versions[0].ID != created.Version.ID {
		t.Fatalf("brief list=%+v err=%v", versions, err)
	}
	clarified, err := briefs.AppendClarification(job.RunID, approvalID, questions, answer)
	if err != nil {
		t.Fatal(err)
	}
	if clarified.Version.ParentID != created.Version.ID || clarified.Version.ID == created.Version.ID {
		t.Fatalf("clarification is not a new immutable child version: initial=%+v clarified=%+v", created.Version, clarified.Version)
	}
	api.close() // A fresh controller API instance must resume from durable storage.
	resumeStateStore, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := resumeStateStore.Create(lifecycle.State{
		RunID: job.RunID, Feature: "feature", TargetDir: target, Task: intention,
		Phase: lifecycle.PhaseRunning, NextStage: "analyst", ConfigSHA256: strings.Repeat("a", 64),
		WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	resumeJob := job
	resumeJob.Operation = OperationResume
	resumeJob.Task = ""
	canonicalTask, err := workerAPICanonicalTask(resumeJob, resumeStateStore)
	if err != nil || canonicalTask != intention {
		t.Fatalf("resume canonical task=%q err=%v", canonicalTask, err)
	}
	api, err = startWorkerAPIServerForTask(resumeJob, &apiRecorderSpy{}, approvalStore, canonicalTask)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(WorkerAPIAddressEnv, "http://"+api.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, api.token)
	port, err = NewWorkerAPIPort(resumeJob)
	if err != nil {
		t.Fatal(err)
	}
	briefs = NewWorkerAPIBriefs(port)
	resumeForgery := workerAPIRequestBody(t, api.scope, "brief.create_initial", workerAPICall{A: "чужое намерение"}, "random", time.Now().UTC())
	if status := postWorkerAPIRequest(t, api, resumeForgery); status == http.StatusOK {
		t.Fatal("resumed worker API accepted a forged initial brief")
	}
	resumedInitial, err := briefs.CreateInitial(resumeJob.RunID, intention)
	if err != nil || resumedInitial.Version.ID != created.Version.ID {
		t.Fatalf("resume initial brief should remain idempotent: version=%+v err=%v", resumedInitial.Version, err)
	}
	loaded, err := briefs.Read(resumeJob.RunID, clarified.Version.ID)
	if err != nil || loaded.Version.SHA256 != clarified.Version.SHA256 || loaded.Version.ParentID != created.Version.ID ||
		loaded.Version.ApprovalID != approvalID || string(loaded.Content) != string(clarified.Content) {
		t.Fatalf("persisted brief read=%+v err=%v", loaded, err)
	}
	retry, err := briefs.AppendClarification(resumeJob.RunID, approvalID, questions, answer)
	if err != nil || retry.Version.ID != clarified.Version.ID {
		t.Fatalf("retry of the same durable answer should be idempotent: version=%+v err=%v", retry.Version, err)
	}
	if _, err := briefs.List("other-run"); err == nil {
		t.Fatal("brief API accepted another run id")
	}
	if _, err := briefs.Read(job.RunID, "../outside"); err == nil {
		t.Fatal("brief API accepted a path instead of a version id")
	}
	path := filepath.Join(target, ".ai-team", "runs", job.RunID, filepath.FromSlash(clarified.Version.Path))
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("durable brief is not immutable: mode=%v err=%v", info, err)
	}
}

func TestWorkerControllerBriefAppendRequiresDurableProductOwnerClarification(t *testing.T) {
	target := t.TempDir()
	job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: "brief-auth-run", TargetDir: target, Task: "Grow B2B revenue", ExecutionID: strings.Repeat("8", ExecutionIDBytes*2)}
	const questions = "Какая аудитория?"
	const answer = "B2B"
	values := map[string]approval.PendingApproval{
		job.RunID + "/pending": workerQuestionApproval(job.RunID, "pending", questions, answer, approval.StatusPending),
		job.RunID + "/wrong-action": func() approval.PendingApproval {
			v := workerQuestionApproval(job.RunID, "wrong-action", questions, answer, approval.StatusResolved)
			v.ResolvedAction = "stop"
			v.Decisions[0].Action = "stop"
			return v
		}(),
		job.RunID + "/wrong-role": func() approval.PendingApproval {
			v := workerQuestionApproval(job.RunID, "wrong-role", questions, answer, approval.StatusResolved)
			v.Decisions[0].ActorRole = "developer"
			return v
		}(),
		job.RunID + "/wrong-questions": workerQuestionApproval(job.RunID, "wrong-questions", "Different durable questions", answer, approval.StatusResolved),
		job.RunID + "/wrong-answer":    workerQuestionApproval(job.RunID, "wrong-answer", questions, "Different durable answer", approval.StatusResolved),
		job.RunID + "/foreign": func() approval.PendingApproval {
			v := workerQuestionApproval("other-run", "foreign", questions, answer, approval.StatusResolved)
			return v
		}(),
	}
	approvalStore := &apiApprovalStore{values: values}
	api, err := startWorkerAPIServer(job, &apiRecorderSpy{}, approvalStore)
	if err != nil {
		t.Fatal(err)
	}
	defer api.close()
	t.Setenv(WorkerAPIAddressEnv, "http://"+api.listener.Addr().String())
	t.Setenv(WorkerAPITokenEnv, api.token)
	port, err := NewWorkerAPIPort(job)
	if err != nil {
		t.Fatal(err)
	}
	briefs := NewWorkerAPIBriefs(port)
	if _, err := briefs.CreateInitial(job.RunID, "Grow B2B revenue"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, approvalID, questions, answer string
	}{
		{name: "unknown approval", approvalID: "not-found", questions: questions, answer: answer},
		{name: "foreign run approval", approvalID: "foreign", questions: questions, answer: answer},
		{name: "pending approval", approvalID: "pending", questions: questions, answer: answer},
		{name: "wrong resolved action", approvalID: "wrong-action", questions: questions, answer: answer},
		{name: "non Product Owner answer", approvalID: "wrong-role", questions: questions, answer: answer},
		{name: "forged questions", approvalID: "wrong-questions", questions: questions, answer: answer},
		{name: "forged answer", approvalID: "wrong-answer", questions: questions, answer: answer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := briefs.AppendClarification(job.RunID, tc.approvalID, tc.questions, tc.answer); err == nil {
				t.Fatal("unverified clarification input was accepted")
			}
		})
	}
	versions, err := briefs.List(job.RunID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("failed clarification attempts must not append brief versions: versions=%+v err=%v", versions, err)
	}
}

func TestWorkerControllerAPIBriefAndJobBoundariesRejectUnsafeRunIDs(t *testing.T) {
	target := t.TempDir()
	unsafeIDs := []string{"", ".", "..", "../outside", "a/b", `a\b`, "/absolute", "a\x00b"}
	for _, runID := range unsafeIDs {
		t.Run(fmt.Sprintf("file brief %q", runID), func(t *testing.T) {
			if _, err := pipeline.NewFileBriefStore(target).CreateInitial(runID, "intention"); err == nil {
				t.Fatal("FileBriefStore accepted unsafe run id")
			}
		})
		t.Run(fmt.Sprintf("worker job %q", runID), func(t *testing.T) {
			job := Job{SchemaVersion: SchemaVersion, Operation: OperationStart, RunID: runID,
				TargetDir: target, Feature: "feature", Task: "task", ExecutionID: strings.Repeat("a", ExecutionIDBytes*2)}
			if err := job.Validate(target); err == nil {
				t.Fatal("worker job accepted unsafe run id")
			}
		})
		t.Run(fmt.Sprintf("controller dispatch %q", runID), func(t *testing.T) {
			api := &workerAPIServer{scope: workerAPIScope{RunID: runID}}
			if _, err := api.dispatch("brief.create_initial", workerAPICall{A: "intention"}); err == nil {
				t.Fatal("controller dispatch accepted unsafe run scope")
			}
		})
	}
	// Directory components are checked as well as the run ID; a valid ID must
	// not let a pre-existing symlink redirect controller-owned writes outside
	// the target.
	symlinkTarget := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team", "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkTarget, filepath.Join(target, ".ai-team", "runs", "symlink-run")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := pipeline.NewFileBriefStore(target).CreateInitial("symlink-run", "intention"); err == nil {
		t.Fatal("FileBriefStore followed a symlinked run directory")
	}
}
