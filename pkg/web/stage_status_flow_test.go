package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
	"github.com/arturpanteleev/ai-team/pkg/stagestatus"
)

// stage_status_flow_test.go — S11 end to end. The task of stage_status_e2e_test.go
// runs through the real HTTP handlers, the real controller, the real pipeline,
// the real approval store and the real SQLite journal. Every interesting
// moment is read back through the API; the return code_review ->
// implementation gives the implementation stage two visits.

// s11Snapshot is what the API answered at one moment of the run: the task view
// (with the projection), the task list card and the dedicated route.
type s11Snapshot struct {
	label  string
	detail taskRunDetails
	list   taskRunListItem
	route  []byte
}

func (v s11Snapshot) stage(t *testing.T, id string) stagestatus.Stage {
	t.Helper()
	if v.detail.StageStatuses == nil {
		t.Fatalf("%s: the task view has no stage_statuses", v.label)
	}
	for _, stage := range v.detail.StageStatuses.Stages {
		if stage.StageID == id {
			return stage
		}
	}
	t.Fatalf("%s: no stage %s", v.label, id)
	return stagestatus.Stage{}
}

func (v s11Snapshot) log(t *testing.T) {
	t.Helper()
	var parts []string
	for _, stage := range v.detail.StageStatuses.Stages {
		part := stage.StageID + "=" + string(stage.Status)
		if stage.Current {
			part += "*"
		}
		part += "[" + string(stage.Executor.Kind) + " visits=" + strconv.Itoa(stage.VisitCount) + "]"
		parts = append(parts, part)
	}
	t.Logf("%s: %s", v.label, strings.Join(parts, " "))
}

func actionIn(actions []string, wanted string) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

func TestStageStatusesEndToEnd_IdeaToProdWithReturn(t *testing.T) {
	cfg := s11Config()
	registry := s11Registry()
	if err := cfg.Validate(registry); err != nil {
		t.Fatalf("fixture template: %v", err)
	}
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	f := newTemplateFixture(t, string(data), false, nil, map[string]lifecycle.State{})
	rt := &s11Runtime{}
	external := &s11External{rt: rt}
	p := pipeline.New(cfg, registry,
		pipeline.WithRuntimeFactory(func(string) (runtime.Runtime, error) { return rt, nil }),
		pipeline.WithNotifier(e2eNotifier{}),
		pipeline.WithRecorder(NewStoreRecorder(f.srv.store)),
		pipeline.WithExternalExecutorFactory(func(string, stageexec.AdapterSpec) (stageexec.StageExecutor, error) { return external, nil }),
	)
	engine := pipeline.NewRunEngine(p)
	taskDir := filepath.Join(f.target, ".ai-team", "artifacts", "tasks", "feat")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte("задача"), 0o644); err != nil {
		t.Fatal(err)
	}

	var runID string
	var snapMu sync.Mutex
	taken := map[string]s11Snapshot{}
	// snap reads the API exactly as a browser does. It is called from the
	// goroutines of the agents, so a failure is reported without Fatal.
	snap := func(label string) s11Snapshot {
		t.Helper()
		snapMu.Lock()
		defer snapMu.Unlock()
		value := s11Snapshot{label: label}
		response := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs/"+runID, "")
		if response.Code != http.StatusOK {
			t.Errorf("%s: GET task: %d %s", label, response.Code, response.Body.String())
			return value
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value.detail); err != nil {
			t.Errorf("%s: %v", label, err)
		}
		listResponse := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs", "")
		var list struct {
			Runs []taskRunListItem `json:"runs"`
		}
		_ = json.Unmarshal(listResponse.Body.Bytes(), &list)
		for _, item := range list.Runs {
			if item.RunID == runID {
				value.list = item
			}
		}
		route := f.requestAs(t, "owner@example.test", s11Roles, http.MethodGet, "/api/runs/"+runID+"/stage-statuses", "")
		if route.Code != http.StatusOK {
			t.Errorf("%s: GET stage-statuses: %d %s", label, route.Code, route.Body.String())
		}
		value.route = route.Body.Bytes()
		taken[label] = value
		if value.detail.StageStatuses == nil {
			t.Errorf("%s: no projection in the task view", label)
			return value
		}
		value.log(t)
		// One concept everywhere: the card, the task view and the route agree.
		var routeProjection stagestatus.Projection
		if err := json.Unmarshal(value.route, &routeProjection); err != nil {
			t.Errorf("%s: route JSON: %v", label, err)
		}
		if !reflect.DeepEqual(&routeProjection, value.detail.StageStatuses) {
			t.Errorf("%s: the route and the task view disagree:\n%s\nvs\n%+v", label, value.route, value.detail.StageStatuses)
		}
		summary := value.detail.StageStatuses.Summary()
		if value.list.StageStatus == nil || !reflect.DeepEqual(*value.list.StageStatus, summary) {
			t.Errorf("%s: the task card disagrees with the task view: %+v vs %+v", label, value.list.StageStatus, summary)
		}
		return value
	}

	waitIdle := func(label string, controller *control.Controller) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			states, err := lifecycle.NewStore(f.target)
			if err != nil {
				t.Fatal(err)
			}
			state, loadErr := states.Load(runID)
			if loadErr == nil && !controller.RunActive(runID) && state.Phase != lifecycle.PhaseRunning {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the task did not settle: %+v %v", label, state, loadErr)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	post := func(label, path, body string, want int) {
		t.Helper()
		response := f.requestAs(t, "owner@example.test", s11Roles, http.MethodPost, path, body)
		if response.Code != want {
			t.Fatalf("%s: POST %s = %d %s", label, path, response.Code, response.Body.String())
		}
	}
	rt.hook = func(name string) {
		rt.mu.Lock()
		coders, reviews := rt.coders, rt.reviews
		rt.mu.Unlock()
		switch name {
		case "architect":
			snap("design: internal agent works")
		case "verifier":
			snap("design: checker runs")
		case "coder":
			snap("implementation: internal agent works, visit " + strconv.Itoa(coders))
		case "code-reviewer":
			snap("code_review: external agent works, review " + strconv.Itoa(reviews))
		}
	}

	started, startErr := engine.Start(context.Background(), pipeline.RunConfig{Feature: "feat", TaskDesc: "задача", TargetDir: f.target})
	var required *pipeline.ApprovalRequiredError
	if !errors.As(startErr, &required) {
		t.Fatalf("the human spec stage must park the run: %v", startErr)
	}
	runID = started.RunID
	controller, err := control.New(engine, f.target)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.controller = controller

	// decide answers the pending approval the way the UI does: the decision
	// endpoint with the exact subject hash, then the resume of the task.
	decide := func(label, action, comment string) {
		t.Helper()
		current := snap(label)
		var pending *approval.PendingApproval
		for index := range current.detail.Approvals {
			if current.detail.State != nil && current.detail.Approvals[index].ID == current.detail.State.PendingApprovalID {
				pending = &current.detail.Approvals[index]
			}
		}
		if pending == nil {
			t.Fatalf("%s: no pending approval", label)
		}
		body, _ := json.Marshal(map[string]any{"actor_id": "owner@example.test", "actor_role": pending.RequiredRoles[0],
			"action": action, "comment": comment, "subject_hash": pending.SubjectHash})
		post(label, "/api/runs/"+runID+"/approvals/"+pending.ID+"/decisions", string(body), http.StatusOK)
		post(label+" resume", "/api/runs/"+runID+"/resume", "", http.StatusAccepted)
		waitIdle(label, controller)
	}
	stagePath := func(stage, action string) string { return "/api/runs/" + runID + "/stages/" + stage + "/" + action }

	// 1. The task waits for a person at the first stage; nothing else is reached.
	v := snap("1 spec ready")
	spec := v.stage(t, "product_spec")
	if spec.Status != stagestatus.StatusReady || spec.Readiness != stagestatus.ReadinessReady || spec.Phase != stagestatus.PhaseNone ||
		spec.Executor.Kind != stagestatus.ExecutorHuman || spec.BlockedBy == nil || spec.BlockedBy.Kind != stagestatus.BlockHumanInput ||
		!spec.Current || spec.ActiveVisit != 1 || len(spec.AllowedActions) == 0 {
		t.Fatalf("spec must be ready for a person: %+v", spec)
	}
	for _, id := range []string{"tech_design", "implementation", "code_review", "qa"} {
		if later := v.stage(t, id); later.Status != stagestatus.StatusNotReached || later.Visited || later.Current || len(later.Visits) != 0 {
			t.Fatalf("%s must not be reached yet: %+v", id, later)
		}
	}
	if v.stage(t, "tech_design").Executor.Kind != stagestatus.ExecutorInternalAgent || v.stage(t, "code_review").Executor.Kind != stagestatus.ExecutorExternalAgent {
		t.Fatal("an unreached stage already says who will do it: internal agent for the design, external agent for the review")
	}

	// 2. A person takes the stage: it is human work now.
	post("take spec", stagePath("product_spec", "take"), "", http.StatusOK)
	spec = snap("2 spec taken by a person").stage(t, "product_spec")
	if spec.Status != stagestatus.StatusInProgress || spec.Phase != stagestatus.PhaseWorking || spec.Executor.Kind != stagestatus.ExecutorHuman ||
		spec.Executor.Actor != "owner@example.test" {
		t.Fatalf("a taken stage is in progress by that person: %+v", spec)
	}

	// 3-5. The person submits; the internal agent designs, the checker checks,
	// the result waits for a person's confirmation.
	post("submit spec", stagePath("product_spec", "submit"), `{"result":"md","content":"# Спецификация\n\n## Критерии приёмки\n\n- ok\n"}`, http.StatusCreated)
	waitIdle("spec submitted", controller)
	working := taken["design: internal agent works"]
	design := working.stage(t, "tech_design")
	if design.Status != stagestatus.StatusInProgress || design.Phase != stagestatus.PhaseWorking || design.Executor.Kind != stagestatus.ExecutorInternalAgent ||
		working.stage(t, "product_spec").Status != stagestatus.StatusPassed || working.stage(t, "product_spec").Current {
		t.Fatalf("the design is internal agent work and the spec is behind: %+v", design)
	}
	if past := working.stage(t, "product_spec").Visits; len(past) != 1 || past[0].Active || past[0].Outcome != "passed" || past[0].Executor.Kind != stagestatus.ExecutorHuman {
		t.Fatalf("the spec visit is finished and inactive: %+v", past)
	}
	checking := taken["design: checker runs"].stage(t, "tech_design")
	if checking.Status != stagestatus.StatusChecking || checking.Phase != stagestatus.PhaseChecking {
		t.Fatalf("while the checker runs the stage is being checked, not merely running: %+v", checking)
	}
	awaiting := snap("3 design awaits confirmation").stage(t, "tech_design")
	if awaiting.Status != stagestatus.StatusAwaitingHuman || awaiting.Phase != stagestatus.PhaseAwaitingHuman || awaiting.BlockedBy == nil ||
		awaiting.BlockedBy.Kind != stagestatus.BlockApproval || awaiting.Outcome != "passed" || !actionIn(awaiting.AllowedActions, "confirm") ||
		awaiting.ActionStageID != "implementation" {
		t.Fatalf("a finished design that waits for a person is awaiting_human with the confirmation open: %+v", awaiting)
	}
	if taken["3 design awaits confirmation"].stage(t, "implementation").Status != stagestatus.StatusNotReached {
		t.Fatal("the next stage is not reached while the confirmation is open")
	}

	// 6-8. Implementation by the internal agent, the external review asks for changes.
	decide("4 confirm design", "approve", "")
	visit1 := taken["implementation: internal agent works, visit 1"]
	if impl := visit1.stage(t, "implementation"); impl.Status != stagestatus.StatusInProgress || impl.Executor.Kind != stagestatus.ExecutorInternalAgent ||
		impl.ActiveVisit != 1 || visit1.stage(t, "tech_design").Status != stagestatus.StatusPassed {
		t.Fatalf("implementation visit 1 runs by the internal agent: %+v", impl)
	}
	external1 := taken["code_review: external agent works, review 1"].stage(t, "code_review")
	if external1.Status != stagestatus.StatusInProgress || external1.Executor.Kind != stagestatus.ExecutorExternalAgent || external1.Executor.Name != "container-review" {
		t.Fatalf("the review is external agent work: %+v", external1)
	}
	review := snap("5 review asks for changes").stage(t, "code_review")
	if review.Status != stagestatus.StatusAwaitingHuman || review.Outcome != "rejected" || review.Executor.Source != stagestatus.SourceAttempt ||
		review.Executor.Kind != stagestatus.ExecutorExternalAgent || review.BlockedBy == nil || !actionIn(review.BlockedBy.Actions, "return_to_implementation") {
		t.Fatalf("a rejected review waits for the person's return decision, executed by the external agent: %+v", review)
	}

	// Restart in the middle: a second server on the same database and target
	// serves the same projection.
	assertSameAfterRestart(t, f, runID, taken["5 review asks for changes"].route)

	// 9. The return: implementation has two visits, the first is finished and
	// inactive, the second is the one that works; the review is not reached again.
	decide("6 return the review to implementation", "return_to_implementation", "нужны правки")
	again := taken["implementation: internal agent works, visit 2"]
	impl := again.stage(t, "implementation")
	if impl.Status != stagestatus.StatusInProgress || impl.VisitCount != 2 || impl.ActiveVisit != 2 || !impl.Current || len(impl.Visits) != 2 {
		t.Fatalf("implementation must be at its second visit: %+v", impl)
	}
	first, second := impl.Visits[0], impl.Visits[1]
	if first.Active || first.Outcome != "passed" || first.LeftTo != "code_review" || first.LeftAt == nil || len(first.Attempts) != 1 ||
		first.Attempts[0].Outcome != "passed" || first.Attempts[0].AttemptID == "" {
		t.Fatalf("the first visit is finished and inactive, and its attempt is kept: %+v", first)
	}
	if !second.Active || second.EnteredVia != stagestatus.EnteredReturn || second.EnteredFrom != "code_review" || second.Outcome != "pending" ||
		len(second.Attempts) != 1 || second.Attempts[0].AttemptID == first.Attempts[0].AttemptID || second.Attempts[0].Outcome != "pending" {
		t.Fatalf("the second visit is the active one, entered by a return, with its own pending attempt: %+v", second)
	}
	reviewBehind := again.stage(t, "code_review")
	if reviewBehind.Status != stagestatus.StatusNotReached || !reviewBehind.Visited || reviewBehind.Current || reviewBehind.VisitCount != 1 ||
		len(reviewBehind.Visits) != 1 || reviewBehind.Visits[0].Active || !reviewBehind.Visits[0].Returned || reviewBehind.Visits[0].Outcome != "rejected" ||
		!reviewBehind.Visits[0].Superseded || reviewBehind.Visits[0].LeftTo != "implementation" {
		t.Fatalf("the review that was returned from is not reached again, yet it was active before: %+v", reviewBehind)
	}
	secondReview := taken["code_review: external agent works, review 2"]
	if reviewAgain := secondReview.stage(t, "code_review"); reviewAgain.Status != stagestatus.StatusInProgress ||
		reviewAgain.VisitCount != 2 || reviewAgain.ActiveVisit != 2 || reviewAgain.Visits[0].Active ||
		secondReview.stage(t, "implementation").Visits[1].Active {
		t.Fatalf("the second review is a new visit of the review: %+v", reviewAgain)
	}

	// 10. The second review passes; QA is ready for a person.
	approved := snap("7 second review awaits confirmation").stage(t, "code_review")
	if approved.Status != stagestatus.StatusAwaitingHuman || approved.Outcome != "passed" || approved.VisitCount != 2 {
		t.Fatalf("the second review awaits confirmation: %+v", approved)
	}
	decide("8 confirm review", "approve", "")
	v = snap("9 qa ready")
	qa := v.stage(t, "qa")
	if qa.Status != stagestatus.StatusReady || qa.Executor.Kind != stagestatus.ExecutorHuman || v.stage(t, "code_review").Status != stagestatus.StatusPassed ||
		v.stage(t, "implementation").Status != stagestatus.StatusPassed || v.stage(t, "implementation").VisitCount != 2 {
		t.Fatalf("qa is ready, everything behind it passed: %+v", v.detail.StageStatuses)
	}
	for _, visit := range v.stage(t, "implementation").Visits {
		if visit.Active {
			t.Fatalf("a stage that is not current has no active visit: %+v", visit)
		}
	}

	// 11. QA by a person ends the task.
	post("take qa", stagePath("qa", "take"), "", http.StatusOK)
	post("submit qa", stagePath("qa", "submit"), `{"result":"md","content":"# QA\n\nвсё прошло\n"}`, http.StatusCreated)
	waitIdle("qa submitted", controller)
	final := snap("10 finished")
	projection := final.detail.StageStatuses
	if !projection.Terminal || projection.CurrentStage != "" {
		t.Fatalf("a finished task has no current stage: %+v", projection)
	}
	for _, stage := range projection.Stages {
		if stage.Status != stagestatus.StatusPassed || stage.Current || stage.Readiness != stagestatus.ReadinessLeft {
			t.Fatalf("everything passed: %+v", stage)
		}
		for _, visit := range stage.Visits {
			if visit.Active {
				t.Fatalf("no visit is active after the end: %+v", visit)
			}
		}
	}
	if final.stage(t, "implementation").VisitCount != 2 || final.stage(t, "code_review").VisitCount != 2 {
		t.Fatal("the history of both loops stays after the task ended")
	}

	// Restart: the projection of the finished task is the same.
	assertSameAfterRestart(t, f, runID, final.route)

	// The projection is the journal replayed: the hash-chained evidence log
	// (a different source than the task feed) projects to the same stages.
	assertEvidenceReplayMatches(t, f, runID, projection)

	// A status cannot be forged: there is no write route, and the action
	// routes refuse a status in the body.
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
		response := f.requestAs(t, "owner@example.test", s11Roles, method, "/api/runs/"+runID+"/stage-statuses", `{"status":"passed"}`)
		if response.Code != http.StatusMethodNotAllowed && response.Code != http.StatusNotFound {
			t.Fatalf("%s stage-statuses must not exist: %d", method, response.Code)
		}
	}
	forged := f.requestAs(t, "owner@example.test", s11Roles, http.MethodPost, stagePath("qa", "take"), `{"status":"passed","stage_statuses":{}}`)
	if forged.Code < 400 {
		t.Fatalf("an action carrying a status must be refused: %d %s", forged.Code, forged.Body.String())
	}
	after := snap("11 after forgery attempts")
	if !reflect.DeepEqual(after.detail.StageStatuses, projection) {
		t.Fatal("forgery attempts changed the projection")
	}
}

// assertSameAfterRestart opens a second server on the same database and target
// and compares its answer with the one the first server gave.
func assertSameAfterRestart(t *testing.T, f functionPermissionFixture, runID string, want []byte) {
	t.Helper()
	srv, err := NewServer(f.dbPath, "", filepath.Join(f.target, ".ai-team", "artifacts"), WithTargetDir(f.target), WithRunController(f.srv.controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	request := authorizedRequest(t, srv, http.MethodGet, "/api/runs/"+runID+"/stage-statuses", "")
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("after restart: %d %s", response.Code, response.Body.String())
	}
	sameJSON(t, "after restart", response.Body.Bytes(), want)
}

// assertEvidenceReplayMatches projects the verified evidence journal and
// compares it with the projection of the task feed.
func assertEvidenceReplayMatches(t *testing.T, f functionPermissionFixture, runID string, want *stagestatus.Projection) {
	t.Helper()
	events := f.e2eEvents(t, runID)
	run, err := f.srv.store.GetPipelineRunByRunID(runID)
	if err != nil || run == nil {
		t.Fatal("run record")
	}
	cfg, found, err := f.srv.pinnedTaskConfig(runID)
	if err != nil || !found {
		t.Fatalf("pinned template: %v", err)
	}
	input := f.srv.stageStatusInput(run, cfg, nil, nil, nil)
	for _, event := range events {
		input.Events = append(input.Events, stagestatus.Event{
			Sequence: int64(event.Sequence), Type: event.Type, Stage: event.Stage, AttemptID: event.AttemptID,
			Timestamp: event.Timestamp, Data: event.Data,
		})
	}
	got := stagestatus.Project(input)
	if len(got.Stages) != len(want.Stages) || got.Terminal != want.Terminal {
		t.Fatalf("replay differs: %+v vs %+v", got, want)
	}
	for index, stage := range got.Stages {
		expected := want.Stages[index]
		if stage.Status != expected.Status || stage.VisitCount != expected.VisitCount || stage.Readiness != expected.Readiness || stage.Outcome != expected.Outcome {
			t.Fatalf("replay of %s from the evidence journal differs: %s/%d/%s vs %s/%d/%s", stage.StageID,
				stage.Status, stage.VisitCount, stage.Outcome, expected.Status, expected.VisitCount, expected.Outcome)
		}
		for position, visit := range stage.Visits {
			other := expected.Visits[position]
			if visit.Outcome != other.Outcome || visit.EnteredVia != other.EnteredVia || visit.LeftTo != other.LeftTo ||
				visit.Active != other.Active || visit.Superseded != other.Superseded || len(visit.Attempts) != len(other.Attempts) {
				t.Fatalf("replay of %s visit %d differs: %+v vs %+v", stage.StageID, position+1, visit, other)
			}
		}
	}
}
