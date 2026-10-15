package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// inboundController records what the tracker is allowed to cause: continuing
// a ready stage. Any other controller call would show up as a failure of the
// fakeRunController it embeds (no decision, no resume).
type inboundController struct {
	*fakeRunController
	agentCalls []string
	agentErr   error
}

func (c *inboundController) RunStageAgent(_ context.Context, runID, stageID, actorID string, _ bool) error {
	c.agentCalls = append(c.agentCalls, runID+"|"+stageID+"|"+actorID)
	return c.agentErr
}

type inboundFixture struct {
	srv        *Server
	fake       *tracker.Fake
	controller *inboundController
	target     string
}

var defaultInboundRules = []tracker.InboundRule{
	{Status: "In Progress", Action: tracker.ActionAdvance},
	{Status: "Done", Action: tracker.ActionAdvance},
	{Status: "Rework", Action: tracker.ActionReturn, Stage: "code_review", ToStage: "implementation"},
}

func newInboundFixture(t *testing.T, rules []tracker.InboundRule, approvals ...approval.PendingApproval) *inboundFixture {
	t.Helper()
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := tracker.NewFake([]string{"To Do", "In Progress", "In Review", "Rework", "Done"},
		tracker.FakeIssueSeed{Key: "APP-1"}, tracker.FakeIssueSeed{Key: "APP-2"})
	controller := &inboundController{fakeRunController: &fakeRunController{approvals: approvals}}
	srv, err := NewServer(":memory:", "", filepath.Join(controlDir, "artifacts"), WithTargetDir(target), WithRunController(controller),
		WithTracker(fake, trackerTestMaps, "https://ai-team.example"), WithTrackerInbound(rules, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return &inboundFixture{srv: srv, fake: fake, controller: controller, target: target}
}

// task creates a linked task whose lifecycle is at stage/phase.
func (f *inboundFixture) task(t *testing.T, runID, issue string, phase lifecycle.Phase, stage, approvalID string) *store.PipelineRun {
	t.Helper()
	run := createJiraTask(t, f.srv, runID, nil)
	templates, err := f.srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templates.PinCurrentForRun(runID, runID); err != nil {
		t.Fatal(err)
	}
	stateStore, err := lifecycle.NewStore(f.target)
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Create(lifecycle.State{
		RunID: runID, Feature: "feature", TargetDir: f.target, Task: "task", Phase: phase, NextStage: stage,
		PendingApprovalID: approvalID, ConfigSHA256: strings.Repeat("0", 64), WorkflowSHA256: strings.Repeat("1", 64),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	linkTask(t, f.srv, run, issue)
	return run
}

func (f *inboundFixture) sync(t *testing.T) trackerSyncResult {
	t.Helper()
	result, err := f.srv.syncTrackerInbound(context.Background(), "test")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return result
}

// start records the tracker tail so only later changes count.
func (f *inboundFixture) start(t *testing.T) {
	t.Helper()
	f.sync(t)
}

func (f *inboundFixture) requests(t *testing.T, state string) []trackerInboundView {
	t.Helper()
	path := "/api/tracker/inbound"
	if state != "" {
		path += "?state=" + state
	}
	response := projectServe(t, f.srv, http.MethodGet, path, "")
	var body struct {
		Requests []trackerInboundView `json:"requests"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("inbound list: %d %s", response.Code, response.Body.String())
	}
	return body.Requests
}

func stageCheckApproval(runID, id, stage string) approval.PendingApproval {
	return approval.PendingApproval{
		ID: id, RunID: runID, AttemptID: "attempt-1", Kind: approval.KindCheckDecision, Status: approval.StatusPending,
		FromStage: stage, ToStage: stage, Trigger: approval.StageCheckTrigger, SubjectHash: testSubjectHash,
		RequiredRoles: []string{"reviewer"}, Actions: []string{"override", "new_version"}, Targets: map[string]string{"override": stage},
	}
}

func confirmApproval(runID, id, stage string) approval.PendingApproval {
	return approval.PendingApproval{
		ID: id, RunID: runID, AttemptID: "attempt-1", Kind: approval.KindApprove, Status: approval.StatusPending,
		FromStage: stage, ToStage: "qa", Trigger: "stage_ready", SubjectHash: testSubjectHash,
		RequiredRoles: []string{"reviewer"}, Actions: []string{"approve", "return_to_implementation"},
		Targets: map[string]string{"return_to_implementation": "implementation"},
	}
}

func TestInboundAllowedStatusAdvancesGraphThroughController(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	run := f.task(t, "inbound-ok", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)

	if err := f.fake.ExternalTransition("APP-1", "In Progress", "alice"); err != nil {
		t.Fatal(err)
	}
	result := f.sync(t)
	if result.Changes != 1 || result.Applied != 1 {
		t.Fatalf("sync result: %+v", result)
	}
	if len(f.controller.agentCalls) != 1 || f.controller.agentCalls[0] != "inbound-ok|implementation|tracker:fake" {
		t.Fatalf("controller calls: %v", f.controller.agentCalls)
	}
	// The same change is visible through REST (the MCP tools project these routes).
	applied := f.requests(t, "applied")
	if len(applied) != 1 || applied[0].ToStatus != "In Progress" || applied[0].ResolvedBy != "tracker:fake" || applied[0].Stage != "implementation" {
		t.Fatalf("applied requests: %+v", applied)
	}
	state := runTrackerState(t, f.srv, run)
	inbound, _ := state["inbound"].([]any)
	if len(inbound) != 1 || state["sync_status"] != "ok" {
		t.Fatalf("task tracker view: %+v", state)
	}
	// Re-reading the same page (cursor lost) neither re-applies nor duplicates.
	if err := f.srv.store.SaveTrackerSyncCursor("fake", "0", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	again := f.sync(t)
	if again.Duplicates != 1 || again.Applied != 0 || len(f.controller.agentCalls) != 1 || len(f.requests(t, "")) != 1 {
		t.Fatalf("replay: %+v calls=%v", again, f.controller.agentCalls)
	}
}

func TestInboundPrematureDoneDoesNotBypassFailedGateOrConfirmation(t *testing.T) {
	for name, tc := range map[string]struct {
		approval approval.PendingApproval
		reason   string
		allowed  string
	}{
		"failed gate":        {stageCheckApproval("done-gate", "ap-gate", "code_review"), "checks did not pass", "override"},
		"human confirmation": {confirmApproval("done-confirm", "ap-confirm", "code_review"), "human confirmation", "confirm"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newInboundFixture(t, defaultInboundRules, tc.approval)
			run := f.task(t, tc.approval.RunID, "APP-1", lifecycle.PhaseWaiting, "code_review", tc.approval.ID)
			f.start(t)
			if err := f.fake.ExternalTransition("APP-1", "Done", "bob"); err != nil {
				t.Fatal(err)
			}
			result := f.sync(t)
			if result.Pending != 1 || result.Applied != 0 {
				t.Fatalf("sync result: %+v", result)
			}
			if len(f.controller.agentCalls) != 0 || f.controller.resumeCalls != 0 || f.controller.decisionCalls != 0 {
				t.Fatalf("controller was driven: agent=%v resume=%d decide=%d", f.controller.agentCalls, f.controller.resumeCalls, f.controller.decisionCalls)
			}
			open := f.requests(t, "open")
			if len(open) != 1 || open[0].State != store.TrackerInboundPending || !strings.Contains(open[0].Reason, tc.reason) {
				t.Fatalf("open requests: %+v", open)
			}
			if !actionAllowed(open[0].AllowedActions, tc.allowed) || !strings.Contains(open[0].SuggestedAction, tc.allowed) {
				t.Fatalf("operator is not offered %q: allowed=%v suggested=%q", tc.allowed, open[0].AllowedActions, open[0].SuggestedAction)
			}
			if state := runTrackerState(t, f.srv, run); state["sync_status"] != "inbound_pending" {
				t.Fatalf("sync status: %v", state["sync_status"])
			}
			// Dismissing needs a reason and changes nothing in the task.
			path := "/api/runs/" + run.RunID + "/tracker/inbound/" + open[0].ChangeID + "/dismiss"
			if r := projectServe(t, f.srv, http.MethodPost, path, `{}`); r.Code != http.StatusBadRequest || errorCode(r) != "reason_required" {
				t.Fatalf("dismiss without reason: %d %s", r.Code, r.Body.String())
			}
			if r := projectServe(t, f.srv, http.MethodPost, path, `{"reason":"jira moved by mistake"}`); r.Code != http.StatusOK {
				t.Fatalf("dismiss: %d %s", r.Code, r.Body.String())
			}
			if r := projectServe(t, f.srv, http.MethodPost, path, `{"reason":"again"}`); r.Code != http.StatusConflict || errorCode(r) != "request_not_open" {
				t.Fatalf("second dismiss: %d %s", r.Code, r.Body.String())
			}
			if len(f.requests(t, "open")) != 0 || len(f.controller.agentCalls) != 0 {
				t.Fatal("dismissal changed the task or left the request open")
			}
		})
	}
}

func TestInboundAmbiguousStatusIsConflictAndDoesNotMoveGraph(t *testing.T) {
	rules := []tracker.InboundRule{
		{Status: "Done", Action: tracker.ActionAdvance},
		{Status: "done", Action: tracker.ActionReturn, Stage: "code_review", ToStage: "implementation"},
	}
	f := newInboundFixture(t, rules)
	// A ready stage that would otherwise be started by "Done".
	f.task(t, "inbound-ambiguous", "APP-1", lifecycle.PhaseResumable, "code_review", "")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "Done", "carol"); err != nil {
		t.Fatal(err)
	}
	result := f.sync(t)
	if result.Conflicts != 1 || result.Applied != 0 || len(f.controller.agentCalls) != 0 {
		t.Fatalf("result %+v calls %v", result, f.controller.agentCalls)
	}
	open := f.requests(t, "conflict")
	if len(open) != 1 || !strings.Contains(open[0].Reason, "ambiguous") || open[0].Action != "" {
		t.Fatalf("conflict: %+v", open)
	}
}

func TestInboundReturnIsAPendingRequestForTheApprover(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules, confirmApproval("inbound-return", "ap-ret", "code_review"))
	f.task(t, "inbound-return", "APP-1", lifecycle.PhaseWaiting, "code_review", "ap-ret")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "Rework", "dave"); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	open := f.requests(t, "open")
	if len(open) != 1 || open[0].Action != tracker.ActionReturn || open[0].ToStage != "implementation" ||
		!actionAllowed(open[0].AllowedActions, "return") || f.controller.decisionCalls != 0 {
		t.Fatalf("return request: %+v decisions=%d", open, f.controller.decisionCalls)
	}
}

func TestInboundReturnWithoutConfiguredRouteIsConflict(t *testing.T) {
	rules := []tracker.InboundRule{{Status: "Rework", Action: tracker.ActionReturn, ToStage: "tech_design"}}
	f := newInboundFixture(t, rules, confirmApproval("inbound-noroute", "ap-nr", "code_review"))
	f.task(t, "inbound-noroute", "APP-1", lifecycle.PhaseWaiting, "code_review", "ap-nr")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "Rework", "dave"); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	open := f.requests(t, "conflict")
	if len(open) != 1 || !strings.Contains(open[0].Reason, "no configured return route") {
		t.Fatalf("expected route conflict: %+v", open)
	}
}

func TestInboundOutageIsRecoveredWithoutDuplicates(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-outage", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "erin"); err != nil {
		t.Fatal(err)
	}
	f.fake.FailNext("changes", errors.New("tracker timeout"), 2)
	for range 2 {
		if _, err := f.srv.syncTrackerInbound(context.Background(), "test"); err == nil {
			t.Fatal("sync must fail while the tracker is down")
		}
	}
	state, err := f.srv.store.GetTrackerSyncState("fake")
	if err != nil || state == nil || state.ConsecutiveFailures != 2 || !strings.Contains(state.LastError, "tracker timeout") || state.LastErrorAt == nil {
		t.Fatalf("failure is not visible: %+v err=%v", state, err)
	}
	if len(f.controller.agentCalls) != 0 {
		t.Fatal("nothing may be applied during the outage")
	}
	summary := projectServe(t, f.srv, http.MethodGet, "/api/tracker/sync", "")
	if !strings.Contains(summary.Body.String(), `"status":"failed"`) {
		t.Fatalf("sync summary: %s", summary.Body.String())
	}
	// The manual sync endpoint reports the failure the same way.
	f.fake.FailNext("changes", errors.New("tracker timeout"), 1)
	if r := projectServe(t, f.srv, http.MethodPost, "/api/tracker/sync", ""); r.Code != http.StatusBadGateway || errorCode(r) != "tracker_unavailable" {
		t.Fatalf("sync now during outage: %d %s", r.Code, r.Body.String())
	}
	f.fake.ClearFailures()
	response := projectServe(t, f.srv, http.MethodPost, "/api/tracker/sync", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"applied":1`) {
		t.Fatalf("recovery sync: %d %s", response.Code, response.Body.String())
	}
	if again := f.sync(t); again.Changes != 0 || len(f.controller.agentCalls) != 1 || len(f.requests(t, "")) != 1 {
		t.Fatalf("duplicates after recovery: %+v calls=%v", again, f.controller.agentCalls)
	}
	state, _ = f.srv.store.GetTrackerSyncState("fake")
	if state.LastError != "" || state.ConsecutiveFailures != 0 || state.LastSuccessAt == nil {
		t.Fatalf("recovered state: %+v", state)
	}
}

func TestInboundDoesNotLoopOnOwnOutboundTransition(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	run := f.task(t, "inbound-echo", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	// ai-team's own stage entry moves the card (on_enter: In Progress).
	trackerEvent(t, f.srv, run, "attempt_started", "implementation", "attempt-echo", `{}`)
	eventually(t, "outbound transition", func() bool {
		status, _ := f.fake.Status("APP-1")
		return status == "In Progress"
	})
	result := f.sync(t)
	if result.Changes != 1 || result.Ignored != 1 || result.Applied != 0 || result.Pending != 0 || len(f.controller.agentCalls) != 0 {
		t.Fatalf("own transition caused a request: %+v calls=%v", result, f.controller.agentCalls)
	}
	ignored := f.requests(t, "ignored")
	if len(ignored) != 1 || ignored[0].Origin != tracker.OriginAITeam || !strings.Contains(ignored[0].Reason, "ai-team itself") {
		t.Fatalf("ignored: %+v", ignored)
	}
}

func TestInboundUnmappedAndUnlinkedChangesAreHarmless(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.task(t, "inbound-unmapped", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "In Review", "frank"); err != nil { // not in the map
		t.Fatal(err)
	}
	if err := f.fake.ExternalTransition("APP-2", "Done", "frank"); err != nil { // no linked task
		t.Fatal(err)
	}
	result := f.sync(t)
	if result.Ignored != 1 || result.Applied != 0 || len(f.controller.agentCalls) != 0 {
		t.Fatalf("result: %+v", result)
	}
	if ignored := f.requests(t, "ignored"); len(ignored) != 1 || !strings.Contains(ignored[0].Reason, "not mapped") {
		t.Fatalf("ignored: %+v", ignored)
	}
}

func TestInboundPendingRequestIsSupersededWhenTaskMovesOn(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules, confirmApproval("inbound-moves", "ap-mv", "code_review"))
	run := f.task(t, "inbound-moves", "APP-1", lifecycle.PhaseWaiting, "code_review", "ap-mv")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "Done", "gina"); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	if len(f.requests(t, "pending")) != 1 {
		t.Fatal("expected a pending request")
	}
	stateStore, err := lifecycle.NewStore(f.target)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := stateStore.Load(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Phase, next.NextStage, next.PendingApprovalID = lifecycle.PhaseRunning, "qa", ""
	if err := stateStore.Save(previous, next); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	superseded := f.requests(t, "superseded")
	if len(superseded) != 1 || !strings.Contains(superseded[0].Reason, "qa") || len(f.requests(t, "open")) != 0 {
		t.Fatalf("superseded: %+v", superseded)
	}
}

func TestInboundControllerRefusalStaysPending(t *testing.T) {
	f := newInboundFixture(t, defaultInboundRules)
	f.controller.agentErr = errors.New("visit limit reached")
	f.task(t, "inbound-refused", "APP-1", lifecycle.PhaseResumable, "implementation", "")
	f.start(t)
	if err := f.fake.ExternalTransition("APP-1", "In Progress", "hank"); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	open := f.requests(t, "pending")
	if len(open) != 1 || !strings.Contains(open[0].Reason, "visit limit reached") {
		t.Fatalf("a controller refusal must stay visible: %+v", open)
	}
}

func TestInboundSyncRequiresConfiguration(t *testing.T) {
	fake := tracker.NewFake(nil)
	srv := newTrackerServer(t, ":memory:", fake) // outbound only
	if r := projectServe(t, srv, http.MethodPost, "/api/tracker/sync", ""); r.Code != http.StatusConflict || errorCode(r) != "inbound_not_configured" {
		t.Fatalf("sync without inbound: %d %s", r.Code, r.Body.String())
	}
	status := projectServe(t, srv, http.MethodGet, "/api/tracker", "")
	if !strings.Contains(status.Body.String(), `"inbound":{"configured":false}`) {
		t.Fatalf("status: %s", status.Body.String())
	}
}
