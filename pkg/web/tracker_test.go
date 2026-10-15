package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

var trackerTestMaps = tracker.Maps{
	OnEnter: map[string]string{"implementation": "In Progress"},
	OnPass:  map[string]string{"implementation": "In Review", "qa": "Done"},
}

func newTrackerServer(t *testing.T, database string, fake *tracker.Fake) *Server {
	t.Helper()
	target := t.TempDir()
	srv, err := NewServer(database, "", target, WithTargetDir(target), WithRunController(&fakeRunController{}),
		WithTracker(fake, trackerTestMaps, "https://ai-team.example"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func createJiraTask(t *testing.T, srv *Server, runID string, links []string) *store.PipelineRun {
	t.Helper()
	snapshot, err := json.Marshal(startRunCommand{Task: "stage event integration test", ExternalLinks: links})
	if err != nil {
		t.Fatal(err)
	}
	run := &store.PipelineRun{
		RunID: runID, Feature: "tracker-test", Status: "running", StartedAt: time.Now().UTC(), ConfigSnapshot: string(snapshot),
	}
	if err := srv.store.CreatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func trackerEvent(t *testing.T, srv *Server, run *store.PipelineRun, eventType, stage, attempt, data string) {
	t.Helper()
	if err := srv.store.AppendEventNext(&store.Event{
		RunID: run.RunID, Type: eventType, Stage: stage, AttemptID: attempt, Timestamp: time.Now().UTC(), DataJSON: data,
	}); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func linkTask(t *testing.T, srv *Server, run *store.PipelineRun, key string) {
	t.Helper()
	response := projectServe(t, srv, http.MethodPut, "/api/runs/"+run.RunID+"/tracker/link", fmt.Sprintf(`{"issue_key":%q}`, key))
	if response.Code != http.StatusOK {
		t.Fatalf("link %s: %d %s", key, response.Code, response.Body.String())
	}
}

func runTrackerState(t *testing.T, srv *Server, run *store.PipelineRun) map[string]any {
	t.Helper()
	response := projectServe(t, srv, http.MethodGet, "/api/runs/"+run.RunID+"/tracker", "")
	var state map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &state) != nil {
		t.Fatalf("tracker state: %d %s", response.Code, response.Body.String())
	}
	return state
}

func errorCode(response *httptest.ResponseRecorder) string {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body.Code
}

func TestTrackerLinkIsValidatedAndStrictlyOneToOne(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"}, tracker.FakeIssueSeed{Key: "APP-2"})
	srv := newTrackerServer(t, ":memory:", fake)
	a := createJiraTask(t, srv, "link-a", nil)
	b := createJiraTask(t, srv, "link-b", nil)
	put := func(run *store.PipelineRun, key string) *httptest.ResponseRecorder {
		return projectServe(t, srv, http.MethodPut, "/api/runs/"+run.RunID+"/tracker/link", fmt.Sprintf(`{"issue_key":%q}`, key))
	}

	if r := put(a, "app-1"); r.Code != http.StatusBadRequest || errorCode(r) != "invalid_issue_key" {
		t.Fatalf("lowercase key: %d %s", r.Code, r.Body.String())
	}
	if r := put(a, "NOPE-9"); r.Code != http.StatusUnprocessableEntity || errorCode(r) != "issue_not_found" {
		t.Fatalf("missing issue: %d %s", r.Code, r.Body.String())
	}
	fake.FailNext("get", tracker.ErrForbidden, 1)
	if r := put(a, "APP-1"); r.Code != http.StatusUnprocessableEntity || errorCode(r) != "issue_forbidden" {
		t.Fatalf("inaccessible issue: %d %s", r.Code, r.Body.String())
	}
	fake.FailNext("get", errors.New("boom"), 1)
	if r := put(a, "APP-1"); r.Code != http.StatusBadGateway || errorCode(r) != "tracker_unavailable" {
		t.Fatalf("unreachable tracker: %d %s", r.Code, r.Body.String())
	}
	if link, _ := srv.store.GetTrackerLink(a.ID); link != nil {
		t.Fatalf("a rejected link must not be stored: %+v", link)
	}
	if r := put(a, "APP-1"); r.Code != http.StatusOK {
		t.Fatalf("valid link: %d %s", r.Code, r.Body.String())
	}
	if r := put(a, "APP-1"); r.Code != http.StatusOK {
		t.Fatalf("repeating the same link must be idempotent: %d %s", r.Code, r.Body.String())
	}
	if r := put(a, "APP-2"); r.Code != http.StatusConflict || errorCode(r) != "task_already_linked" {
		t.Fatalf("second issue for one task: %d %s", r.Code, r.Body.String())
	}
	if r := put(b, "APP-1"); r.Code != http.StatusConflict || errorCode(r) != "issue_already_linked" {
		t.Fatalf("second task for one issue: %d %s", r.Code, r.Body.String())
	}
	if link, _ := srv.store.GetTrackerLink(a.ID); link == nil || link.IssueKey != "APP-1" {
		t.Fatalf("the original link must survive rejected attempts: %+v", link)
	}
	if r := projectServe(t, srv, http.MethodDelete, "/api/runs/"+a.RunID+"/tracker/link", ""); r.Code != http.StatusOK {
		t.Fatalf("explicit unlink: %d %s", r.Code, r.Body.String())
	}
	if r := put(b, "APP-1"); r.Code != http.StatusOK {
		t.Fatalf("after the explicit correction the issue can move: %d %s", r.Code, r.Body.String())
	}
	state := runTrackerState(t, srv, b)
	if state["sync_status"] != "ok" || state["tracker"] != "fake" || state["link"].(map[string]any)["issue_key"] != "APP-1" {
		t.Fatalf("task tracker state=%v", state)
	}
	if a := runTrackerState(t, srv, a); a["sync_status"] != "not_linked" {
		t.Fatalf("unlinked task state=%v", a)
	}
}

func TestTrackerStageEventsDriveExactlyTheConfiguredTransitionsAndNeverDuplicate(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "flow-run", nil)
	linkTask(t, srv, run, "APP-1")

	trackerEvent(t, srv, run, "attempt_started", "implementation", "att-1", `{}`)
	trackerEvent(t, srv, run, "attempt_started", "design", "att-0", `{}`) // not mapped: no call
	eventually(t, "stage entry transition", func() bool { s, _ := fake.Status("APP-1"); return s == "In Progress" })
	trackerEvent(t, srv, run, "attempt_finished", "implementation", "att-1", `{"status":"passed","outcome":"passed","decision":"approved"}`)
	eventually(t, "stage pass transition and comment", func() bool {
		s, _ := fake.Status("APP-1")
		return s == "In Review" && len(fake.Comments("APP-1")) == 1
	})

	transitions := func() (calls []string) {
		for _, call := range fake.Calls() {
			if call.Op == "transition" {
				calls = append(calls, call.Arg)
			}
		}
		return
	}
	if got := strings.Join(transitions(), ","); got != "In Progress,In Review" {
		t.Fatalf("transition calls=%s, want exactly the configured ones", got)
	}
	comment := fake.Comments("APP-1")[0]
	for _, want := range []string{"https://ai-team.example/pipelines/", "Stage result: implementation", "outcome=passed", "ai-team attempt: att-1"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment missing %q: %s", want, comment)
		}
	}

	// Replay every stored event, as after a crash before the cursor commit:
	// the comment is not duplicated and the final status is unchanged.
	stored, err := srv.store.GetEventsAfter(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		for _, event := range stored {
			if !srv.deliverTrackerEvent(context.Background(), event) {
				t.Fatalf("replay of event %d kept pending", event.ID)
			}
		}
	}
	if len(fake.Comments("APP-1")) != 1 {
		t.Fatalf("replay duplicated the comment: %v", fake.Comments("APP-1"))
	}
	if s, _ := fake.Status("APP-1"); s != "In Review" {
		t.Fatalf("status after replay=%s", s)
	}
	if state := runTrackerState(t, srv, run); state["sync_status"] != "ok" {
		t.Fatalf("state=%v", state)
	}
}

func TestTrackerCommentsFailedSkippedAndOverriddenHonestlyWithoutTransition(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "honest-run", nil)
	linkTask(t, srv, run, "APP-1")

	trackerEvent(t, srv, run, "attempt_finished", "qa", "att-f", `{"status":"failed","outcome":"failed","execution":"succeeded","decision":"approved"}`)
	trackerEvent(t, srv, run, "attempt_finished", "qa", "att-s", `{"status":"skipped","outcome":"skipped"}`)
	trackerEvent(t, srv, run, "attempt_finished", "qa", "att-r", `{"status":"passed","outcome":"passed","decision":"rejected"}`)
	trackerEvent(t, srv, run, "check_overridden", "qa", "att-o", `{"reason":"release deadline","check_status":"failed"}`)
	eventually(t, "four comments", func() bool { return len(fake.Comments("APP-1")) == 4 })

	if s, _ := fake.Status("APP-1"); s != "To Do" {
		t.Fatalf("a failed, skipped, rejected or overridden stage must not move the card, status=%s", s)
	}
	for _, call := range fake.Calls() {
		if call.Op == "transition" {
			t.Fatalf("unexpected transition %+v", call)
		}
	}
	joined := strings.Join(fake.Comments("APP-1"), "\n---\n")
	for _, want := range []string{"outcome=failed", "outcome=skipped", "outcome=rejected", "outcome=overridden", "reason=release deadline"} {
		if !strings.Contains(joined, want) {
			t.Errorf("comments lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "outcome=passed") {
		t.Fatalf("a non-pass was presented as a pass:\n%s", joined)
	}
}

func TestTrackerFailureIsVisibleDurableAndManuallyRetryable(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "failure-run", nil)
	linkTask(t, srv, run, "APP-1")
	run.Status = "completed"
	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	if err := srv.store.UpdatePipelineRun(run); err != nil {
		t.Fatal(err)
	}

	fake.FailNext("transition", errors.New("tracker is down"), -1)
	trackerEvent(t, srv, run, "attempt_finished", "qa", "att-1", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "failed delivery to be recorded", func() bool {
		failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
		return len(failed) == 1
	})

	state := runTrackerState(t, srv, run)
	if state["sync_status"] != "failed" {
		t.Fatalf("failure must be visible to the operator: %v", state)
	}
	deliveries := state["deliveries"].([]any)
	first := deliveries[0].(map[string]any)
	if first["status"] != "failed" || !strings.Contains(first["last_error"].(string), "tracker is down") || first["attempts"].(float64) != 1 {
		t.Fatalf("delivery=%v", first)
	}
	events, err := srv.store.GetRunEventsAfter(run.RunID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, event := range events {
		warned = warned || (event.Type == "integration_error" && event.Severity == "warning" && strings.Contains(event.DataJSON, "tracker is down"))
	}
	if !warned {
		t.Fatalf("durable integration_error event is missing: %+v", events)
	}
	if unchanged, _ := srv.store.GetPipelineRunByRunID(run.RunID); unchanged.Status != "completed" {
		t.Fatalf("tracker failure changed the local run to %q", unchanged.Status)
	}
	overview := projectServe(t, srv, http.MethodGet, "/api/tracker", "")
	if !strings.Contains(overview.Body.String(), `"failed_deliveries":1`) {
		t.Fatalf("overview must count failed deliveries: %s", overview.Body.String())
	}
	if list := projectServe(t, srv, http.MethodGet, "/api/tracker/deliveries?status=failed", ""); !strings.Contains(list.Body.String(), "tracker is down") {
		t.Fatalf("failed deliveries list: %s", list.Body.String())
	}

	// The tracker is still down: the retry fails again, stays visible and counts the attempt.
	eventID := int64(first["event_id"].(float64))
	retryPath := fmt.Sprintf("/api/runs/%s/tracker/deliveries/%d/retry", run.RunID, eventID)
	if r := projectServe(t, srv, http.MethodPost, retryPath, ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"failed"`) || !strings.Contains(r.Body.String(), `"attempts":2`) {
		t.Fatalf("retry while down: %d %s", r.Code, r.Body.String())
	}

	fake.ClearFailures()
	if r := projectServe(t, srv, http.MethodPost, "/api/runs/"+run.RunID+"/tracker/retry", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"delivered"`) {
		t.Fatalf("retry after recovery: %d %s", r.Code, r.Body.String())
	}
	if s, _ := fake.Status("APP-1"); s != "Done" || len(fake.Comments("APP-1")) != 1 {
		t.Fatalf("after retry status=%s comments=%d", s, len(fake.Comments("APP-1")))
	}
	if state := runTrackerState(t, srv, run); state["sync_status"] != "ok" {
		t.Fatalf("state after retry=%v", state)
	}
	if r := projectServe(t, srv, http.MethodPost, retryPath, ""); r.Code != http.StatusConflict || errorCode(r) != "delivery_not_failed" {
		t.Fatalf("retrying a delivered event must be refused: %d %s", r.Code, r.Body.String())
	}
	other := createJiraTask(t, srv, "other-run", nil)
	if r := projectServe(t, srv, http.MethodPost, fmt.Sprintf("/api/runs/%s/tracker/deliveries/%d/retry", other.RunID, eventID), ""); r.Code != http.StatusNotFound {
		t.Fatalf("a delivery of another task must not be retryable here: %d %s", r.Code, r.Body.String())
	}
}

func TestTrackerPartialFailureKeepsCommentIdempotentOnRetry(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "partial-run", nil)
	linkTask(t, srv, run, "APP-1")
	fake.FailNext("transition", errors.New("workflow denied"), 1)
	trackerEvent(t, srv, run, "attempt_finished", "qa", "att-1", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "partial failure", func() bool {
		failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
		return len(failed) == 1 && len(fake.Comments("APP-1")) == 1
	})
	if r := projectServe(t, srv, http.MethodPost, "/api/runs/"+run.RunID+"/tracker/retry", ""); r.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", r.Code, r.Body.String())
	}
	if s, _ := fake.Status("APP-1"); s != "Done" || len(fake.Comments("APP-1")) != 1 {
		t.Fatalf("status=%s comments=%d, the comment must not repeat", s, len(fake.Comments("APP-1")))
	}
}

func TestTrackerUnlinkedAndOtherTrackerLinksProduceNoRequests(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	unlinked := createJiraTask(t, srv, "unlinked-run", []string{"APP-1"}) // key only in free-form links: not a link
	trackerEvent(t, srv, unlinked, "attempt_finished", "qa", "att-1", `{"status":"passed","outcome":"passed"}`)
	time.Sleep(600 * time.Millisecond)
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("a task without an explicit link must not reach the tracker: %+v", calls)
	}
}

func TestLegacyExternalLinksMigrateSafelyAndAmbiguityBecomesConflict(t *testing.T) {
	database := filepath.Join(t.TempDir(), "web.db")
	plain, err := NewServer(database, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one := createJiraTask(t, plain, "legacy-1", []string{"APP-1", "https://example.test/not-a-key"})
	two := createJiraTask(t, plain, "legacy-2", []string{" APP-2 "})
	several := createJiraTask(t, plain, "legacy-3", []string{"APP-3", "APP-4"})
	shareA := createJiraTask(t, plain, "legacy-4", []string{"APP-5"})
	shareB := createJiraTask(t, plain, "legacy-5", []string{"APP-5"})
	none := createJiraTask(t, plain, "legacy-6", []string{"encoding UTF-8", "see APP-9 in text"})
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}

	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-5"})
	srv := newTrackerServer(t, database, fake)
	linked := func(run *store.PipelineRun) string {
		link, err := srv.store.GetTrackerLink(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if link == nil {
			return ""
		}
		return link.IssueKey
	}
	if linked(one) != "APP-1" || linked(two) != "APP-2" {
		t.Fatalf("unambiguous links must migrate: %q %q", linked(one), linked(two))
	}
	for _, run := range []*store.PipelineRun{several, shareA, shareB, none} {
		if key := linked(run); key != "" {
			t.Fatalf("run %s must not be linked by guessing, got %s", run.RunID, key)
		}
	}
	conflicts, err := srv.store.ListTrackerLinkConflicts(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range conflicts {
		got[fmt.Sprintf("%d/%s", c.TaskID, c.IssueKey)] = c.Reason
	}
	for _, want := range []string{
		fmt.Sprintf("%d/APP-3", several.ID), fmt.Sprintf("%d/APP-4", several.ID),
		fmt.Sprintf("%d/APP-5", shareA.ID), fmt.Sprintf("%d/APP-5", shareB.ID),
	} {
		if got[want] == "" {
			t.Errorf("conflict %s is missing: %v", want, got)
		}
	}
	if len(conflicts) != 4 {
		t.Fatalf("conflicts=%+v", conflicts)
	}
	if state := runTrackerState(t, srv, shareA); state["sync_status"] != "conflict" || len(state["conflicts"].([]any)) != 1 {
		t.Fatalf("conflict must be visible on the task: %v", state)
	}
	if api := projectServe(t, srv, http.MethodGet, "/api/tracker/conflicts", ""); !strings.Contains(api.Body.String(), "referenced by 2 tasks") {
		t.Fatalf("conflicts API: %s", api.Body.String())
	}

	// Explicit correction: one task takes the card, the other is refused until it is unlinked.
	linkTask(t, srv, shareA, "APP-5")
	if r := projectServe(t, srv, http.MethodPut, "/api/runs/"+shareB.RunID+"/tracker/link", `{"issue_key":"APP-5"}`); r.Code != http.StatusConflict {
		t.Fatalf("second task must not take the card: %d %s", r.Code, r.Body.String())
	}
	if r := projectServe(t, srv, http.MethodDelete, "/api/runs/"+shareB.RunID+"/tracker/link", ""); r.Code != http.StatusOK {
		t.Fatalf("dismissing the conflict: %d", r.Code)
	}
	remaining, _ := srv.store.ListTrackerLinkConflicts(0)
	if len(remaining) != 2 {
		t.Fatalf("only the several-keys conflicts must remain: %+v", remaining)
	}

	// A restart must not resurrect corrected data: the import runs once.
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	again := newTrackerServer(t, database, fake)
	if after, _ := again.store.ListTrackerLinkConflicts(0); len(after) != 2 {
		t.Fatalf("restart re-imported conflicts: %+v", after)
	}
	if link, _ := again.store.GetTrackerLink(shareB.ID); link != nil {
		t.Fatalf("restart re-linked a corrected task: %+v", link)
	}
}

func TestTrackerAPIWithoutConfiguredTracker(t *testing.T) {
	srv, _ := newProjectTestServer(t)
	run := createJiraTask(t, srv, "no-tracker", nil)
	if r := projectServe(t, srv, http.MethodGet, "/api/tracker", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"configured":false`) {
		t.Fatalf("overview: %d %s", r.Code, r.Body.String())
	}
	if r := projectServe(t, srv, http.MethodPut, "/api/runs/"+run.RunID+"/tracker/link", `{"issue_key":"APP-1"}`); r.Code != http.StatusConflict || errorCode(r) != "tracker_not_configured" {
		t.Fatalf("link without tracker: %d %s", r.Code, r.Body.String())
	}
	if r := projectServe(t, srv, http.MethodGet, "/api/runs/unknown/tracker", ""); r.Code != http.StatusNotFound {
		t.Fatalf("unknown run: %d", r.Code)
	}
}

func TestTrackerLinkAndRetryUseRBAC(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("t", 32)))
	if err != nil {
		t.Fatal(err)
	}
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	target := t.TempDir()
	srv, err := NewServer(":memory:", "", target, WithTargetDir(target), WithAuthenticator(manager), WithRunController(&fakeRunController{}),
		WithTracker(fake, trackerTestMaps, "https://ai-team.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := createJiraTask(t, srv, "rbac-run", nil)
	ownerCookie, ownerCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPost, "/api/team/invitations", `{"email":"dev@example.com","roles":["developer"]}`)
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if invite.Code != http.StatusCreated || json.Unmarshal(invite.Body.Bytes(), &invitation) != nil {
		t.Fatalf("invite: %d %s", invite.Code, invite.Body.String())
	}
	activate := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activate.Header.Set("Content-Type", "application/json")
	activated := httptest.NewRecorder()
	srv.router.ServeHTTP(activated, activate)
	devCookie, devCSRF := cloudSessionForTest(t, srv, manager, "dev@example.com", cloudidentity.RoleProductOwner)

	path := "/api/runs/" + run.RunID + "/tracker/link"
	if r := teamRequest(srv, devCookie, devCSRF, http.MethodPut, path, `{"issue_key":"APP-1"}`); r.Code != http.StatusForbidden {
		t.Fatalf("developer must not link: %d %s", r.Code, r.Body.String())
	}
	if r := teamRequest(srv, devCookie, devCSRF, http.MethodPost, "/api/runs/"+run.RunID+"/tracker/retry", ""); r.Code != http.StatusForbidden {
		t.Fatalf("developer must not retry: %d %s", r.Code, r.Body.String())
	}
	if r := teamRequest(srv, devCookie, devCSRF, http.MethodGet, "/api/runs/"+run.RunID+"/tracker", ""); r.Code != http.StatusOK {
		t.Fatalf("developer may read: %d", r.Code)
	}
	if r := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPut, path, `{"issue_key":"APP-1"}`); r.Code != http.StatusOK {
		t.Fatalf("owner link: %d %s", r.Code, r.Body.String())
	}
	if link, _ := srv.store.GetTrackerLink(run.ID); link == nil || link.LinkedBy != "owner@example.com" {
		t.Fatalf("link must record the actor: %+v", link)
	}
}

func TestTrackerRetryDoesNotSendStaleTransitionOverNewerDelivery(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "stale-run", nil)
	linkTask(t, srv, run, "APP-1")

	fake.FailNext("transition", errors.New("tracker is down"), -1)
	trackerEvent(t, srv, run, "attempt_started", "implementation", "att-1", `{}`)
	eventually(t, "failed on_enter delivery", func() bool {
		failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
		return len(failed) == 1
	})
	fake.ClearFailures()
	trackerEvent(t, srv, run, "attempt_finished", "implementation", "att-1", `{"status":"passed","outcome":"passed"}`)
	eventually(t, "newer delivery", func() bool {
		delivered, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryDelivered, 10)
		return len(delivered) == 1
	})
	if s, _ := fake.Status("APP-1"); s != "In Review" {
		t.Fatalf("status=%s, want In Review", s)
	}
	failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
	retry := fmt.Sprintf("/api/runs/%s/tracker/deliveries/%d/retry", run.RunID, failed[0].EventID)
	if r := projectServe(t, srv, http.MethodPost, retry, ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"superseded"`) {
		t.Fatalf("retry: %d %s", r.Code, r.Body.String())
	}
	if s, _ := fake.Status("APP-1"); s != "In Review" {
		t.Fatalf("the stale on_enter moved the card back to %s", s)
	}
	if state := runTrackerState(t, srv, run); state["sync_status"] != "ok" {
		t.Fatalf("superseded must not count as an error: %v", state)
	}
}

func TestTrackerUnlinkAbandonsFailedDeliveriesInsteadOfSendingThemToTheNewCard(t *testing.T) {
	fake := tracker.NewFake(nil, tracker.FakeIssueSeed{Key: "APP-1"}, tracker.FakeIssueSeed{Key: "APP-2"})
	srv := newTrackerServer(t, ":memory:", fake)
	run := createJiraTask(t, srv, "relink-run", nil)
	linkTask(t, srv, run, "APP-1")
	fake.FailNext("transition", errors.New("tracker is down"), -1)
	trackerEvent(t, srv, run, "attempt_started", "implementation", "att-1", `{}`)
	eventually(t, "failed delivery", func() bool {
		failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10)
		return len(failed) == 1
	})
	fake.ClearFailures()
	if r := projectServe(t, srv, http.MethodDelete, "/api/runs/"+run.RunID+"/tracker/link", ""); r.Code != http.StatusOK && r.Code != http.StatusNoContent {
		t.Fatalf("unlink: %d %s", r.Code, r.Body.String())
	}
	linkTask(t, srv, run, "APP-2")
	if failed, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 10); len(failed) != 0 {
		t.Fatalf("failed deliveries of the removed link must not stay failed: %+v", failed)
	}
	abandoned, _ := srv.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryAbandoned, 10)
	if len(abandoned) != 1 {
		t.Fatalf("abandoned=%+v", abandoned)
	}
	if state := runTrackerState(t, srv, run); state["sync_status"] != "ok" {
		t.Fatalf("abandoned must not count as an error: %v", state)
	}
	retry := fmt.Sprintf("/api/runs/%s/tracker/deliveries/%d/retry", run.RunID, abandoned[0].EventID)
	if r := projectServe(t, srv, http.MethodPost, retry, ""); r.Code != http.StatusConflict {
		t.Fatalf("an abandoned delivery must not be retried: %d %s", r.Code, r.Body.String())
	}
	if s, _ := fake.Status("APP-2"); s != "To Do" {
		t.Fatalf("the old event reached the new card: %s", s)
	}
}

func TestNextIntegrationPauseDoublesUpToAMinute(t *testing.T) {
	pause := time.Duration(0)
	var seen []time.Duration
	for range 10 {
		pause, _ = nextIntegrationPause(pause)
		seen = append(seen, pause)
	}
	if seen[0] != 500*time.Millisecond || seen[1] != time.Second || seen[len(seen)-1] != time.Minute {
		t.Fatalf("pauses=%v", seen)
	}
}
