package worker

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

func TestWorkerAPIEventAppendRequestEnvelopeBoundary(t *testing.T) {
	if workerAPIMaxEventAppendEnvelope >= evidence.MaxEventLogSize || workerAPIMaxEventAppendPayload >= workerAPIMaxEventAppendEnvelope {
		t.Fatalf("event request bounds must leave room below the journal limit: envelope=%d payload=%d journal=%d",
			workerAPIMaxEventAppendEnvelope, workerAPIMaxEventAppendPayload, evidence.MaxEventLogSize)
	}
	if workerAPIMaxEventAppendPayload < 24<<20 {
		t.Fatalf("event payload limit too small for worst-case JSON escaping of runtime diagnostics: %d", workerAPIMaxEventAppendPayload)
	}
	for _, tc := range []struct {
		name   string
		method string
		size   int
		want   bool
	}{
		{name: "event at envelope cap", method: "event_log.append", size: workerAPIMaxEventAppendEnvelope, want: true},
		{name: "event above envelope cap", method: "event_log.append", size: workerAPIMaxEventAppendEnvelope + 1},
		{name: "normal API above normal cap", method: "approval.list", size: workerAPIMaxBody + 1},
		{name: "manifest at envelope cap", method: "attempt_manifest.write", size: workerAPIMaxAttemptManifestEnvelope, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerAPIRequestWithinLimit(tc.method, tc.size); got != tc.want {
				t.Fatalf("workerAPIRequestWithinLimit(%q, %d)=%v, want %v", tc.method, tc.size, got, tc.want)
			}
		})
	}
}

func TestAppendVerifiedClarificationRejectsMalformedDurableApprovals(t *testing.T) {
	const runID = "clarification-validation-run"
	const approvalID = "approval-1"
	const questions = "Какая аудитория?"
	const answer = "B2B"
	valid := workerQuestionApproval(runID, approvalID, questions, answer, approval.StatusResolved)

	cases := []struct {
		name   string
		change func(*approval.PendingApproval)
	}{
		{name: "identity", change: func(v *approval.PendingApproval) { v.ID = "other-id" }},
		{name: "wrong stage target", change: func(v *approval.PendingApproval) { v.FromStage = "reviewer" }},
		{name: "wrong trigger", change: func(v *approval.PendingApproval) { v.Trigger = "manual" }},
		{name: "missing target", change: func(v *approval.PendingApproval) { delete(v.Targets, "answer_questions") }},
		{name: "missing action", change: func(v *approval.PendingApproval) { v.Actions = []string{"stop"} }},
		{name: "missing required role", change: func(v *approval.PendingApproval) { v.RequiredRoles = []string{"operator"} }},
		{name: "malformed payload", change: func(v *approval.PendingApproval) { v.Payload = []byte("{") }},
		{name: "empty questions", change: func(v *approval.PendingApproval) { v.Payload = []byte(`{"kind":"questions","markdown":"  "}`) }},
		{name: "missing durable answer", change: func(v *approval.PendingApproval) { v.Decisions = nil }},
		{name: "wrong decision approval", change: func(v *approval.PendingApproval) { v.Decisions[0].ApprovalID = "another" }},
		{name: "missing actor", change: func(v *approval.PendingApproval) { v.Decisions[0].ActorID = "" }},
		{name: "wrong subject", change: func(v *approval.PendingApproval) { v.Decisions[0].SubjectHash = "other" }},
		{name: "missing timestamp", change: func(v *approval.PendingApproval) { v.Decisions[0].DecidedAt = time.Time{} }},
		{name: "empty comment", change: func(v *approval.PendingApproval) { v.Decisions[0].Comment = " " }},
		{name: "invalid later decision", change: func(v *approval.PendingApproval) {
			later := v.Decisions[0]
			later.DecidedAt = later.DecidedAt.Add(-time.Second)
			later.ActorID = ""
			v.Decisions = append(v.Decisions, later)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := valid
			value.Decisions = append([]approval.Decision(nil), valid.Decisions...)
			value.Actions = append([]string(nil), valid.Actions...)
			value.RequiredRoles = append([]string(nil), valid.RequiredRoles...)
			value.Targets = map[string]string{"answer_questions": "questioner", "stop": "$stop"}
			tc.change(&value)
			store := &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: value}}
			server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: store}
			if _, err := server.appendVerifiedClarification(approvalID, questions, answer); err == nil {
				t.Fatal("malformed durable clarification approval was accepted")
			}
		})
	}

	loadFailure := errors.New("approval database unavailable")
	server := &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{loadErr: loadFailure}}
	if _, err := server.appendVerifiedClarification(approvalID, questions, answer); err == nil || !strings.Contains(err.Error(), loadFailure.Error()) {
		t.Fatalf("approval load failure was not propagated: %v", err)
	}
	server = &workerAPIServer{scope: workerAPIScope{RunID: runID}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{runID + "/" + approvalID: valid}}}
	if _, err := server.appendVerifiedClarification("", questions, answer); err == nil {
		t.Fatal("empty approval ID accepted")
	}
	if _, err := server.appendVerifiedClarification(approvalID, "forged questions", answer); err == nil {
		t.Fatal("questions differing from the durable payload accepted")
	}
	if _, err := server.appendVerifiedClarification(approvalID, questions, "forged answer"); err == nil {
		t.Fatal("answer differing from the durable decision accepted")
	}
}

func TestWorkerAPIPortRejectsInvalidTransportAndResponseCases(t *testing.T) {
	job := Job{RunID: "api-port-errors", Operation: OperationStart, ExecutionID: strings.Repeat("a", ExecutionIDBytes*2), TargetDir: t.TempDir()}
	t.Setenv(WorkerAPIAddressEnv, "")
	t.Setenv(WorkerAPITokenEnv, "token")
	t.Setenv(WorkerAPISocketEnv, "")
	if _, err := NewWorkerAPIPort(job); err == nil {
		t.Fatal("missing API address was accepted for a valid run")
	}
	t.Setenv(WorkerAPIAddressEnv, "http://127.0.0.1:1234")
	t.Setenv(WorkerAPITokenEnv, "")
	if _, err := NewWorkerAPIPort(job); err == nil {
		t.Fatal("missing API token was accepted for a valid run")
	}
	t.Setenv(WorkerAPITokenEnv, "token")
	for _, tc := range []struct {
		name, address, socket string
		setSocket             bool
	}{
		{name: "unix without socket", address: "http://unix"},
		{name: "relative unix socket", address: "http://unix", socket: "relative.sock", setSocket: true},
		{name: "unclean unix socket", address: "http://unix", socket: "/tmp/a/../api.sock", setSocket: true},
		{name: "tcp plus unix socket", address: "http://127.0.0.1:1234", socket: "/tmp/api.sock", setSocket: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(WorkerAPIAddressEnv, tc.address)
			if tc.setSocket {
				t.Setenv(WorkerAPISocketEnv, tc.socket)
			} else {
				t.Setenv(WorkerAPISocketEnv, "")
			}
			if _, err := NewWorkerAPIPort(job); err == nil {
				t.Fatal("invalid transport configuration was accepted")
			}
		})
	}

	for _, tc := range []struct {
		name       string
		handler    http.HandlerFunc
		value, out any
		address    string
	}{
		{name: "invalid request JSON value", value: make(chan int), address: "http://127.0.0.1:1"},
		{name: "invalid URL", value: workerAPICall{}, address: "://invalid"},
		{name: "HTTP error", value: workerAPICall{}, handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "denied", http.StatusForbidden) }},
		{name: "invalid response JSON", value: workerAPICall{}, out: new(map[string]string), handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not-json")) }},
		{name: "oversized response", value: workerAPICall{}, out: new(map[string]string), handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", workerAPIMaxBody+1)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := tc.address
			if tc.handler != nil {
				server := httptest.NewServer(tc.handler)
				defer server.Close()
				address = server.URL
			}
			port := &workerAPIPort{address: address, token: "token", scope: workerAPIScope{RunID: job.RunID}, client: httpClient()}
			if err := port.call("test", tc.value, tc.out); err == nil {
				t.Fatal("invalid worker API call unexpectedly succeeded")
			}
		})
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	port := &workerAPIPort{address: closed.URL, token: "token", scope: workerAPIScope{RunID: job.RunID}, client: httpClient()}
	if err := port.call("closed", workerAPICall{}, nil); err == nil {
		t.Fatal("closed controller transport did not return an error")
	}

	readFailure := errors.New("response stream interrupted")
	port = &workerAPIPort{address: "http://controller", token: "token", scope: workerAPIScope{RunID: job.RunID}, client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: errorReadCloser{err: readFailure}, Header: make(http.Header)}, nil
	})}}
	if err := port.call("read-error", workerAPICall{}, nil); !errors.Is(err, readFailure) {
		t.Fatalf("response read error not propagated: %v", err)
	}
	if err := port.callWithRandom("nonce-error", workerAPICall{}, nil, strings.NewReader("short")); err == nil {
		t.Fatal("short nonce source was accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type errorReadCloser struct{ err error }

func (r errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (errorReadCloser) Close() error               { return nil }

func TestTaskBoundBriefStoreRequiresCanonicalTask(t *testing.T) {
	target := t.TempDir()
	base := pipeline.NewFileBriefStore(target)
	for _, tc := range []struct {
		name             string
		store            *taskBoundBriefStore
		runID, intention string
	}{
		{name: "nil receiver", runID: "run", intention: "task"},
		{name: "nil store", store: &taskBoundBriefStore{expectedTask: "task"}, runID: "run", intention: "task"},
		{name: "empty canonical task", store: &taskBoundBriefStore{BriefStore: base}, runID: "run", intention: "task"},
		{name: "mismatched task", store: &taskBoundBriefStore{BriefStore: base, expectedTask: "canonical"}, runID: "run", intention: "forged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *taskBoundBriefStore = tc.store
			if tc.name == "nil receiver" {
				got = nil
			}
			if _, err := got.CreateInitial(tc.runID, tc.intention); err == nil {
				t.Fatal("brief creation without the canonical task was accepted")
			}
		})
	}
	store := &taskBoundBriefStore{BriefStore: base, expectedTask: "canonical"}
	if _, err := store.CreateInitial("canonical-run", "canonical"); err != nil {
		t.Fatalf("canonical initial task rejected: %v", err)
	}
}

func TestWorkerControllerAPIServerClosesListenerForInvalidRunID(t *testing.T) {
	setAcceptDeadline := func(listener net.Listener) {
		t.Helper()
		tcp, ok := listener.(*net.TCPListener)
		if !ok {
			t.Fatalf("expected TCP listener, got %T", listener)
		}
		if err := tcp.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatalf("set listener accept deadline: %v", err)
		}
	}
	assertClosed := func(listener net.Listener) {
		t.Helper()
		if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("listener should be closed, Accept error=%v", err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	setAcceptDeadline(listener)
	if _, err := serveWorkerAPI(Job{RunID: "../invalid"}, &apiRecorderSpy{}, &apiApprovalStore{}, listener, "", strings.NewReader(strings.Repeat("x", 32)), "task"); err == nil {
		t.Fatal("controller API accepted an unsafe run id")
	}
	assertClosed(listener)
	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	setAcceptDeadline(listener)
	if _, err := serveWorkerAPI(Job{RunID: "valid-run"}, &apiRecorderSpy{}, &apiApprovalStore{}, listener, "", strings.NewReader("short"), "task"); err == nil {
		t.Fatal("controller API started without a complete cryptographic nonce")
	}
	assertClosed(listener)
}

func TestWorkerBriefClientRejectsMismatchedRunForEveryOperation(t *testing.T) {
	port := &workerAPIPort{scope: workerAPIScope{RunID: "scoped-run"}}
	briefs := NewWorkerAPIBriefs(port)
	if _, err := briefs.CreateInitial("other-run", "task"); err == nil {
		t.Fatal("create accepted another run")
	}
	if _, err := briefs.AppendClarification("other-run", "approval", pipeline.ClarificationProvenance{Stage: "stage", ActorID: "actor", ActorRole: "role"}, "question", "answer"); err == nil {
		t.Fatal("append accepted another run")
	}
	if _, err := briefs.AppendClarification("scoped-run", "approval", pipeline.ClarificationProvenance{}, "question", "answer"); err == nil {
		t.Fatal("append accepted missing durable clarification provenance")
	}
	if _, err := briefs.List("other-run"); err == nil {
		t.Fatal("list accepted another run")
	}
	if _, err := briefs.Read("other-run", "version-id"); err == nil {
		t.Fatal("read accepted another run")
	}
}

func TestWorkerControllerDispatchRejectsApprovalMismatchAndLifecycleStoreErrors(t *testing.T) {
	target := t.TempDir()
	const runID = "dispatch-errors-run"
	server := &workerAPIServer{
		scope:    workerAPIScope{RunID: runID, TargetDir: target},
		recorder: &apiRecorderSpy{}, approvals: &apiApprovalStore{values: map[string]approval.PendingApproval{}},
		lifecycle: lifecycleLoadErrorPort{err: errors.New("checkpoint read failed")},
	}
	if _, err := server.dispatch("approval.create", workerAPICall{Approval: approval.PendingApproval{RunID: "other-run"}}); err == nil {
		t.Fatal("approval create accepted a different run ID")
	}
	if _, err := server.dispatch("approval.decide", workerAPICall{}); err == nil {
		t.Fatal("worker was allowed to decide human approval")
	}
	state := lifecycle.State{RunID: runID, TargetDir: target, Task: "task", Phase: lifecycle.PhaseRunning}
	if _, err := server.dispatch("lifecycle.save", workerAPICall{Previous: state, Lifecycle: state}); err == nil || !strings.Contains(err.Error(), "checkpoint read failed") {
		t.Fatalf("lifecycle save did not propagate controller read error: %v", err)
	}
}

func TestWorkerAPICanonicalTaskHandlesResumeAndRecoveryFailures(t *testing.T) {
	job := Job{RunID: "canonical-task-run", TargetDir: t.TempDir(), Task: "original task"}
	if got, err := workerAPICanonicalTask(Job{Operation: OperationStart, Task: "start task"}, nil); err != nil || got != "start task" {
		t.Fatalf("start canonical task=%q err=%v", got, err)
	}
	if got, err := workerAPICanonicalTask(Job{Operation: OperationCancel, Task: "ignored"}, nil); err != nil || got != "" {
		t.Fatalf("cancel canonical task=%q err=%v", got, err)
	}
	for _, op := range []Operation{OperationResume, OperationRecover} {
		job.Operation = op
		if _, err := workerAPICanonicalTask(job, nil); err == nil {
			t.Fatalf("%s accepted a missing lifecycle store", op)
		}
	}

	missing := lifecycleLoadErrorPort{err: os.ErrNotExist}
	job.Operation = OperationRecover
	if got, err := workerAPICanonicalTask(job, missing); err != nil || got != job.Task {
		t.Fatalf("recover admission should retain queued task when state is absent: task=%q err=%v", got, err)
	}
	job.Operation = OperationResume
	if _, err := workerAPICanonicalTask(job, missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume swallowed missing lifecycle state: %v", err)
	}
	loadErr := errors.New("controller store offline")
	if _, err := workerAPICanonicalTask(job, lifecycleLoadErrorPort{err: loadErr}); !errors.Is(err, loadErr) {
		t.Fatalf("lifecycle load failure was not propagated: %v", err)
	}

	wrongRun := wrongTargetLifecyclePort{}
	if _, err := workerAPICanonicalTask(job, wrongRun); err == nil {
		t.Fatal("canonical task accepted a persisted target mismatch")
	}
	state := lifecycle.State{RunID: job.RunID, TargetDir: job.TargetDir, Task: "  "}
	if _, err := workerAPICanonicalTask(job, singleStateLifecyclePort{state: state}); err == nil {
		t.Fatal("canonical task accepted an empty persisted task")
	}
	state.Task = "persisted task"
	if got, err := workerAPICanonicalTask(job, singleStateLifecyclePort{state: state}); err != nil || got != state.Task {
		t.Fatalf("canonical task=%q err=%v", got, err)
	}
}

type singleStateLifecyclePort struct{ state lifecycle.State }

func (singleStateLifecyclePort) Create(lifecycle.State) error                { return nil }
func (p singleStateLifecyclePort) Load(string) (lifecycle.State, error)      { return p.state, nil }
func (singleStateLifecyclePort) Save(lifecycle.State, lifecycle.State) error { return nil }
