package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

type apiApprovalStore struct {
	values    map[string]approval.PendingApproval
	createErr error
}

func (s *apiApprovalStore) Create(v approval.PendingApproval) (approval.PendingApproval, error) {
	if s.createErr != nil {
		return approval.PendingApproval{}, s.createErr
	}
	if v.RunID == "" {
		return v, os.ErrInvalid
	}
	s.values[v.RunID+"/"+v.ID] = v
	return v, nil
}
func (s *apiApprovalStore) Load(run, id string) (approval.PendingApproval, error) {
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
			payload, _ := json.Marshal(tc.call)
			body, _ := json.Marshal(workerAPIRequest{workerAPIScope: tc.scope, Method: tc.method, Payload: payload})
			resp, err := port.client.Post(port.address+"/v1/call", "application/json", strings.NewReader(string(body)))
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
	t.Setenv("AI_TEAM_WORKER_ARGS_MARKER", marker)
	t.Setenv("AI_TEAM_WORKER_TEST_MODE", "echo")
	allowWorkerTestEnvironment(t, "AI_TEAM_WORKER_ARGS_MARKER", "AI_TEAM_WORKER_TEST_MODE")
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
