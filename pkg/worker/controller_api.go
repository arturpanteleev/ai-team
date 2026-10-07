package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

const (
	workerAPIAddressEnv  = "AI_TEAM_WORKER_API_ADDRESS"
	workerAPISocketEnv   = "AI_TEAM_WORKER_API_SOCKET"
	workerAPITokenEnv    = "AI_TEAM_WORKER_API_TOKEN"
	workerAPIMaxBody     = 1 << 20
	workerAPIErrorMarker = "AI_TEAM_WORKER_API_RECORDER_ERROR:"
	workerAPIRequestTTL  = 30 * time.Second
	workerAPIFutureSkew  = 5 * time.Second
	workerAPIMaxNonces   = 4096
)

const WorkerAPIAddressEnv = workerAPIAddressEnv
const WorkerAPISocketEnv = workerAPISocketEnv
const WorkerAPITokenEnv = workerAPITokenEnv

type workerAPIScope struct {
	RunID       string    `json:"run_id"`
	Operation   Operation `json:"operation"`
	ExecutionID string    `json:"execution_id"`
	TargetDir   string    `json:"target_dir"`
}
type workerAPIRequest struct {
	workerAPIScope
	Method   string          `json:"method"`
	Payload  json.RawMessage `json:"payload"`
	Nonce    string          `json:"nonce"`
	IssuedAt time.Time       `json:"issued_at"`
}
type workerAPICall struct {
	RunID        string                   `json:"run_id,omitempty"`
	A            string                   `json:"a,omitempty"`
	B            string                   `json:"b,omitempty"`
	C            string                   `json:"c,omitempty"`
	Index        int                      `json:"index,omitempty"`
	At           time.Time                `json:"at,omitempty"`
	Data         map[string]any           `json:"data,omitempty"`
	IDs          []string                 `json:"ids,omitempty"`
	Stage        notifier.StageResult     `json:"stage,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Approval     approval.PendingApproval `json:"approval,omitempty"`
	Lifecycle    lifecycle.State          `json:"lifecycle,omitempty"`
	Previous     lifecycle.State          `json:"previous_lifecycle,omitempty"`
	BriefVersion pipeline.BriefVersion    `json:"brief_version,omitempty"`
	BriefContent []byte                   `json:"brief_content,omitempty"`
}
type workerQuestionPayload struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
}

type taskBoundBriefStore struct {
	pipeline.BriefStore
	expectedTask string
}

func (s *taskBoundBriefStore) CreateInitial(runID, intention string) (pipeline.BriefDocument, error) {
	if s == nil || s.BriefStore == nil || s.expectedTask == "" || intention != s.expectedTask {
		return pipeline.BriefDocument{}, errors.New("initial brief must match the controller's canonical task")
	}
	return s.BriefStore.CreateInitial(runID, s.expectedTask)
}

type workerApprovalPort interface {
	Create(approval.PendingApproval) (approval.PendingApproval, error)
	Load(string, string) (approval.PendingApproval, error)
	List(string) ([]approval.PendingApproval, error)
}
type workerAPIServer struct {
	scope      workerAPIScope
	token      string
	listener   net.Listener
	socketPath string
	server     *http.Server
	recorder   pipeline.Recorder
	approvals  workerApprovalPort
	lifecycle  lifecycle.StorePort
	briefs     pipeline.BriefStore
	briefTask  string
	dispatchMu sync.Mutex
	nonceMu    sync.Mutex
	nonces     map[string]time.Time
}

// startWorkerAPIServer creates a per-execution loopback capability for
// unsandboxed launches. Sandboxed Linux launches use startWorkerAPIServerUnix.
// The worker
// can report pipeline events and request/read approvals only for this run.
func startWorkerAPIServer(job Job, recorder pipeline.Recorder, approvals workerApprovalPort) (*workerAPIServer, error) {
	return startWorkerAPIServerForTask(job, recorder, approvals, job.Task)
}

func startWorkerAPIServerForTask(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, expectedTask string) (*workerAPIServer, error) {
	return startWorkerAPIServerWithTaskAndListener(job, recorder, approvals, func() (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}, rand.Reader, expectedTask)
}

func startWorkerAPIServerWithListener(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, listen func() (net.Listener, error), random io.Reader) (*workerAPIServer, error) {
	return startWorkerAPIServerWithTaskAndListener(job, recorder, approvals, listen, random, job.Task)
}

func startWorkerAPIServerWithTaskAndListener(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, listen func() (net.Listener, error), random io.Reader, expectedTask string) (*workerAPIServer, error) {
	if recorder == nil || approvals == nil {
		return nil, errors.New("worker controller API requires recorder and approval ports")
	}
	listener, err := listen()
	if err != nil {
		return nil, err
	}
	return serveWorkerAPI(job, recorder, approvals, listener, "", random, expectedTask)
}

func startWorkerAPIServerUnix(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath string) (*workerAPIServer, error) {
	return startWorkerAPIServerUnixForTask(job, recorder, approvals, socketPath, job.Task)
}

func startWorkerAPIServerUnixForTask(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath, expectedTask string) (*workerAPIServer, error) {
	return startWorkerAPIServerUnixWithTask(job, recorder, approvals, socketPath, net.Listen, os.Chmod, rand.Reader, expectedTask)
}

func startWorkerAPIServerUnixWith(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath string, listen func(string, string) (net.Listener, error), chmod func(string, os.FileMode) error, random io.Reader) (*workerAPIServer, error) {
	return startWorkerAPIServerUnixWithTask(job, recorder, approvals, socketPath, listen, chmod, random, job.Task)
}

func startWorkerAPIServerUnixWithTask(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, socketPath string, listen func(string, string) (net.Listener, error), chmod func(string, os.FileMode) error, random io.Reader, expectedTask string) (*workerAPIServer, error) {
	if recorder == nil || approvals == nil {
		return nil, errors.New("worker controller API requires recorder and approval ports")
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return nil, errors.New("worker controller API socket path must be absolute and clean")
	}
	listener, err := listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on worker controller API socket: %w", err)
	}
	if err := chmod(socketPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("secure worker controller API socket: %w", err)
	}
	return serveWorkerAPI(job, recorder, approvals, listener, socketPath, random, expectedTask)
}

func serveWorkerAPI(job Job, recorder pipeline.Recorder, approvals workerApprovalPort, listener net.Listener, socketPath string, random io.Reader, expectedTask string) (*workerAPIServer, error) {
	if err := evidence.ValidateRunID(job.RunID); err != nil {
		_ = listener.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, fmt.Errorf("worker controller API run id: %w", err)
	}
	var nonce [32]byte
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		_ = listener.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil, err
	}
	api := &workerAPIServer{scope: workerAPIScope{RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, TargetDir: job.TargetDir}, token: hex.EncodeToString(nonce[:]), listener: listener, socketPath: socketPath, recorder: recorder, approvals: approvals, briefs: &taskBoundBriefStore{BriefStore: pipeline.NewFileBriefStore(job.TargetDir), expectedTask: expectedTask}, briefTask: expectedTask, nonces: make(map[string]time.Time)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/call", api.handle)
	api.server = &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = api.server.Serve(listener) }()
	return api, nil
}
func (s *workerAPIServer) close() {
	if s != nil && s.server != nil {
		_ = s.server.Close()
		if s.socketPath != "" {
			_ = os.Remove(s.socketPath)
		}
	}
}
func (s *workerAPIServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/call" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, workerAPIMaxBody+1))
	if err != nil || len(data) > workerAPIMaxBody {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request workerAPIRequest
	if strictjson.Unmarshal(data, workerAPIMaxBody, &request) != nil || request.workerAPIScope != s.scope {
		http.Error(w, "invalid invocation scope", http.StatusForbidden)
		return
	}
	var call workerAPICall
	if len(request.Payload) > 0 && strictjson.Unmarshal(request.Payload, workerAPIMaxBody, &call) != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !s.acceptRequest(time.Now(), request.Nonce, request.IssuedAt) {
		http.Error(w, "invalid, expired, replayed, or exhausted request nonce", http.StatusUnauthorized)
		return
	}
	result, err := s.dispatch(request.Method, call)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result == nil {
		result = struct{}{}
	}
	_ = json.NewEncoder(w).Encode(result)
}

// acceptRequest binds one authenticated API operation to a fresh, short-lived
// nonce. The map is capped per invocation; when it is full, requests fail
// closed until expired entries can be reclaimed.
func (s *workerAPIServer) acceptRequest(now time.Time, nonce string, issuedAt time.Time) bool {
	if len(nonce) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(nonce)
	if err != nil || len(decoded) != 32 || issuedAt.IsZero() || !issuedAt.After(now.Add(-workerAPIRequestTTL)) || issuedAt.After(now.Add(workerAPIFutureSkew)) {
		return false
	}
	// Hex decoding accepts either case. Key by the decoded bytes' canonical
	// representation so changing hex letter case cannot bypass replay checks.
	nonceKey := hex.EncodeToString(decoded)
	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	for existing, expiresAt := range s.nonces {
		if !now.Before(expiresAt) {
			delete(s.nonces, existing)
		}
	}
	if _, exists := s.nonces[nonceKey]; exists || len(s.nonces) >= workerAPIMaxNonces {
		return false
	}
	s.nonces[nonceKey] = issuedAt.Add(workerAPIRequestTTL)
	return true
}

func (s *workerAPIServer) appendVerifiedClarification(approvalID, questions, submittedAnswer string) (any, error) {
	if approvalID == "" {
		return nil, errors.New("clarification approval id is required")
	}
	value, err := s.approvals.Load(s.scope.RunID, approvalID)
	if err != nil {
		return nil, fmt.Errorf("load clarification approval: %w", err)
	}
	if value.RunID != s.scope.RunID || value.ID != approvalID {
		return nil, errors.New("clarification approval identity mismatch")
	}
	if value.Status != approval.StatusResolved || value.ResolvedAction != "answer_questions" || value.ResolvedAt.IsZero() {
		return nil, errors.New("clarification requires a resolved answer_questions approval")
	}
	if value.FromStage != "analyst" || value.Trigger != "graph_outcome:blocked" ||
		value.Targets["answer_questions"] != "analyst" || !containsWorkerString(value.Actions, "answer_questions") ||
		!containsWorkerString(value.RequiredRoles, "product_owner") {
		return nil, errors.New("approval is not a Product Owner analyst clarification")
	}
	var payload workerQuestionPayload
	if len(value.Payload) == 0 || json.Unmarshal(value.Payload, &payload) != nil ||
		payload.Kind != "questions" || strings.TrimSpace(payload.Markdown) == "" {
		return nil, errors.New("clarification approval has no durable analyst questions")
	}
	if questions != payload.Markdown {
		return nil, errors.New("clarification questions do not match the durable approval")
	}
	var answer string
	for _, decision := range value.Decisions {
		if decision.Action != "answer_questions" || decision.ActorRole != "product_owner" {
			continue
		}
		if decision.ApprovalID != value.ID || decision.ActorID == "" ||
			decision.SubjectHash != value.SubjectHash || decision.DecidedAt.IsZero() || strings.TrimSpace(decision.Comment) == "" {
			return nil, errors.New("clarification decision is invalid")
		}
		if answer != "" {
			return nil, errors.New("clarification approval has multiple Product Owner answers")
		}
		answer = strings.TrimSpace(decision.Comment)
	}
	if answer == "" {
		return nil, errors.New("clarification approval has no durable Product Owner answer")
	}
	if strings.TrimSpace(submittedAnswer) != answer {
		return nil, errors.New("clarification answer does not match the durable decision")
	}
	return s.briefs.AppendClarification(s.scope.RunID, value.ID, payload.Markdown, answer)
}

func containsWorkerString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (s *workerAPIServer) dispatch(method string, c workerAPICall) (any, error) {
	// Recorder implementations such as web.StoreRecorder keep per-run sequence
	// and stage state. Requests from one child can arrive concurrently, so keep
	// each invocation's projection ordered and race-free.
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if err := evidence.ValidateRunID(s.scope.RunID); err != nil {
		return nil, fmt.Errorf("worker API scope run id: %w", err)
	}
	if c.RunID != "" && c.RunID != s.scope.RunID {
		return nil, errors.New("worker API run mismatch")
	}
	switch method {
	case "brief.create_initial":
		if s.briefTask == "" || c.A != s.briefTask {
			return nil, errors.New("initial brief must match the controller's canonical task")
		}
		return s.briefs.CreateInitial(s.scope.RunID, c.A)
	case "brief.append_clarification":
		return s.appendVerifiedClarification(c.A, c.B, c.C)
	case "brief.list":
		return s.briefs.List(s.scope.RunID)
	case "brief.read":
		return s.briefs.Read(s.scope.RunID, c.A)
	case "approval.create":
		if c.Approval.RunID != "" && c.Approval.RunID != s.scope.RunID {
			return nil, errors.New("approval run mismatch")
		}
		c.Approval.RunID = s.scope.RunID
		return s.approvals.Create(c.Approval)
	case "approval.load":
		return s.approvals.Load(s.scope.RunID, c.A)
	case "approval.list":
		return s.approvals.List(s.scope.RunID)
	case "approval.decide", "approval.resolve_deferred":
		return nil, errors.New("worker API cannot make human approval decisions")
	case "lifecycle.create":
		if s.lifecycle == nil {
			return nil, errors.New("worker lifecycle API unavailable")
		}
		if c.Lifecycle.RunID != s.scope.RunID || c.Lifecycle.TargetDir != s.scope.TargetDir || c.Lifecycle.TargetDir == "" {
			return nil, errors.New("lifecycle create identity mismatch")
		}
		return nil, s.lifecycle.Create(c.Lifecycle)
	case "lifecycle.load":
		if s.lifecycle == nil {
			return nil, errors.New("worker lifecycle API unavailable")
		}
		if c.A != s.scope.RunID {
			return nil, errors.New("lifecycle load run mismatch")
		}
		state, err := s.lifecycle.Load(s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if state.RunID != s.scope.RunID || state.TargetDir != s.scope.TargetDir {
			return nil, errors.New("lifecycle store returned another run or target")
		}
		return state, nil
	case "lifecycle.save":
		if s.lifecycle == nil {
			return nil, errors.New("worker lifecycle API unavailable")
		}
		if c.Previous.RunID != s.scope.RunID || c.Lifecycle.RunID != s.scope.RunID ||
			c.Previous.TargetDir != s.scope.TargetDir || c.Lifecycle.TargetDir != s.scope.TargetDir || c.Previous.TargetDir == "" {
			return nil, errors.New("lifecycle save identity mismatch")
		}
		current, err := s.lifecycle.Load(s.scope.RunID)
		if err != nil {
			return nil, err
		}
		if current.RunID != s.scope.RunID || current.TargetDir != s.scope.TargetDir {
			return nil, errors.New("lifecycle store returned another run or target")
		}
		if !sameLifecycleState(current, c.Previous) {
			return nil, errors.New("stale lifecycle state: previous checkpoint does not match controller state")
		}
		return nil, s.lifecycle.Save(c.Previous, c.Lifecycle)
	case "recorder.run_started":
		s.recorder.RunStarted(s.scope.RunID, c.A, c.B, c.At)
	case "recorder.run_resumed":
		s.recorder.RunResumed(s.scope.RunID, c.At)
	case "recorder.run_attached":
		s.recorder.RunAttached(s.scope.RunID)
	case "recorder.run_paused":
		s.recorder.RunPaused(s.scope.RunID, c.A, c.At)
	case "recorder.run_canceled":
		s.recorder.RunCanceled(s.scope.RunID, c.At)
	case "recorder.approval_requested":
		s.recorder.ApprovalRequested(s.scope.RunID, c.A, c.B, c.At, c.Data)
	case "recorder.transition_selected":
		s.recorder.TransitionSelected(s.scope.RunID, c.A, c.At, c.Data)
	case "recorder.stage_started":
		s.recorder.StageStarted(s.scope.RunID, c.A, c.B, c.Index, c.At)
	case "recorder.stage_finished":
		c.Stage.RunID = s.scope.RunID
		if c.Error != "" {
			c.Stage.Err = errors.New(c.Error)
		}
		s.recorder.StageFinished(c.Stage)
	case "recorder.attempts_invalidated":
		s.recorder.AttemptsInvalidated(s.scope.RunID, c.IDs, c.At)
	case "recorder.run_finished":
		s.recorder.RunFinished(s.scope.RunID, c.A, c.At)
	default:
		return nil, fmt.Errorf("worker API method %q is not allowed", method)
	}
	return nil, nil
}

// sameLifecycleState compares the complete persisted checkpoint. time.Time.Equal
// avoids treating equivalent instants with different location metadata as a
// stale checkpoint after a JSON round trip.
func sameLifecycleState(a, b lifecycle.State) bool {
	return a.SchemaVersion == b.SchemaVersion && a.RunID == b.RunID &&
		a.Feature == b.Feature && a.TargetDir == b.TargetDir && a.Task == b.Task &&
		a.Phase == b.Phase && a.NextStage == b.NextStage &&
		a.PendingApprovalID == b.PendingApprovalID && a.AttemptOrdinal == b.AttemptOrdinal &&
		a.ConfigSHA256 == b.ConfigSHA256 && a.WorkflowSHA256 == b.WorkflowSHA256 &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

type workerAPIPort struct {
	address, token string
	scope          workerAPIScope
	client         *http.Client
}

type WorkerAPIPort = workerAPIPort

func newWorkerAPIPort(job Job) (*workerAPIPort, error) {
	if err := evidence.ValidateRunID(job.RunID); err != nil {
		return nil, fmt.Errorf("worker controller API run id: %w", err)
	}
	address, okA := os.LookupEnv(workerAPIAddressEnv)
	token, okT := os.LookupEnv(workerAPITokenEnv)
	socketPath, hasSocket := os.LookupEnv(workerAPISocketEnv)
	if !okA || !okT || token == "" {
		return nil, errors.New("worker controller API environment is missing or invalid")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	switch {
	case strings.HasPrefix(address, "http://127.0.0.1:") && !hasSocket:
		// Compatibility transport for non-bubblewrap worker launches.
	case address == "http://unix" && hasSocket && filepath.IsAbs(socketPath) && filepath.Clean(socketPath) == socketPath:
		client.Transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}}
	default:
		return nil, errors.New("worker controller API environment is missing or invalid")
	}
	return &workerAPIPort{address: address, token: token, scope: workerAPIScope{RunID: job.RunID, Operation: job.Operation, ExecutionID: job.ExecutionID, TargetDir: job.TargetDir}, client: client}, nil
}

func NewWorkerAPIPort(job Job) (*WorkerAPIPort, error) { return newWorkerAPIPort(job) }
func (p *workerAPIPort) call(method string, value, out any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var nonceBytes [32]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return err
	}
	data, err := json.Marshal(workerAPIRequest{workerAPIScope: p.scope, Method: method, Payload: payload, Nonce: hex.EncodeToString(nonceBytes[:]), IssuedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, p.address+"/v1/call", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, workerAPIMaxBody+1))
	if err != nil {
		return err
	}
	if len(body) > workerAPIMaxBody {
		return errors.New("worker API response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("worker API %s: %s", method, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

type workerAPIRecorder struct {
	port     *workerAPIPort
	mu       sync.Mutex
	firstErr error
}
type WorkerAPIRecorder = workerAPIRecorder

func NewWorkerAPIRecorder(port *WorkerAPIPort) pipeline.Recorder {
	return &workerAPIRecorder{port: port}
}
func (r *workerAPIRecorder) send(method string, c workerAPICall) {
	if err := r.port.call("recorder."+method, c, nil); err != nil {
		r.mu.Lock()
		if r.firstErr == nil {
			r.firstErr = err
			// pipeline.Recorder intentionally has no error return. Preserve the
			// first infrastructure failure in captured child diagnostics so the
			// parent ProcessEngine can fail the invocation closed.
			_, _ = fmt.Fprintf(os.Stderr, "%s%s\n", workerAPIErrorMarker, err)
		}
		r.mu.Unlock()
	}
}

func (r *workerAPIRecorder) Error() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstErr
}

func workerAPIRecorderFailure(diagnostics string) string {
	for _, line := range strings.Split(diagnostics, "\n") {
		if index := strings.Index(line, workerAPIErrorMarker); index >= 0 {
			return strings.TrimSpace(line[index+len(workerAPIErrorMarker):])
		}
	}
	return ""
}

// Reconciliation is controller startup maintenance, never a worker report.
func (r *workerAPIRecorder) ReconcileInterrupted(time.Time) {}
func (r *workerAPIRecorder) RunStarted(id, feature, snapshot string, at time.Time) {
	r.send("run_started", workerAPICall{A: feature, B: snapshot, At: at})
}
func (r *workerAPIRecorder) RunResumed(_ string, at time.Time) {
	r.send("run_resumed", workerAPICall{At: at})
}
func (r *workerAPIRecorder) RunAttached(_ string) { r.send("run_attached", workerAPICall{}) }
func (r *workerAPIRecorder) RunPaused(_, status string, at time.Time) {
	r.send("run_paused", workerAPICall{A: status, At: at})
}
func (r *workerAPIRecorder) RunCanceled(_ string, at time.Time) {
	r.send("run_canceled", workerAPICall{At: at})
}
func (r *workerAPIRecorder) ApprovalRequested(_, id, attempt string, at time.Time, data map[string]any) {
	r.send("approval_requested", workerAPICall{A: id, B: attempt, At: at, Data: data})
}
func (r *workerAPIRecorder) ApprovalDecided(_, id, attempt string, at time.Time, data map[string]any) {
	// Human decisions are committed by the controller approval service. The
	// worker-side event cannot assert or manufacture that decision.
}
func (r *workerAPIRecorder) TransitionSelected(_, attempt string, at time.Time, data map[string]any) {
	r.send("transition_selected", workerAPICall{A: attempt, At: at, Data: data})
}
func (r *workerAPIRecorder) StageStarted(_, attempt, agent string, index int, at time.Time) {
	r.send("stage_started", workerAPICall{A: attempt, B: agent, Index: index, At: at})
}
func (r *workerAPIRecorder) StageFinished(stage notifier.StageResult) {
	message := ""
	if stage.Err != nil {
		message = stage.Err.Error()
	}
	stage.Err = nil
	r.send("stage_finished", workerAPICall{Stage: stage, Error: message})
}
func (r *workerAPIRecorder) AttemptsInvalidated(_ string, ids []string, at time.Time) {
	r.send("attempts_invalidated", workerAPICall{IDs: ids, At: at})
}
func (r *workerAPIRecorder) RunFinished(_, status string, at time.Time) {
	r.send("run_finished", workerAPICall{A: status, At: at})
}

type workerAPIApprovals struct{ port *workerAPIPort }
type WorkerAPIApprovals = workerAPIApprovals

func NewWorkerAPIApprovals(port *WorkerAPIPort) pipeline.ApprovalStore {
	return &workerAPIApprovals{port: port}
}
func (a *workerAPIApprovals) Create(value approval.PendingApproval) (approval.PendingApproval, error) {
	var out approval.PendingApproval
	err := a.port.call("approval.create", workerAPICall{Approval: value}, &out)
	return out, err
}
func (a *workerAPIApprovals) Load(_, id string) (approval.PendingApproval, error) {
	var out approval.PendingApproval
	err := a.port.call("approval.load", workerAPICall{A: id}, &out)
	return out, err
}
func (a *workerAPIApprovals) List(_ string) ([]approval.PendingApproval, error) {
	var out []approval.PendingApproval
	err := a.port.call("approval.list", workerAPICall{}, &out)
	return out, err
}
func (*workerAPIApprovals) Decide(string, string, approval.Decision) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, approval.ErrWorkerDecisionWrite
}
func (*workerAPIApprovals) ResolveDeferred(string, string, approval.Decision) (approval.PendingApproval, error) {
	return approval.PendingApproval{}, approval.ErrWorkerDecisionWrite
}

type workerAPIBriefs struct{ port *workerAPIPort }
type WorkerAPIBriefs = workerAPIBriefs

func NewWorkerAPIBriefs(port *WorkerAPIPort) pipeline.BriefStore { return &workerAPIBriefs{port: port} }

func (b *workerAPIBriefs) checkRun(runID string) error {
	if b == nil || b.port == nil || runID == "" || runID != b.port.scope.RunID {
		return errors.New("worker brief API run mismatch")
	}
	return nil
}

func (b *workerAPIBriefs) CreateInitial(runID, intention string) (pipeline.BriefDocument, error) {
	if err := b.checkRun(runID); err != nil {
		return pipeline.BriefDocument{}, err
	}
	var result pipeline.BriefDocument
	err := b.port.call("brief.create_initial", workerAPICall{A: intention}, &result)
	return result, err
}

func (b *workerAPIBriefs) AppendClarification(runID, approvalID, questions, answer string) (pipeline.BriefDocument, error) {
	if err := b.checkRun(runID); err != nil {
		return pipeline.BriefDocument{}, err
	}
	var result pipeline.BriefDocument
	err := b.port.call("brief.append_clarification", workerAPICall{A: approvalID, B: questions, C: answer}, &result)
	return result, err
}

func (b *workerAPIBriefs) List(runID string) ([]pipeline.BriefVersion, error) {
	if err := b.checkRun(runID); err != nil {
		return nil, err
	}
	var result []pipeline.BriefVersion
	err := b.port.call("brief.list", workerAPICall{}, &result)
	return result, err
}

func (b *workerAPIBriefs) Read(runID, versionID string) (pipeline.BriefDocument, error) {
	if err := b.checkRun(runID); err != nil {
		return pipeline.BriefDocument{}, err
	}
	var result pipeline.BriefDocument
	err := b.port.call("brief.read", workerAPICall{A: versionID}, &result)
	return result, err
}

type workerAPILifecycle struct{ port *workerAPIPort }
type WorkerAPILifecycle = workerAPILifecycle

func NewWorkerAPILifecycle(port *WorkerAPIPort) *WorkerAPILifecycle {
	return &workerAPILifecycle{port: port}
}
func (s *workerAPILifecycle) Create(state lifecycle.State) error {
	return s.port.call("lifecycle.create", workerAPICall{Lifecycle: state}, nil)
}
func (s *workerAPILifecycle) Load(runID string) (lifecycle.State, error) {
	if runID != s.port.scope.RunID {
		return lifecycle.State{}, errors.New("lifecycle load run mismatch")
	}
	var state lifecycle.State
	err := s.port.call("lifecycle.load", workerAPICall{A: runID}, &state)
	return state, err
}
func (s *workerAPILifecycle) Save(previous, next lifecycle.State) error {
	return s.port.call("lifecycle.save", workerAPICall{Previous: previous, Lifecycle: next}, nil)
}

var _ pipeline.Recorder = (*workerAPIRecorder)(nil)
var _ pipeline.ApprovalStore = (*workerAPIApprovals)(nil)
var _ pipeline.BriefStore = (*workerAPIBriefs)(nil)
var _ lifecycle.StorePort = (*workerAPILifecycle)(nil)
