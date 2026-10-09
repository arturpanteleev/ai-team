package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/scheduler"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

type fakeRunController struct {
	startFeature   string
	startTask      string
	startCalls     int
	resumeRunID    string
	cancelRunID    string
	cancelErr      error
	decision       approval.Decision
	approvalID     string
	runID          string
	approvals      []approval.PendingApproval
	deliveryRunID  string
	deliveryRecord delivery.TerminalRecord
	deliveryErr    error
	deliveryCalls  int
}

type approvalBoundaryEngine struct{}

func (approvalBoundaryEngine) Start(_ context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	return pipeline.RunResult{RunID: config.RunID}, nil
}
func (approvalBoundaryEngine) Resume(_ context.Context, config pipeline.ResumeConfig) (pipeline.RunResult, error) {
	return pipeline.RunResult{RunID: config.RunID}, nil
}
func (approvalBoundaryEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	return pipeline.RunResult{RunID: config.RunID}, nil
}

type admissionCapturingController struct {
	*control.Controller
	runID string
}

type persistedThenErroredEngine struct {
	queue *scheduler.Queue
}

func (e *persistedThenErroredEngine) Start(_ context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	_, err := e.queue.EnsureStartJob(worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart, RunID: config.RunID,
		TargetDir: config.TargetDir, Feature: config.Feature, Task: config.TaskDesc,
	})
	if err != nil {
		return pipeline.RunResult{}, err
	}
	return pipeline.RunResult{RunID: config.RunID}, errors.New("simulated ambiguous enqueue error after durable insert")
}

func (*persistedThenErroredEngine) Resume(context.Context, pipeline.ResumeConfig) (pipeline.RunResult, error) {
	return pipeline.RunResult{}, errors.New("unexpected resume")
}

func (*persistedThenErroredEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	return pipeline.RunResult{RunID: config.RunID}, nil
}

func (c *admissionCapturingController) StartWithAdmission(feature, task string, admit func(string) error) (string, error) {
	return c.Controller.StartWithAdmission(feature, task, func(runID string) error {
		c.runID = runID
		return admit(runID)
	})
}

func TestDecisionPinsLatestImmutableArtifactRevision(t *testing.T) {
	target := t.TempDir()
	approvalValue := approval.PendingApproval{
		ID: "approval-1", RunID: "run-1", AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: "reviewer", ToStage: "coder", RequiredRoles: []string{"reviewer"},
		Actions: []string{"return_to_coder"}, Targets: map[string]string{"return_to_coder": "coder"},
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{approvalValue}}
	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	srv, err := NewServer(":memory:", "", artifactRoot, WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	relative := "attempts/attempt-1/artifacts/review.md"
	runRoot := filepath.Join(target, ".ai-team", "runs", "run-1")
	source := []byte("source review\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(runRoot, relative)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, relative), source, 0644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"run_id":"run-1","attempt_id":"attempt-1","outputs":[{"name":"review","evidence_path":%q}]}`, relative)
	if err := os.MkdirAll(filepath.Join(runRoot, "attempts", "attempt-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "attempts", "attempt-1", "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	humanStore, err := humanartifact.New(target)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("%x", sha256.Sum256(source))
	revision, err := humanStore.Append("run-1", relative, "", base, "review corrected by QA\n", "human edit", "qa-1")
	if err != nil {
		t.Fatal(err)
	}
	request := authorizedRequest(t, srv, "POST", "/api/runs/run-1/approvals/approval-1/decisions",
		`{"actor_id":"qa-1","actor_role":"reviewer","action":"return_to_coder","comment":"fix it","subject_hash":"`+testSubjectHash+`"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", response.Code, response.Body.String())
	}
	if controller.decision.ArtifactRevisions[relative] != revision.ID {
		t.Fatalf("handoff did not pin current immutable revision: %+v", controller.decision.ArtifactRevisions)
	}
	controller.approvals[0].Status = approval.StatusResolved
	editAfterHandoff := authorizedRequest(t, srv, "POST", "/api/runs/run-1/artifact-revisions",
		fmt.Sprintf(`{"artifact_path":%q,"base_revision":%q,"base_sha256":%q,"content":"unapproved drift","comment":"late edit"}`,
			relative, revision.ID, revision.SHA256))
	blocked := httptest.NewRecorder()
	srv.router.ServeHTTP(blocked, editAfterHandoff)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("a resolved handoff must not be silently changed: %d %s", blocked.Code, blocked.Body.String())
	}
}

func TestQuorumVotesReuseFirstPinnedArtifactRevisionAfterNewerEdit(t *testing.T) {
	target := t.TempDir()
	approvalValue := approval.PendingApproval{
		ID: "approval-quorum", RunID: "run-1", AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: "reviewer", ToStage: "coder", RequiredRoles: []string{"reviewer", "operator"},
		Quorum: approval.QuorumAll, Actions: []string{"return_to_coder"},
		Targets: map[string]string{"return_to_coder": "coder"},
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{approvalValue}}
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	relative := "attempts/attempt-1/artifacts/review.md"
	runRoot := filepath.Join(target, ".ai-team", "runs", "run-1")
	source := []byte("source review\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(runRoot, relative)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, relative), source, 0644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"run_id":"run-1","attempt_id":"attempt-1","outputs":[{"name":"review","evidence_path":%q}]}`, relative)
	if err := os.MkdirAll(filepath.Join(runRoot, "attempts", "attempt-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "attempts", "attempt-1", "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	humanStore, err := humanartifact.New(target)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("%x", sha256.Sum256(source))
	pinned, err := humanStore.Append("run-1", relative, "", base, "review A\n", "first voter selection", "reviewer-1")
	if err != nil {
		t.Fatal(err)
	}

	first := authorizedRequest(t, srv, "POST", "/api/runs/run-1/approvals/approval-quorum/decisions",
		`{"actor_id":"reviewer-1","actor_role":"reviewer","action":"return_to_coder","comment":"fix it","subject_hash":"`+testSubjectHash+`"}`)
	firstResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusOK || controller.decision.ArtifactRevisions[relative] != pinned.ID {
		t.Fatalf("first vote should pin revision A: code=%d selection=%v body=%s", firstResponse.Code, controller.decision.ArtifactRevisions, firstResponse.Body.String())
	}
	// Model the approval's durable post-vote state, then append a newer human
	// edit before the other quorum member votes.
	controller.approvals[0].ArtifactRevisions = map[string]string{relative: pinned.ID}
	controller.approvals[0].Decisions = []approval.Decision{{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "return_to_coder",
		ArtifactRevisions: map[string]string{relative: pinned.ID},
	}}
	newer, err := humanStore.Append("run-1", relative, pinned.ID, pinned.SHA256, "review B\n", "later edit", "editor-2")
	if err != nil {
		t.Fatal(err)
	}
	if newer.ID == pinned.ID {
		t.Fatal("test setup expected a distinct newer revision")
	}

	second := authorizedRequest(t, srv, "POST", "/api/runs/run-1/approvals/approval-quorum/decisions",
		`{"actor_id":"operator-2","actor_role":"operator","action":"return_to_coder","comment":"fix it","subject_hash":"`+testSubjectHash+`"}`)
	secondResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusOK || controller.decision.ArtifactRevisions[relative] != pinned.ID {
		t.Fatalf("second vote must reuse revision A after revision B exists: code=%d selection=%v body=%s", secondResponse.Code, controller.decision.ArtifactRevisions, secondResponse.Body.String())
	}
}

func TestQuorumVotesKeepFirstEmptyArtifactRevisionSelection(t *testing.T) {
	target := t.TempDir()
	approvalValue := approval.PendingApproval{
		ID: "approval-empty-quorum", RunID: "run-1", AttemptID: "attempt-1", Status: approval.StatusPending,
		FromStage: "reviewer", ToStage: "coder", RequiredRoles: []string{"reviewer", "operator"},
		Quorum: approval.QuorumAll, Actions: []string{"return_to_coder"},
		Targets: map[string]string{"return_to_coder": "coder"},
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{approvalValue}}
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	relative := "attempts/attempt-1/artifacts/review.md"
	runRoot := filepath.Join(target, ".ai-team", "runs", "run-1")
	source := []byte("source review\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(runRoot, relative)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, relative), source, 0644); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"run_id":"run-1","attempt_id":"attempt-1","outputs":[{"name":"review","evidence_path":%q}]}`, relative)
	if err := os.MkdirAll(filepath.Join(runRoot, "attempts", "attempt-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "attempts", "attempt-1", "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	first := authorizedRequest(t, srv, "POST", "/api/runs/run-1/approvals/approval-empty-quorum/decisions",
		`{"actor_id":"reviewer-1","actor_role":"reviewer","action":"return_to_coder","comment":"fix it","subject_hash":"`+testSubjectHash+`"}`)
	firstResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusOK || len(controller.decision.ArtifactRevisions) != 0 {
		t.Fatalf("first vote should pin an empty revision selection: code=%d selection=%v body=%s", firstResponse.Code, controller.decision.ArtifactRevisions, firstResponse.Body.String())
	}
	// The approval store represents an empty pinned selection as nil, so the
	// persisted first decision is the marker that the selection is already fixed.
	controller.approvals[0].Decisions = []approval.Decision{{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "return_to_coder",
		SubjectHash: testSubjectHash, ArtifactRevisions: controller.decision.ArtifactRevisions,
	}}

	humanStore, err := humanartifact.New(target)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("%x", sha256.Sum256(source))
	if _, err := humanStore.Append("run-1", relative, "", base, "edited after first vote\n", "later edit", "editor-2"); err != nil {
		t.Fatal(err)
	}

	second := authorizedRequest(t, srv, "POST", "/api/runs/run-1/approvals/approval-empty-quorum/decisions",
		`{"actor_id":"operator-2","actor_role":"operator","action":"return_to_coder","comment":"fix it","subject_hash":"`+testSubjectHash+`"}`)
	secondResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusOK || len(controller.decision.ArtifactRevisions) != 0 {
		t.Fatalf("second vote must reuse the original empty selection and resolve: code=%d selection=%v body=%s", secondResponse.Code, controller.decision.ArtifactRevisions, secondResponse.Body.String())
	}
	var resolved approval.PendingApproval
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Status != approval.StatusResolved {
		t.Fatalf("second quorum vote did not resolve the approval: status=%q", resolved.Status)
	}
}

func (f *fakeRunController) Start(feature, task string) (string, error) {
	f.startCalls++
	f.startFeature, f.startTask = feature, task
	return "run-created", nil
}
func (f *fakeRunController) StartWithAdmission(feature, task string, admit func(string) error) (string, error) {
	if err := admit("run-created"); err != nil {
		return "", err
	}
	return f.Start(feature, task)
}
func (f *fakeRunController) Resume(runID string) error { f.resumeRunID = runID; return nil }
func (f *fakeRunController) Cancel(runID string) error { f.cancelRunID = runID; return f.cancelErr }
func (f *fakeRunController) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	f.runID, f.approvalID, f.decision = runID, approvalID, decision
	return approval.PendingApproval{ID: approvalID, RunID: runID, Status: approval.StatusResolved, ResolvedAction: decision.Action}, nil
}
func (f *fakeRunController) Approvals(string) ([]approval.PendingApproval, error) {
	return f.approvals, nil
}
func (f *fakeRunController) DeliverDeferred(_ context.Context, runID string) (delivery.TerminalRecord, error) {
	f.deliveryCalls++
	f.deliveryRunID = runID
	return f.deliveryRecord, f.deliveryErr
}
func (f *fakeRunController) Preflight(context.Context) preflight.Report {
	return preflight.Report{Ready: true, CheckedAt: time.Now().UTC(), Checks: []preflight.Check{{
		ID: "opencode", Status: preflight.StatusPassed, Required: true, Message: "opencode test",
	}}}
}

func TestPreflightEndpoint(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/preflight", nil))
	if writer.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", writer.Code, writer.Body.String())
	}
	var report preflight.Report
	if err := json.NewDecoder(writer.Body).Decode(&report); err != nil || !report.Ready || len(report.Checks) != 1 {
		t.Fatalf("preflight report: %+v, %v", report, err)
	}
}

func TestQueueProjectionConnectsQueueFailureToVisibleRun(t *testing.T) {
	srv, err := NewServer(":memory:", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := &store.PipelineRun{RunID: "queued-run", Feature: "feature", Status: "queued", StartedAt: time.Now().UTC()}
	if err := srv.store.AdmitPipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if err := srv.RecordQueuedJob("queued-run", 41); err != nil {
		t.Fatal(err)
	}
	srv.RecordQueueStatus(41, "failed", "worker preflight: CLI missing")
	projected, err := srv.store.GetPipelineRunByRunID("queued-run")
	if err != nil || projected.Status != "failed" || projected.QueueJobID != 41 || !strings.Contains(projected.Error, "CLI missing") {
		t.Fatalf("queue failure not visible on run: %+v err=%v", projected, err)
	}
	if err := srv.RecordQueuedJob("queued-run", 41); err != nil {
		t.Fatal(err)
	}
	projected, err = srv.store.GetPipelineRunByRunID("queued-run")
	if err != nil || projected.Status != "failed" || !strings.Contains(projected.Error, "CLI missing") {
		t.Fatalf("correlation hid a genuine terminal worker failure: %+v err=%v", projected, err)
	}
	events, err := srv.store.GetEventsAfter(0, 10)
	if err != nil || len(events) != 3 || events[0].Type != "queue_updated" || events[1].Type != "queue_updated" || !strings.Contains(events[2].DataJSON, `"status":"failed"`) {
		t.Fatalf("queue projection events missing: %+v err=%v", events, err)
	}
}

func TestSchedulerAdmissionIsVisibleAndCancelableWithoutWorker(t *testing.T) {
	target := t.TempDir()
	queue, err := scheduler.Open(filepath.Join(t.TempDir(), "queue.db"), scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	engine, err := scheduler.NewQueueEngine(queue, target)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := control.New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	controller.SetAdmissionSink(srv.RecordQueuedJob)
	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"feature","task":"task without a worker"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var projected *store.PipelineRun
	var jobs []scheduler.Record
	for time.Now().Before(deadline) {
		projected, err = srv.store.GetPipelineRunByRunID(accepted.RunID)
		if err == nil && projected.QueueJobID > 0 {
			jobs, err = queue.ListRun(accepted.RunID)
			if err == nil && len(jobs) == 1 && jobs[0].Status == scheduler.StatusQueued {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || projected == nil || projected.Status != "queued" || projected.QueueJobID == 0 || len(jobs) != 1 || jobs[0].Status != scheduler.StatusQueued {
		t.Fatalf("run not projected with scheduler identity: %+v err=%v", projected, err)
	}
	cancel := authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+accepted.RunID+"/cancel", "")
	response = httptest.NewRecorder()
	srv.router.ServeHTTP(response, cancel)
	if response.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
	}
	projected, err = srv.store.GetPipelineRunByRunID(accepted.RunID)
	if err != nil || projected.Status != "canceled" {
		t.Fatalf("cancel not projected: %+v err=%v", projected, err)
	}
}

func TestPendingAdmissionRecoversQueueCorrelationAfterCrashAndRetry(t *testing.T) {
	target := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "web.db")
	queuePath := filepath.Join(t.TempDir(), "queue.db")
	commandJSON := `{"feature":"feature","task":"recover after crash"}`
	srv, err := NewServer(dbPath, "", filepath.Join(target, ".ai-team", "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AdmitPipelineRun(&store.PipelineRun{
		RunID: "recover-run", Feature: "feature", Status: "queued", StartedAt: time.Now().UTC(), ConfigSnapshot: commandJSON,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AdmitPipelineRun(&store.PipelineRun{
		RunID: "crash-before-enqueue", Feature: "feature", Status: "queued", StartedAt: time.Now().UTC(), ConfigSnapshot: commandJSON,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a process crash after scheduler persistence but before the web
	// projection attaches the durable queue ID.
	queue, err := scheduler.Open(queuePath, scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	job := worker.Job{SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart, RunID: "recover-run", TargetDir: target, Feature: "feature", Task: "recover after crash"}
	jobID, err := queue.EnsureStartJob(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := queue.Claim(context.Background(), "worker-before-correlation"); err != nil || claimed {
		t.Fatalf("uncorrelated pending job was claimable: claimed=%v err=%v", claimed, err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart from the admission outbox. It must find the existing job, attach
	// it before making it claimable, and be idempotent on the next retry.
	srv, err = NewServer(dbPath, "", filepath.Join(target, ".ai-team", "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	queue, err = scheduler.Open(queuePath, scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	admissions, err := srv.store.PendingAdmissions()
	if err != nil || len(admissions) != 2 {
		t.Fatalf("durable pending admission lost: %+v err=%v", admissions, err)
	}
	for _, admission := range admissions {
		if admission.ConfigSnapshot != commandJSON {
			t.Fatalf("admission task snapshot was not durable: %+v", admission)
		}
		var command struct {
			Feature string `json:"feature"`
			Task    string `json:"task"`
		}
		if err := json.Unmarshal([]byte(admission.ConfigSnapshot), &command); err != nil {
			t.Fatal(err)
		}
		recoveredID, err := queue.EnsureStartJob(worker.Job{
			SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart, RunID: admission.RunID,
			TargetDir: target, Feature: command.Feature, Task: command.Task,
		})
		if err != nil {
			t.Fatal(err)
		}
		if admission.RunID == "recover-run" && recoveredID != jobID {
			t.Fatalf("recovery did not reuse pending job: id=%d original=%d", recoveredID, jobID)
		}
		if err := srv.RecordQueuedJob(admission.RunID, recoveredID); err != nil {
			t.Fatal(err)
		}
		if err := queue.Activate(recoveredID); err != nil {
			t.Fatal(err)
		}
		jobs, err := queue.ListRun(admission.RunID)
		if err != nil || len(jobs) != 1 || jobs[0].Status != scheduler.StatusQueued {
			t.Fatalf("recovered queue state: %+v err=%v", jobs, err)
		}
		projected, err := srv.store.GetPipelineRunByRunID(admission.RunID)
		if err != nil || projected.QueueJobID != recoveredID || projected.Status != "queued" {
			t.Fatalf("recovered projection: %+v err=%v", projected, err)
		}
	}
	if again, err := queue.EnsureStartJob(job); err != nil || again != jobID {
		t.Fatalf("retry was not idempotent: id=%d err=%v", again, err)
	}
}

func TestStartRunProjectionFailurePreventsControllerStart(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}

	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"feature","task":"task"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("projection failure must not claim accepted: code=%d body=%s", response.Code, response.Body.String())
	}
	if controller.startCalls != 0 {
		t.Fatalf("controller started run before durable projection: calls=%d", controller.startCalls)
	}
	if controller.cancelRunID != "" {
		t.Fatalf("failed pre-start projection should not require asynchronous compensation, got cancel %q", controller.cancelRunID)
	}
	if strings.Contains(response.Body.String(), `"run_id"`) {
		t.Fatalf("failed projection response must not expose a falsely accepted run: %s", response.Body.String())
	}
}

func TestSchedulerAdmissionProjectionFailureCannotLeaveClaimableJob(t *testing.T) {
	target := t.TempDir()
	queue, err := scheduler.Open(filepath.Join(t.TempDir(), "queue.db"), scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	engine, err := scheduler.NewQueueEngine(queue, target)
	if err != nil {
		t.Fatal(err)
	}
	baseController, err := control.New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	controller := &admissionCapturingController{Controller: baseController}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	baseController.SetAdmissionSink(srv.RecordQueuedJob)
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}

	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"feature","task":"task"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("projection failure must return 503: code=%d body=%s", response.Code, response.Body.String())
	}
	if controller.runID == "" {
		t.Fatal("admission callback was not reached")
	}

	// The admission callback runs before Controller launches its background
	// worker. After the 503 there must be no persistent scheduler job to claim,
	// even after enough time for that worker to have run if it had been started.
	time.Sleep(25 * time.Millisecond)
	jobs, err := queue.ListRun(controller.runID)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("failed admission left scheduler jobs: jobs=%+v err=%v", jobs, err)
	}
	_, claimed, err := queue.Claim(context.Background(), "admission-failure-test")
	if err != nil || claimed {
		t.Fatalf("failed admission job was claimable: claimed=%v err=%v", claimed, err)
	}
}

func TestAmbiguousQueueInsertFailureConvergesThroughAdmissionRecovery(t *testing.T) {
	target := t.TempDir()
	queue, err := scheduler.Open(filepath.Join(t.TempDir(), "queue.db"), scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := queue.Close(); err != nil {
			t.Errorf("close queue: %v", err)
		}
	}()
	engine := &persistedThenErroredEngine{queue: queue}
	baseController, err := control.New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(baseController))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close server: %v", err)
		}
	}()
	baseController.SetFailureSink(srv.RecordAdmissionFailure)
	baseController.SetAdmissionSink(srv.RecordQueuedJob)

	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs", `{"feature":"ambiguous-queue-work","task":"recover durable pending job"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var projected *store.PipelineRun
	for time.Now().Before(deadline) {
		projected, err = srv.store.GetPipelineRunByRunID(accepted.RunID)
		if err == nil && projected.Status == "failed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || projected == nil || projected.Status != "failed" || projected.QueueJobID != 0 {
		t.Fatalf("simulated ambiguous enqueue failure was not projected: %+v err=%v", projected, err)
	}

	// Startup recovery sees the failed, uncorrelated admission, reuses the
	// already-persisted pending job, links it, and only then activates it.
	admissions, err := srv.store.PendingAdmissions()
	if err != nil || len(admissions) != 1 || admissions[0].RunID != accepted.RunID {
		t.Fatalf("failed admission was not recoverable: %+v err=%v", admissions, err)
	}
	var command struct {
		Feature string `json:"feature"`
		Task    string `json:"task"`
	}
	if err := json.Unmarshal([]byte(admissions[0].ConfigSnapshot), &command); err != nil {
		t.Fatal(err)
	}
	jobID, err := queue.EnsureStartJob(worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
		RunID: accepted.RunID, TargetDir: target, Feature: command.Feature, Task: command.Task,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.ListRun(accepted.RunID)
	if err != nil || len(jobs) != 1 || jobs[0].ID != jobID || jobs[0].Status != scheduler.Status("pending") {
		t.Fatalf("recovery did not reuse the durable pending job: %+v err=%v", jobs, err)
	}
	if err := srv.RecordQueuedJob(accepted.RunID, jobID); err != nil {
		t.Fatal(err)
	}
	if err := queue.Activate(jobID); err != nil {
		t.Fatal(err)
	}
	projected, err = srv.store.GetPipelineRunByRunID(accepted.RunID)
	if err != nil || projected.Status != "queued" || projected.QueueJobID != jobID || projected.Error != "" || projected.CompletedAt != nil {
		t.Fatalf("recovered admission did not return to queued state: %+v err=%v", projected, err)
	}
	claimed, ok, err := queue.Claim(context.Background(), "recovered-admission-test")
	if err != nil || !ok || claimed.ID != jobID {
		t.Fatalf("recovered queue job is not claimable: job=%+v claimed=%v err=%v", claimed, ok, err)
	}
	srv.RecordQueueStatus(jobID, string(scheduler.StatusRunning), "")
	projected, err = srv.store.GetPipelineRunByRunID(accepted.RunID)
	if err != nil || projected.Status != "running" || projected.QueueJobID != jobID {
		t.Fatalf("projection did not converge to the claimed queue job: %+v err=%v", projected, err)
	}
	// Exercise the opposite ordering too: the old foreground error arrives
	// after recovery has linked and a worker has claimed the job.
	srv.RecordAdmissionFailure(accepted.RunID, "late ambiguous enqueue error")
	projected, err = srv.store.GetPipelineRunByRunID(accepted.RunID)
	if err != nil || projected.Status != "running" || projected.QueueJobID != jobID {
		t.Fatalf("late enqueue error overwrote recovered queue state: %+v err=%v", projected, err)
	}
}

func TestRunLogReturnsBoundedTail(t *testing.T) {
	srv, artifactRoot := newTestServer(t)
	run := &store.PipelineRun{RunID: "run-log", Feature: "feat", Status: "running", StartedAt: time.Now()}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(filepath.Dir(artifactRoot), "runs", run.RunID, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", maxLogTailSize) + "TAIL"
	if err := os.WriteFile(filepath.Join(logDir, "001-agent.log"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/runs/run-log/logs/001-agent", nil))
	if writer.Code != http.StatusOK {
		t.Fatalf("log: %d %s", writer.Code, writer.Body.String())
	}
	var tail logTail
	if err := json.NewDecoder(writer.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if !tail.Truncated || tail.Offset != 4 || len(tail.Content) != maxLogTailSize || !strings.HasSuffix(tail.Content, "TAIL") {
		t.Fatalf("unexpected log tail: offset=%d truncated=%t size=%d", tail.Offset, tail.Truncated, len(tail.Content))
	}
}

func TestRunLogRejectsUnsafeIdentity(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../attempt", "dir/attempt", `dir\attempt`} {
		if safeIdentity(value) {
			t.Errorf("identity %q должна быть отклонена", value)
		}
	}
}

func TestRunWorkflowReturnsImmutableSnapshot(t *testing.T) {
	srv, artifactRoot := newTestServer(t)
	run := &store.PipelineRun{RunID: "run-graph", Feature: "feat", Status: "running", StartedAt: time.Now()}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(artifactRoot), "runs", run.RunID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	snapshot := `{"schema_version":2,"graph":{"entry":"analyst","nodes":[],"edges":[]}}`
	if err := os.WriteFile(filepath.Join(runDir, "workflow.json"), []byte(snapshot), 0644); err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/runs/run-graph/workflow", nil))
	if writer.Code != http.StatusOK || strings.TrimSpace(writer.Body.String()) != snapshot {
		t.Fatalf("workflow: %d %s", writer.Code, writer.Body.String())
	}
}

// newLoopbackRequest wraps httptest.NewRequest and sets Host to a loopback
// value: httptest.NewRequest defaults Host to "example.com" for relative
// targets, which sameOriginMiddleware now correctly rejects (the CLI only
// ever binds this server to a loopback address in practice).
func newLoopbackRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1"
	return req
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	target := t.TempDir()
	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		t.Fatalf("create artifact root: %v", err)
	}
	srv, err := NewServer(":memory:", "", artifactRoot, WithTargetDir(target))
	if err != nil {
		t.Fatalf("failed to create test server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, artifactRoot
}

func authorizedRequest(t *testing.T, srv *Server, method, target, body string) *http.Request {
	t.Helper()
	const testLocalToken = "test-local-web-token-0123456789abcdef0123456789abcdef"
	if srv.authenticator == nil {
		verifier, err := NewLocalAuthenticator(testLocalToken)
		if err != nil {
			t.Fatal(err)
		}
		srv.authenticator = verifier
		srv.localAuth = true
	}
	sessionRequest := newLoopbackRequest("GET", "/api/session", nil)
	sessionRequest.Header.Set("Authorization", "Bearer "+testLocalToken)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	if sessionWriter.Code != http.StatusOK {
		t.Fatalf("session bootstrap: %d %s", sessionWriter.Code, sessionWriter.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(sessionWriter.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	response := sessionWriter.Result()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookie отсутствует: %v", cookies)
	}
	request := newLoopbackRequest(method, target, strings.NewReader(body))
	request.AddCookie(cookies[0])
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func authenticatedRequest(t *testing.T, srv *Server, token, method, target, body string) *http.Request {
	t.Helper()
	sessionRequest := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	sessionRequest.Header.Set("Authorization", "Bearer "+token)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	if sessionWriter.Code != http.StatusOK {
		t.Fatalf("authenticated session bootstrap: %d %s", sessionWriter.Code, sessionWriter.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(sessionWriter.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookies := sessionWriter.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("authenticated session cookie отсутствует: %v", cookies)
	}
	request := newLoopbackRequest(method, target, strings.NewReader(body))
	request.AddCookie(cookies[0])
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestWriteAPIRequiresSessionAndCSRF(t *testing.T) {
	controller := &fakeRunController{}
	artifactRoot := t.TempDir()
	srv, err := NewServer(":memory:", "", artifactRoot, WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	noSession := newLoopbackRequest("POST", "/api/runs", strings.NewReader(`{"feature":"f","task":"t"}`))
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, noSession)
	if writer.Code != http.StatusUnauthorized {
		t.Fatalf("без session: %d", writer.Code)
	}

	sessionRequest := newLoopbackRequest("GET", "/api/session", nil)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	if sessionWriter.Code != http.StatusUnauthorized {
		t.Fatalf("session bootstrap without authenticator: %d %s", sessionWriter.Code, sessionWriter.Body.String())
	}
	noCSRF := newLoopbackRequest("POST", "/api/runs", strings.NewReader(`{"feature":"f","task":"t"}`))
	noCSRF.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "forged-session"})
	noCSRF.Header.Set("X-CSRF-Token", "forged-csrf")
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, noCSRF)
	if writer.Code != http.StatusUnauthorized {
		t.Fatalf("без authenticator и bearer token: %d", writer.Code)
	}
}

func TestLocalWebTokenIsRequiredAndApprovalRoleComesFromServer(t *testing.T) {
	const token = "local-web-token-for-tests-0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: "approval-local", RunID: "run-local", AttemptID: "attempt-local",
		Status: approval.StatusPending, RequiredRoles: []string{"reviewer"},
		Actions: []string{"approve"}, SubjectHash: testSubjectHash,
	}}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	withoutToken := newLoopbackRequest(http.MethodPost, "/api/runs/run-local/approvals/approval-local/decisions", strings.NewReader(`{"action":"approve"}`))
	unauthenticated := httptest.NewRecorder()
	srv.router.ServeHTTP(unauthenticated, withoutToken)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("write without token must be rejected: %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	invalidLogin := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	invalidLogin.Header.Set("Authorization", "Bearer invalid-local-token")
	invalidResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(invalidResponse, invalidLogin)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid local token must be rejected: %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}

	request := authenticatedRequest(t, srv, token, http.MethodPost,
		"/api/runs/run-local/approvals/approval-local/decisions",
		`{"actor_id":"spoofed","actor_role":"release_manager","action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("local decision: %d %s", response.Code, response.Body.String())
	}
	if controller.decision.ActorID != "local-user" || controller.decision.ActorRole != "reviewer" {
		t.Fatalf("local identity and role must come from server: %+v", controller.decision)
	}
}

func TestLocalWebQuorumAllAssignsNextUnvotedRequiredRole(t *testing.T) {
	const token = "local-web-token-for-quorum-0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: "approval-quorum", RunID: "run-quorum", Status: approval.StatusPending,
		Quorum: approval.QuorumAll, RequiredRoles: []string{"architect", "reviewer"},
		Actions: []string{"approve"}, SubjectHash: testSubjectHash,
	}}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	endpoint := "/api/runs/run-quorum/approvals/approval-quorum/decisions"
	body := `{"actor_id":"spoofed","actor_role":"product_owner","action":"approve","subject_hash":"` + testSubjectHash + `"}`
	first := authenticatedRequest(t, srv, token, http.MethodPost, endpoint, body)
	firstWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(firstWriter, first)
	if firstWriter.Code != http.StatusOK || controller.decision.ActorRole != "architect" {
		t.Fatalf("first local vote should use first unvoted required role: code=%d decision=%+v body=%s", firstWriter.Code, controller.decision, firstWriter.Body.String())
	}
	controller.approvals[0].Decisions = []approval.Decision{{ActorID: controller.decision.ActorID, ActorRole: controller.decision.ActorRole}}
	second := authenticatedRequest(t, srv, token, http.MethodPost, endpoint, body)
	secondWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(secondWriter, second)
	if secondWriter.Code != http.StatusOK || controller.decision.ActorRole != "reviewer" {
		t.Fatalf("second local vote should use the next unvoted required role: code=%d decision=%+v body=%s", secondWriter.Code, controller.decision, secondWriter.Body.String())
	}
}

func TestAuthenticatedSessionRecoversCSRFForSameOriginCookieAndRejectsExpiredSession(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cloudidentity.NewPrincipal("product-1", []cloudidentity.Role{cloudidentity.RoleProductOwner})
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	login := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	login.Header.Set("Authorization", "Bearer "+token)
	loginWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(loginWriter, login)
	if loginWriter.Code != http.StatusOK {
		t.Fatalf("login: %d %s", loginWriter.Code, loginWriter.Body.String())
	}
	var initial sessionResponse
	if err := json.NewDecoder(loginWriter.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	cookie := loginWriter.Result().Cookies()[0]

	bootstrap := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	bootstrap.AddCookie(cookie)
	bootstrap.Header.Set("Origin", "http://127.0.0.1")
	bootstrapWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(bootstrapWriter, bootstrap)
	if bootstrapWriter.Code != http.StatusOK {
		t.Fatalf("same-origin recovery: %d %s", bootstrapWriter.Code, bootstrapWriter.Body.String())
	}
	var recovered sessionResponse
	if err := json.NewDecoder(bootstrapWriter.Body).Decode(&recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.CSRFToken != initial.CSRFToken || recovered.Principal == nil || recovered.Principal.ActorID != principal.ActorID {
		t.Fatalf("recovered session differs: initial=%+v recovered=%+v", initial, recovered)
	}
	if bootstrapWriter.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session response may be cached: %q", bootstrapWriter.Header().Get("Cache-Control"))
	}
	command := func(path, body string) *httptest.ResponseRecorder {
		req := newLoopbackRequest(http.MethodPost, path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", recovered.CSRFToken)
		req.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		srv.router.ServeHTTP(result, req)
		return result
	}
	if result := command("/api/runs", `{"feature":"reload-feature","task":"start after reload"}`); result.Code != http.StatusAccepted {
		t.Fatalf("start after CSRF recovery: %d %s", result.Code, result.Body.String())
	}
	if result := command("/api/runs/run-1/resume", ""); result.Code != http.StatusAccepted || controller.resumeRunID != "run-1" {
		t.Fatalf("resume after CSRF recovery: %d %s", result.Code, result.Body.String())
	}
	decisionBody := `{"actor_id":"client-spoof","actor_role":"product_owner","action":"approve","subject_hash":"` + testSubjectHash + `"}`
	if result := command("/api/runs/run-1/approvals/approval-1/decisions", decisionBody); result.Code != http.StatusOK || controller.decision.ActorID != principal.ActorID {
		t.Fatalf("decision after CSRF recovery: %d %s decision=%+v", result.Code, result.Body.String(), controller.decision)
	}

	foreign := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	foreign.AddCookie(cookie)
	foreign.Header.Set("Origin", "https://attacker.example")
	foreignWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(foreignWriter, foreign)
	if foreignWriter.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin recovery: %d %s", foreignWriter.Code, foreignWriter.Body.String())
	}
	wrongScheme := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	wrongScheme.AddCookie(cookie)
	wrongScheme.Header.Set("Origin", "https://127.0.0.1")
	wrongSchemeWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(wrongSchemeWriter, wrongScheme)
	if wrongSchemeWriter.Code != http.StatusForbidden {
		t.Fatalf("wrong-scheme recovery: %d %s", wrongSchemeWriter.Code, wrongSchemeWriter.Body.String())
	}
	crossSite := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	crossSite.AddCookie(cookie)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSiteWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(crossSiteWriter, crossSite)
	if crossSiteWriter.Code != http.StatusForbidden {
		t.Fatalf("cross-site metadata recovery: %d %s", crossSiteWriter.Code, crossSiteWriter.Body.String())
	}

	srv.sessionMu.Lock()
	session := srv.sessions[cookie.Value]
	session.ExpiresAt = time.Now().UTC().Add(-time.Second)
	srv.sessions[cookie.Value] = session
	srv.sessionMu.Unlock()
	expired := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	expired.AddCookie(cookie)
	expired.Header.Set("Origin", "http://127.0.0.1")
	expiredWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(expiredWriter, expired)
	if expiredWriter.Code != http.StatusUnauthorized || !strings.Contains(expiredWriter.Body.String(), "сессия истекла") {
		t.Fatalf("expired session: %d %s", expiredWriter.Code, expiredWriter.Body.String())
	}
}

func TestWriteRunAndDecisionCommands(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	start := authorizedRequest(t, srv, "POST", "/api/runs", `{"feature":"feat","task":"задача"}`)
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, start)
	if writer.Code != http.StatusAccepted || controller.startFeature != "feat" || controller.startTask != "задача" {
		t.Fatalf("start: code=%d controller=%+v body=%s", writer.Code, controller, writer.Body.String())
	}
	admitted, err := srv.store.GetPipelineRunByRunID("run-created")
	if err != nil || admitted.Status != "queued" {
		t.Fatalf("accepted run was not projected as queued: %+v err=%v", admitted, err)
	}
	cancel := authorizedRequest(t, srv, "POST", "/api/runs/run-created/cancel", "")
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, cancel)
	if writer.Code != http.StatusAccepted {
		t.Fatalf("queued cancel: %d %s", writer.Code, writer.Body.String())
	}
	queued, err := srv.store.GetPipelineRunByRunID("run-created")
	if err != nil || queued.Status != "canceled" {
		t.Fatalf("queued cancellation not projected: %+v err=%v", queued, err)
	}

	bad := authorizedRequest(t, srv, "POST", "/api/runs", `{"feature":"feat","task":"задача","unknown":true}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, bad)
	if writer.Code != http.StatusBadRequest || controller.startCalls != 1 {
		t.Fatalf("unknown JSON field запустил worker: code=%d calls=%d", writer.Code, controller.startCalls)
	}

	decision := authorizedRequest(t, srv, "POST",
		"/api/runs/run-1/approvals/approval-1/decisions",
		`{"actor_id":"user-1","actor_role":"qa","action":"approve","comment":"проверено","subject_hash":"`+testSubjectHash+`"}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, decision)
	if writer.Code != http.StatusOK || controller.runID != "run-1" ||
		controller.approvalID != "approval-1" || controller.decision.ActorID != "local-user" ||
		controller.decision.ActorRole != "product_owner" {
		t.Fatalf("decision: code=%d controller=%+v body=%s", writer.Code, controller, writer.Body.String())
	}
	if controller.decision.ControllerAuthenticated {
		t.Fatal("unauthenticated local server must not mark a decision as controller-authenticated")
	}
}

func TestQuestionApprovalRequiresNonEmptyAnswer(t *testing.T) {
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: "approval-1", RunID: "run-1", Kind: approval.KindQuestions, Status: approval.StatusPending,
		RequiredRoles: []string{"qa"},
		Payload:       []byte(`{"kind":"questions","markdown":"Какова целевая аудитория?"}`),
	}}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	endpoint := "/api/runs/run-1/approvals/approval-1/decisions"
	empty := authorizedRequest(t, srv, "POST", endpoint,
		`{"actor_id":"user-1","actor_role":"qa","action":"answer_questions","subject_hash":"`+testSubjectHash+`"}`)
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, empty)
	if writer.Code != http.StatusBadRequest || controller.approvalID != "" {
		t.Fatalf("пустой ответ должен отклоняться: code=%d controller=%+v body=%s", writer.Code, controller, writer.Body.String())
	}
	answered := authorizedRequest(t, srv, "POST", endpoint,
		`{"actor_id":"user-1","actor_role":"qa","action":"answer_questions","comment":"B2B-клиенты среднего бизнеса","subject_hash":"`+testSubjectHash+`"}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, answered)
	if writer.Code != http.StatusOK || controller.decision.Comment != "B2B-клиенты среднего бизнеса" {
		t.Fatalf("ответ должен сохраниться как решение: code=%d decision=%+v body=%s", writer.Code, controller.decision, writer.Body.String())
	}
}

func TestWorkerApplicationPortDeniedAndAuthenticatedControllerCanDecide(t *testing.T) {
	target := t.TempDir()
	approvalStore, err := approval.NewSQLiteStore(filepath.Join(target, "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = approvalStore.Close() }()
	pending, err := approvalStore.Create(approval.PendingApproval{
		RunID: "boundary-run", AttemptID: "attempt-1", FromStage: "reviewer", ToStage: "coder",
		Trigger: "stage_completed", SubjectHash: testSubjectHash,
		RequiredRoles: []string{"reviewer"}, Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "coder", "reject": "$stop"},
	})
	if err != nil {
		t.Fatal(err)
	}

	workerStore := approval.NewWorkerStore(approvalStore)
	if _, err := workerStore.Decide(pending.RunID, pending.ID, approval.Decision{
		ActorID: "worker", ActorRole: "reviewer", Action: "approve", SubjectHash: pending.SubjectHash,
	}); !errors.Is(err, approval.ErrWorkerDecisionWrite) {
		t.Fatalf("worker application port should refuse decision writes: %v", err)
	}
	unchanged, err := approvalStore.Load(pending.RunID, pending.ID)
	if err != nil || unchanged.Status != approval.StatusPending || len(unchanged.Decisions) != 0 {
		t.Fatalf("worker denial must preserve the approval: %+v err=%v", unchanged, err)
	}

	controller, err := control.New(approvalBoundaryEngine{}, target, control.WithApprovalStore(approvalStore))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cloudidentity.NewPrincipal("reviewer-1@example.com", []cloudidentity.Role{cloudidentity.RoleReviewer})
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller), WithAuthenticator(manager))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	ownerCookie, ownerCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPost, "/api/team/invitations", `{"email":"reviewer-1@example.com","roles":["reviewer"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite reviewer: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	activation := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activation.Header.Set("Content-Type", "application/json")
	activationWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(activationWriter, activation)
	if activationWriter.Code != http.StatusOK {
		t.Fatalf("activate reviewer: %d %s", activationWriter.Code, activationWriter.Body.String())
	}
	request := authenticatedRequest(t, srv, token, http.MethodPost,
		"/api/runs/"+pending.RunID+"/approvals/"+pending.ID+"/decisions",
		`{"actor_id":"spoofed","actor_role":"reviewer","action":"approve","subject_hash":"`+pending.SubjectHash+`"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated controller decision: %d %s", response.Code, response.Body.String())
	}
	resolved, err := approvalStore.Load(pending.RunID, pending.ID)
	if err != nil || resolved.Status != approval.StatusResolved || resolved.Decisions[0].ActorID != "reviewer-1@example.com" {
		t.Fatalf("controller must record the authenticated principal: %+v err=%v", resolved, err)
	}
}

func TestSpecificationApprovalRequiresProductOwnerRoleAndPayload(t *testing.T) {
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: "approval-spec", RunID: "run-1", Status: approval.StatusPending,
		RequiredRoles: []string{"product_owner"},
		Payload:       []byte(`{"kind":"agreed_spec","artifacts":{"spec":"abc"}}`),
	}}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	endpoint := "/api/runs/run-1/approvals/approval-spec/decisions"
	spoofedRole := authorizedRequest(t, srv, "POST", endpoint,
		`{"actor_id":"user-1","actor_role":"qa","action":"approve_spec","subject_hash":"`+testSubjectHash+`"}`)
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, spoofedRole)
	if writer.Code != http.StatusOK || controller.decision.ActorRole != "product_owner" || controller.decision.ActorID != "local-user" {
		t.Fatalf("local server must ignore spoofed actor and assign Product Owner: code=%d controller=%+v", writer.Code, controller)
	}
	legacyAction := authorizedRequest(t, srv, "POST", endpoint,
		`{"actor_id":"user-1","actor_role":"qa","action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, legacyAction)
	if writer.Code != http.StatusConflict {
		t.Fatalf("Product Owner role check нельзя обойти общим approve action: code=%d controller=%+v", writer.Code, controller)
	}
	productOwner := authorizedRequest(t, srv, "POST", endpoint,
		`{"actor_id":"user-1","actor_role":"product_owner","action":"approve_spec","subject_hash":"`+testSubjectHash+`"}`)
	writer = httptest.NewRecorder()
	srv.router.ServeHTTP(writer, productOwner)
	if writer.Code != http.StatusOK || controller.decision.Action != "approve_spec" {
		t.Fatalf("Product Owner должен иметь возможность согласовать ТЗ: code=%d decision=%+v", writer.Code, controller.decision)
	}
}

func TestCloudAuthenticationAndRBACUseTrustedPrincipal(t *testing.T) {
	controller := &fakeRunController{}
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cloudidentity.NewPrincipal("reviewer@example.com", []cloudidentity.Role{cloudidentity.RoleReviewer})
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(),
		WithRunController(controller), WithAuthenticator(manager))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	adminCookie, adminCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations", `{"email":"reviewer@example.com","roles":["reviewer"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite reviewer: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	activation := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activation.Header.Set("Content-Type", "application/json")
	activationWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(activationWriter, activation)
	if activationWriter.Code != http.StatusOK {
		t.Fatalf("activate reviewer: %d %s", activationWriter.Code, activationWriter.Body.String())
	}

	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest("GET", "/api/pipelines", nil))
	if writer.Code != http.StatusUnauthorized {
		t.Fatalf("cloud read без session: %d", writer.Code)
	}

	sessionRequest := newLoopbackRequest("GET", "/api/session", nil)
	sessionRequest.Header.Set("Authorization", "Bearer "+token)
	sessionWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(sessionWriter, sessionRequest)
	if sessionWriter.Code != http.StatusOK {
		t.Fatalf("cloud session: %d %s", sessionWriter.Code, sessionWriter.Body.String())
	}
	var session sessionResponse
	if err := json.NewDecoder(sessionWriter.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookie := sessionWriter.Result().Cookies()[0]

	command := func(target, body string) *httptest.ResponseRecorder {
		request := newLoopbackRequest("POST", target, strings.NewReader(body))
		request.AddCookie(cookie)
		request.Header.Set("X-CSRF-Token", session.CSRFToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)
		return response
	}
	if response := command("/api/runs", `{"feature":"f","task":"t"}`); response.Code != http.StatusForbidden {
		t.Fatalf("reviewer не должен создавать run: %d %s", response.Code, response.Body.String())
	}
	response := command("/api/runs/run-1/approvals/approval-1/decisions",
		`{"actor_id":"spoofed","actor_role":"reviewer","action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("reviewer decision: %d %s", response.Code, response.Body.String())
	}
	if controller.decision.ActorID != "reviewer@example.com" || controller.decision.ActorRole != "reviewer" {
		t.Fatalf("decision audit использовал недоверенную identity: %+v", controller.decision)
	}
	if !controller.decision.ControllerAuthenticated {
		t.Fatal("authenticated controller decision must retain its authorization provenance")
	}
}

const testSubjectHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGetPipelines_Empty(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var runs []interface{}
	if err := json.NewDecoder(w.Body).Decode(&runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected empty array, got %d items", len(runs))
	}
}

func TestGetPipelines_WithData(t *testing.T) {
	srv, _ := newTestServer(t)

	if err := srv.Store().CreatePipelineRun(&store.PipelineRun{
		Feature:   "test-feat",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var runs []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&runs); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0]["feature"] != "test-feat" {
		t.Errorf("expected feature 'test-feat', got %v", runs[0]["feature"])
	}
}

func TestGetPipelinesPagination(t *testing.T) {
	srv, _ := newTestServer(t)
	for index := 0; index < 3; index++ {
		if err := srv.Store().CreatePipelineRun(&store.PipelineRun{RunID: fmt.Sprintf("run-%d", index), Feature: "f", Status: "completed", StartedAt: time.Now().Add(time.Duration(index) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	req := newLoopbackRequest("GET", "/api/pipelines?limit=1&offset=1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	var runs []store.PipelineRun
	_ = json.NewDecoder(w.Body).Decode(&runs)
	if w.Code != http.StatusOK || len(runs) != 1 || w.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("pagination response: code=%d total=%s runs=%+v", w.Code, w.Header().Get("X-Total-Count"), runs)
	}
	bad := newLoopbackRequest("GET", "/api/pipelines?limit=1000", nil)
	badWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(badWriter, bad)
	if badWriter.Code != http.StatusBadRequest {
		t.Fatalf("invalid pagination must be 400, got %d", badWriter.Code)
	}
}

func TestGetPipelineByID(t *testing.T) {
	srv, _ := newTestServer(t)

	run := &store.PipelineRun{Feature: "detail-test", Status: "completed", StartedAt: time.Now()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatalf("setup: %v", err)
	}

	req := newLoopbackRequest("GET", "/api/pipelines/1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if resp["run"] == nil {
		t.Error("expected 'run' in response")
	}
	if resp["stages"] == nil {
		t.Error("expected 'stages' in response")
	}
}

func TestGetPipelineProjectsDeliveryRecordSeparatelyFromRunStatus(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(t.TempDir(), "custom-artifacts", "delivery-output")
	srv, err := NewServer(":memory:", "", artifactRoot,
		WithRunController(&fakeRunController{}), WithTargetDir(target))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	targetCanonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if srv.targetDir != targetCanonical || srv.runRoot != filepath.Join(targetCanonical, ".ai-team", "runs") {
		t.Fatalf("custom artifact root must not determine project target/run root: target=%q runRoot=%q", srv.targetDir, srv.runRoot)
	}
	run := &store.PipelineRun{RunID: "delivery-projection", Feature: "feat", Status: "completed", StartedAt: time.Now().UTC()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store().CreateStage(&store.Stage{
		PipelineRunID: run.ID, AttemptID: "attempt-1", StageIndex: 1, AgentName: "deployer",
		Status: "passed", StartedAt: run.StartedAt, DeliveryJSON: `{"plan_hash":"` + strings.Repeat("a", 64) + `"}`,
	}); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		t.Helper()
		request := newLoopbackRequest(http.MethodGet, "/api/pipelines/"+fmt.Sprint(run.ID), nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("pipeline details: %d %s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	body := get()
	deliveryView, ok := body["delivery"].(map[string]any)
	if !ok || deliveryView["status"] != "pending" {
		t.Fatalf("completed run must not imply successful delivery: %v", body["delivery"])
	}
	record := delivery.TerminalRecord{
		SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: run.RunID, Feature: run.Feature,
		PlanHash: strings.Repeat("a", 64), CommitSHA: strings.Repeat("b", 40), PRURL: "https://example.test/pr/12",
		PerformedAt: time.Now().UTC(),
	}
	legacyRunDir := filepath.Join(target, ".ai-team", "runs", run.RunID)
	if err := os.MkdirAll(legacyRunDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := delivery.WriteTerminalRecord(legacyRunDir, record); err != nil {
		t.Fatal(err)
	}
	if err := delivery.WriteControllerTerminalRecord(target, run.RunID, record); err != nil {
		t.Fatal(err)
	}
	body = get()
	deliveryView, ok = body["delivery"].(map[string]any)
	if !ok || deliveryView["status"] != "pending" {
		t.Fatalf("worker-submitted terminal record must not project a trusted delivery: %v", body["delivery"])
	}
	if err := delivery.WriteControllerDeliveryReceipt(target, record); err != nil {
		t.Fatal(err)
	}
	body = get()
	deliveryView, ok = body["delivery"].(map[string]any)
	if !ok || deliveryView["status"] != "recorded" {
		t.Fatalf("controller delivery receipt must project recorded Git outcome: %v", body["delivery"])
	}
	projectedRecord, ok := deliveryView["record"].(map[string]any)
	if !ok || projectedRecord["commit_sha"] != record.CommitSHA || projectedRecord["pr_url"] != record.PRURL {
		t.Fatalf("commit and PR missing from projection: %v", deliveryView)
	}
	if _, exists := body["deployment"]; exists {
		t.Fatal("pipeline or Git delivery must not project deployment success")
	}
}

func TestRetryDeliveryRequiresAuthorizedActorAndUsesController(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("m", 32)))
	if err != nil {
		t.Fatal(err)
	}
	record := delivery.TerminalRecord{
		SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: "retry-run", Feature: "feat",
		PlanHash: strings.Repeat("a", 64), CommitSHA: strings.Repeat("b", 40),
		PRURL: "https://example.test/pr/13", PerformedAt: time.Now().UTC(),
	}
	controller := &fakeRunController{deliveryRecord: record}
	target := t.TempDir()
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"),
		WithRunController(controller), WithAuthenticator(manager))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	if err := srv.Store().CreatePipelineRun(&store.PipelineRun{
		RunID: record.RunID, Feature: record.Feature, Status: "completed", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	ownerCookie, ownerCSRF := cloudSessionForTest(t, srv, manager, "owner@example.test", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPost, "/api/team/invitations",
		`{"email":"reviewer@example.test","roles":["reviewer"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite reviewer: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	activate := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activate.Header.Set("Content-Type", "application/json")
	activation := httptest.NewRecorder()
	srv.router.ServeHTTP(activation, activate)
	if activation.Code != http.StatusOK {
		t.Fatalf("activate reviewer: %d %s", activation.Code, activation.Body.String())
	}
	reviewer, err := cloudidentity.NewPrincipal("reviewer@example.test", []cloudidentity.Role{cloudidentity.RoleReviewer})
	if err != nil {
		t.Fatal(err)
	}
	reviewerToken, err := manager.Issue(reviewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/runs/" + record.RunID + "/delivery/retry"
	denied := httptest.NewRecorder()
	srv.router.ServeHTTP(denied, authenticatedRequest(t, srv, reviewerToken, http.MethodPost, path, ""))
	if denied.Code != http.StatusForbidden || controller.deliveryCalls != 0 {
		t.Fatalf("reviewer must not retry Git delivery: code=%d calls=%d body=%s", denied.Code, controller.deliveryCalls, denied.Body.String())
	}
	request := newLoopbackRequest(http.MethodPost, path, nil)
	request.AddCookie(ownerCookie)
	request.Header.Set("X-CSRF-Token", ownerCSRF)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || controller.deliveryRunID != record.RunID || controller.deliveryCalls != 1 {
		t.Fatalf("authorized retry: code=%d run=%q calls=%d body=%s", response.Code, controller.deliveryRunID, controller.deliveryCalls, response.Body.String())
	}
	requested, err := srv.Store().LatestRunEvent(record.RunID, "delivery_retry_requested")
	if err != nil || requested == nil {
		t.Fatalf("retry actor audit missing: event=%v err=%v", requested, err)
	}
	var requestData map[string]string
	if err := json.Unmarshal([]byte(requested.DataJSON), &requestData); err != nil || requestData["actor_id"] != "owner@example.test" {
		t.Fatalf("retry audit must use the authenticated actor: data=%v err=%v", requestData, err)
	}
}

func TestRetryDeliveryRequiresDurableIntentBeforeControllerCall(t *testing.T) {
	target := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "retry-audit.db")
	controller := &fakeRunController{deliveryErr: errors.New("must not execute")}
	srv, err := NewServer(dbPath, "", filepath.Join(target, ".ai-team", "artifacts"), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := &store.PipelineRun{RunID: "retry-audit-required", Feature: "feat", Status: "completed", StartedAt: time.Now().UTC()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	blocker, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()
	if _, err := blocker.Exec(`CREATE TRIGGER deny_delivery_retry_intent BEFORE INSERT ON events
		WHEN NEW.type = 'delivery_retry_requested' BEGIN SELECT RAISE(ABORT, 'audit storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, http.MethodPost,
		"/api/runs/"+run.RunID+"/delivery/retry", ""))
	if response.Code != http.StatusServiceUnavailable || controller.deliveryCalls != 0 {
		t.Fatalf("delivery must not execute without durable intent audit: status=%d calls=%d body=%s",
			response.Code, controller.deliveryCalls, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "audit storage unavailable") {
		t.Fatalf("internal SQLite error leaked to operator response: %s", response.Body.String())
	}
}

func TestRetryDeliveryFailureIsProjectedSeparatelyFromCompletedRun(t *testing.T) {
	controller := &fakeRunController{deliveryErr: errors.New("delivery push failed")}
	target := t.TempDir()
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := &store.PipelineRun{RunID: "retry-failed-run", Feature: "feat", Status: "completed", StartedAt: time.Now().UTC()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store().CreateStage(&store.Stage{
		PipelineRunID: run.ID, AttemptID: "attempt-1", StageIndex: 1, AgentName: "deployer",
		Status: "passed", StartedAt: run.StartedAt, DeliveryJSON: `{"plan_hash":"` + strings.Repeat("a", 64) + `"}`,
	}); err != nil {
		t.Fatal(err)
	}
	retry := httptest.NewRecorder()
	srv.router.ServeHTTP(retry, authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+run.RunID+"/delivery/retry", ""))
	if retry.Code != http.StatusConflict {
		t.Fatalf("retry failure status=%d body=%s", retry.Code, retry.Body.String())
	}
	details := httptest.NewRecorder()
	srv.router.ServeHTTP(details, authorizedRequest(t, srv, http.MethodGet, "/api/pipelines/"+fmt.Sprint(run.ID), ""))
	var response struct {
		Run      store.PipelineRun     `json:"run"`
		Delivery webDeliveryProjection `json:"delivery"`
	}
	if err := json.NewDecoder(details.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Run.Status != "completed" || response.Delivery.Status != "failed" || response.Delivery.Error != "delivery retry failed; see controller diagnostics" {
		t.Fatalf("Git delivery failure must be separate from completed pipeline: run=%+v delivery=%+v", response.Run, response.Delivery)
	}
	if strings.Contains(retry.Body.String(), "delivery push failed") {
		t.Fatalf("internal delivery error leaked in retry response: %s", retry.Body.String())
	}
}

func TestGetPipelineByID_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines/999", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetPipelineByID_InvalidID(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines/abc", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestGetArtifacts_ListsFeatureFiles(t *testing.T) {
	srv, root := newTestServer(t)

	run := &store.PipelineRun{Feature: "feat-x", Status: "completed", StartedAt: time.Now()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatalf("setup: %v", err)
	}

	featureDir := filepath.Join(root, "feat-x")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "proposal.md"), []byte("# P"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	req := newLoopbackRequest("GET", "/api/pipelines/1/artifacts", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var artifacts []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&artifacts); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0]["path"] != "feat-x/proposal.md" {
		t.Errorf("path = %v", artifacts[0]["path"])
	}
}

func TestGetArtifactsUnknownRunReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	request := newLoopbackRequest(http.MethodGet, "/api/pipelines/999/artifacts", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}

func TestGetArtifactsUsesImmutableRunEvidence(t *testing.T) {
	srv, root := newTestServer(t)
	runID := "20260720T000000.000000000Z-0123456789abcdef"
	run := &store.PipelineRun{RunID: runID, Feature: "feat", Status: "completed", StartedAt: time.Now()}
	if err := srv.Store().CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(root), "runs", runID)
	evidenceFile := filepath.Join(runDir, "attempts", "001-analyst", "artifacts", "proposal", "proposal.md")
	if err := os.MkdirAll(filepath.Dir(evidenceFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidenceFile, []byte("immutable"), 0644); err != nil {
		t.Fatal(err)
	}
	briefFile := filepath.Join(runDir, "brief", "0001-intention.md")
	if err := os.MkdirAll(filepath.Dir(briefFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(briefFile, []byte("business intention"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "feat"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "feat", "proposal.md"), []byte("live-mutated"), 0644)

	req := newLoopbackRequest("GET", fmt.Sprintf("/api/pipelines/%d/artifacts", run.ID), nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	var artifacts []artifactInfo
	_ = json.NewDecoder(w.Body).Decode(&artifacts)
	if len(artifacts) != 2 || artifacts[0].RunID != runID || artifacts[1].RunID != runID {
		t.Fatalf("immutable listing: %+v", artifacts)
	}
	for _, artifact := range artifacts {
		raw := newLoopbackRequest("GET", "/api/runs/"+runID+"/artifacts/"+artifact.Path, nil)
		rawWriter := httptest.NewRecorder()
		srv.router.ServeHTTP(rawWriter, raw)
		want := "immutable"
		if artifact.Path == "brief/0001-intention.md" {
			want = "business intention"
		}
		if rawWriter.Code != http.StatusOK || rawWriter.Body.String() != want {
			t.Fatalf("immutable artifact %s: code=%d body=%q", artifact.Path, rawWriter.Code, rawWriter.Body.String())
		}
	}
}

// Артефакты отдаются ТОЛЬКО внутри artifactRoot: абсолютные пути и traversal
// за пределы корня недоступны (регрессия против arbitrary file read).
func TestGetArtifact_ConfinedToRoot(t *testing.T) {
	srv, root := newTestServer(t)

	if err := os.MkdirAll(filepath.Join(root, "feat"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "feat", "review.md"), []byte("# Review"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Файл вне корня
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	t.Run("valid relative path", func(t *testing.T) {
		req := newLoopbackRequest("GET", "/api/artifacts/feat/review.md", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/markdown" {
			t.Errorf("expected text/markdown, got %s", ct)
		}
		if w.Body.String() != "# Review" {
			t.Errorf("body = %q", w.Body.String())
		}
	})

	for _, path := range []string{
		"/api/artifacts/../secret.txt",
		"/api/artifacts/feat/../../secret.txt",
		"/api/artifacts/" + outside, // абсолютный путь
		"/api/artifacts/etc/passwd",
	} {
		t.Run(path, func(t *testing.T) {
			req := newLoopbackRequest("GET", path, nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				t.Errorf("путь %q не должен отдаваться (код %d)", path, w.Code)
			}
		})
	}
}

func TestGetArtifact_RejectsSymlinkOutsideRoot(t *testing.T) {
	srv, root := newTestServer(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "feat"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	link := filepath.Join(root, "feat", "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	req := newLoopbackRequest("GET", "/api/artifacts/feat/link.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("symlink outside artifact root не должен читаться: %s", w.Body.String())
	}
}

func TestGetArtifact_RejectsOversizedFile(t *testing.T) {
	srv, root := newTestServer(t)
	path := filepath.Join(root, "large.md")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxArtifactSize + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	req := newLoopbackRequest("GET", "/api/artifacts/large.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestGetArtifact_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/artifacts/nonexistent.md", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestNoCORSWildcard(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS wildcard не должен выставляться")
	}
}

func TestSameOriginMiddlewareRejectsHostileHost(t *testing.T) {
	srv, _ := newTestServer(t)

	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host must be rejected, got %d", w.Code)
	}
}

func TestSameOriginMiddlewareRejectsHostileOrigin(t *testing.T) {
	srv, _ := newTestServer(t)

	// Simulates DNS rebinding: Host is loopback (the connection really did
	// land here), but Origin reflects the attacker's domain from the
	// browser's address bar.
	req := newLoopbackRequest("GET", "/api/pipelines", nil)
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("hostile Origin must be rejected even with a loopback Host, got %d", w.Code)
	}
}

func TestSameOriginMiddlewareAllowsLoopbackRequests(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, host := range []string{"127.0.0.1", "127.0.0.1:8080", "localhost", "localhost:8080", "[::1]:8080"} {
		req := newLoopbackRequest("GET", "/api/pipelines", nil)
		req.Host = host
		req.Header.Set("Origin", "http://"+host)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden {
			t.Errorf("loopback host %q must not be rejected, got 403", host)
		}
	}
}

func TestSPAHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('hi')"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	handler := spaHandler(dir)

	t.Run("serves existing file", func(t *testing.T) {
		req := newLoopbackRequest("GET", "/app.js", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("falls back to index.html for unknown routes", func(t *testing.T) {
		req := newLoopbackRequest("GET", "/unknown/route", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != "<html>SPA</html>" {
			t.Errorf("expected SPA fallback, got %q", w.Body.String())
		}
	})
}

func TestNewServerFallsBackToEmbeddedFrontend(t *testing.T) {
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", filepath.Join(t.TempDir(), "missing-dist"), artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	request := newLoopbackRequest(http.MethodGet, "/pipelines/123", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected embedded SPA response, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("response does not contain embedded frontend marker: %q", response.Body.String())
	}
}

// assertJSONStatus проверяет, что ответ машиночитаемый: нужный код и JSON, а
// не страница дашборда.
func assertJSONStatus(t *testing.T, srv *Server, method, target string, status int) map[string]any {
	t.Helper()
	request := newLoopbackRequest(method, target, nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)

	if response.Code != status {
		t.Fatalf("%s %s: expected %d, got %d: %s", method, target, status, response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("%s %s: expected JSON content type, got %q", method, target, contentType)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("%s %s: decode body: %v", method, target, err)
	}
	if payload["error"] == nil || payload["detail"] == nil {
		t.Fatalf("%s %s: expected explanatory body, got %v", method, target, payload)
	}
	return payload
}

// TestUnknownAPIRouteReturnsJSONNotFound фиксирует границу между API и
// SPA-fallback: катч-олл фронтенда раньше отдавал HTML со статусом 200 на любой
// несуществующий /api/-путь.
func TestUnknownAPIRouteReturnsJSONNotFound(t *testing.T) {
	srv := newFrontendTestServer(t)

	t.Run("unknown api path", func(t *testing.T) {
		assertJSONStatus(t, srv, http.MethodGet, "/api/definitely-not-a-route", http.StatusNotFound)
	})

	t.Run("unknown nested api path", func(t *testing.T) {
		assertJSONStatus(t, srv, http.MethodGet, "/api/runs/run-1/definitely-not-a-route", http.StatusNotFound)
	})

	// Корень API без завершающего слэша — самый вероятный пробный URL клиента,
	// и он тоже не должен получать HTML.
	t.Run("api root without slash", func(t *testing.T) {
		assertJSONStatus(t, srv, http.MethodGet, "/api", http.StatusNotFound)
	})

	t.Run("api root with slash", func(t *testing.T) {
		assertJSONStatus(t, srv, http.MethodGet, "/api/", http.StatusNotFound)
	})

	// Несуществующий путь не превращается в 405 из-за SPA-катч-олла: он не
	// обслуживает API-пути ни одним методом, значит и обещать нечего.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run("unknown api path via "+method, func(t *testing.T) {
			request := newLoopbackRequest(method, "/api/definitely-not-a-route", nil)
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
			}
			if allow := response.Header().Values("Allow"); len(allow) != 0 {
				t.Fatalf("404 must not advertise methods, got Allow=%v", allow)
			}
		})
	}

	t.Run("existing route still answers", func(t *testing.T) {
		request := newLoopbackRequest(http.MethodGet, "/api/pipelines", nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
		}
		var runs []any
		if err := json.NewDecoder(response.Body).Decode(&runs); err != nil {
			t.Fatalf("decode: %v", err)
		}
	})

	t.Run("non-api path still serves spa", func(t *testing.T) {
		request := newLoopbackRequest(http.MethodGet, "/pipelines/123", nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", response.Code)
		}
		if response.Body.String() != "<html>SPA</html>" {
			t.Fatalf("expected SPA fallback, got %q", response.Body.String())
		}
	})
}

// TestUnsupportedMethodKeepsAllowHeader сторожит ветку 405. Заголовок
// сравнивается целиком через Values: Get вернул бы только первое значение и не
// заметил бы лишнего метода в списке.
func TestUnsupportedMethodKeepsAllowHeader(t *testing.T) {
	srv := newFrontendTestServer(t)

	cases := []struct {
		method string
		target string
		allow  []string
	}{
		{http.MethodDelete, "/api/pipelines", []string{"GET"}},
		{http.MethodPost, "/api/pipelines", []string{"GET"}},
		{http.MethodPut, "/api/artifacts/feat/review.md", []string{"GET"}},
		{http.MethodPatch, "/api/runs/run-1/cancel", []string{"POST"}},
		{http.MethodOptions, "/api/pipelines", []string{"GET"}},
		{http.MethodHead, "/api/pipelines", []string{"GET"}},
		{http.MethodPut, "/api/runs", []string{"POST"}},
		// GET на путь, зарегистрированный только под POST, раньше проваливался
		// в SPA-fallback и отдавал HTML.
		{http.MethodGet, "/api/runs", []string{"POST"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.target, func(t *testing.T) {
			request := newLoopbackRequest(testCase.method, testCase.target, nil)
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, request)

			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d: %s", response.Code, response.Body.String())
			}
			allow := response.Header().Values("Allow")
			if !slices.Equal(allow, testCase.allow) {
				t.Fatalf("expected Allow %v, got %v", testCase.allow, allow)
			}
			if strings.Contains(response.Body.String(), "<html") {
				t.Fatalf("405 must not carry HTML, got %q", response.Body.String())
			}
		})
	}
}

// TestAllowHeaderOnlyListsAnsweringMethods — свойство, которого не было у
// дефолтного 405 chi: тот включал в Allow метод GET от SPA-катч-олла, хотя GET
// по тому же пути отдаёт 404. Инвариант двусторонний: каждый обещанный метод
// обязан отвечать, а 404 не имеет права обещать что-либо вовсе.
//
// Percent-encoded пути здесь не экзотика: фронтенд строит их штатно через
// encodeURIComponent (web/src/api.ts), и именно на них ответ разъезжался,
// когда роуты искались по декодированному пути, а chi маршрутизировал по
// сырому.
func TestAllowHeaderOnlyListsAnsweringMethods(t *testing.T) {
	srv := newFrontendTestServer(t)
	// Артефакт должен существовать: 404 от artifact-хендлера означал бы
	// отсутствие файла, а не отсутствие роута, и проверку бы исказил.
	if err := os.WriteFile(filepath.Join(srv.artifactRoot, "review.md"), []byte("# review"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets := []string{
		"/api/pipelines",
		"/api/runs",
		"/api/runs/run-1/cancel",
		"/api/artifacts/review.md",
		// Сырая форма не совпадает ни с одним роутом, декодированная — с
		// `/api/runs`; отвечать надо про первую.
		"/api/run%73",
		"/api/pipeline%73",
		// %2F не является разделителем сегментов: путь остаётся неизвестным.
		"/api/pipelines%2Fx",
		"/api%2Ftypo",
		"/api/run%73/r1/cancel",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			probe := newLoopbackRequest(http.MethodPatch, target, nil)
			response := httptest.NewRecorder()
			srv.router.ServeHTTP(response, probe)
			allow := response.Header().Values("Allow")

			if response.Code == http.StatusNotFound {
				if len(allow) != 0 {
					t.Fatalf("404 for %s must not advertise methods, got Allow=%v", target, allow)
				}
				return
			}
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 404 or 405 for %s, got %d", target, response.Code)
			}
			if len(allow) == 0 {
				t.Fatalf("405 for %s must advertise methods", target)
			}

			for _, method := range allow {
				replay := newLoopbackRequest(method, target, nil)
				replayResponse := httptest.NewRecorder()
				srv.router.ServeHTTP(replayResponse, replay)

				if replayResponse.Code == http.StatusNotFound || replayResponse.Code == http.StatusMethodNotAllowed {
					t.Fatalf("Allow advertises %s for %s, but it answers %d", method, target, replayResponse.Code)
				}
			}
		})
	}
}

// TestEncodedPathKeepsRequestedForm: ошибка должна называть тот URL, который
// прислал клиент, а не его декодированную форму — иначе машиночитаемый ответ
// указывает на ресурс, которого не запрашивали.
func TestEncodedPathKeepsRequestedForm(t *testing.T) {
	srv := newFrontendTestServer(t)

	payload := assertJSONStatus(t, srv, http.MethodGet, "/api/pipelines%2Fx", http.StatusNotFound)
	detail, _ := payload["detail"].(string)
	if !strings.Contains(detail, "/api/pipelines%2Fx") {
		t.Fatalf("detail must name the requested URL, got %q", detail)
	}
}

// TestEncodedPathReachesRealHandler сторожит обратную сторону: роуты с
// percent-encoded сегментами, которые фронтенд строит сам, должны доходить до
// своих хендлеров, а не подменяться fallback'ом.
func TestEncodedPathReachesRealHandler(t *testing.T) {
	srv := newFrontendTestServer(t)

	request := newLoopbackRequest(http.MethodGet, "/api/runs/run%2Fone/logs/attempt%20one", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)

	if strings.Contains(response.Body.String(), "no API route matches") {
		t.Fatalf("fallback shadowed the run log handler: %q", response.Body.String())
	}
}

// TestAPIFallbackIsConfigurationIndependent: ответ по API-пути не должен
// зависеть от того, собран ли фронтенд, — иначе контракт держится только в
// проде, а роут `/*` незаметно подменяет коды.
func TestAPIFallbackIsConfigurationIndependent(t *testing.T) {
	withFrontend := newFrontendTestServer(t)
	withoutFrontend, _ := newTestServer(t)

	cases := [][2]string{
		{http.MethodGet, "/api/definitely-not-a-route"},
		{http.MethodPost, "/api/definitely-not-a-route"},
		{http.MethodGet, "/api/run%73"},
		{http.MethodPost, "/api/run%73"},
		{http.MethodGet, "/api/pipelines%2Fx"},
		{http.MethodGet, "/api"},
		{http.MethodGet, "/api/runs"},
		{http.MethodDelete, "/api/pipelines"},
		{http.MethodPatch, "/api/runs/run-1/cancel"},
	}
	for _, testCase := range cases {
		t.Run(testCase[0]+" "+testCase[1], func(t *testing.T) {
			respond := func(srv *Server) (int, []string, string) {
				request := newLoopbackRequest(testCase[0], testCase[1], nil)
				response := httptest.NewRecorder()
				srv.router.ServeHTTP(response, request)
				return response.Code, response.Header().Values("Allow"), response.Header().Get("Content-Type")
			}
			wantCode, wantAllow, wantType := respond(withFrontend)
			gotCode, gotAllow, gotType := respond(withoutFrontend)

			if wantCode != gotCode || !slices.Equal(wantAllow, gotAllow) || wantType != gotType {
				t.Fatalf("frontend build changes API answer: with dist %d %v %q, without dist %d %v %q",
					wantCode, wantAllow, wantType, gotCode, gotAllow, gotType)
			}
		})
	}
}

// TestAPIFallbackDoesNotShadowWildcardRoutes: у артефактных роутов свои
// wildcard-шаблоны, и fallback не должен перехватывать их до хендлера.
func TestAPIFallbackDoesNotShadowWildcardRoutes(t *testing.T) {
	srv := newFrontendTestServer(t)
	if err := os.WriteFile(filepath.Join(srv.artifactRoot, "review.md"), []byte("# review"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("artifact wildcard reaches handler", func(t *testing.T) {
		request := newLoopbackRequest(http.MethodGet, "/api/artifacts/review.md", nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
		}
		if response.Body.String() != "# review" {
			t.Fatalf("unexpected artifact body: %q", response.Body.String())
		}
	})

	// Промах внутри wildcard-роута должен остаться ответом его хендлера
	// (404 от artifact-логики), а не подмениться fallback'ом.
	t.Run("missing artifact stays with its handler", func(t *testing.T) {
		request := newLoopbackRequest(http.MethodGet, "/api/artifacts/missing.md", nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", response.Code)
		}
		if strings.Contains(response.Body.String(), "no API route matches") {
			t.Fatalf("fallback shadowed the artifact handler: %q", response.Body.String())
		}
	})

	// Run-scoped wildcard: неизвестный run отвечает своей ошибкой, а не
	// fallback'ом — значит роут не затенён.
	t.Run("run artifact wildcard reaches handler", func(t *testing.T) {
		request := newLoopbackRequest(http.MethodGet, "/api/runs/unknown-run/artifacts/review.md", nil)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", response.Code)
		}
		if strings.Contains(response.Body.String(), "no API route matches") {
			t.Fatalf("fallback shadowed the run artifact handler: %q", response.Body.String())
		}
	})
}

// TestNonAPIMethodMissKeepsBareResponse: вне /api/ ответ 405 остаётся таким же,
// каким его отдавал дефолтный хендлер chi, — SPA-катч-олл там настоящий роут.
func TestNonAPIMethodMissKeepsBareResponse(t *testing.T) {
	srv := newFrontendTestServer(t)

	request := newLoopbackRequest(http.MethodPost, "/pipelines/123", nil)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", response.Code)
	}
	if allow := response.Header().Values("Allow"); !slices.Equal(allow, []string{"GET"}) {
		t.Fatalf("expected Allow [GET], got %v", allow)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("expected empty body outside /api/, got %q", response.Body.String())
	}
}

// TestUnknownAPIRouteReturnsJSONWithoutFrontend: JSON-404 не должен зависеть от
// того, собран ли фронтенд — без dist роут `/*` вообще не регистрируется и
// запрос доходит до NotFound chi.
func TestUnknownAPIRouteReturnsJSONWithoutFrontend(t *testing.T) {
	srv, _ := newTestServer(t)

	assertJSONStatus(t, srv, http.MethodGet, "/api/definitely-not-a-route", http.StatusNotFound)
}

// newFrontendTestServer поднимает сервер с минимальным собранным фронтендом,
// чтобы SPA-fallback был зарегистрирован как в проде.
func newFrontendTestServer(t *testing.T) *Server {
	t.Helper()
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>SPA</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", dist, t.TempDir())
	if err != nil {
		t.Fatalf("failed to create test server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}
