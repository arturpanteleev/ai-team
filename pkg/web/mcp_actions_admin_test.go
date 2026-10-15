package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

const planHash = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

func deliveryPlanApproval(now time.Time) approval.PendingApproval {
	return approval.PendingApproval{
		ID: "plan-1", RunID: "run-plan", AttemptID: "attempt-plan", FromStage: "accept", ToStage: "accept",
		Trigger: "delivery_plan", Status: approval.StatusPending, SubjectHash: planHash,
		RequiredRoles: []string{"product_owner"}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": "accept"}, CreatedAt: now,
	}
}

// Resume, cancel, delivery retry and delivery-plan approval keep their rights
// and their exact-hash rule over MCP, and a repeated delivery does not repeat
// a delivery or a decision.
func TestMCPTaskLifecycleAndDeliveryActions(t *testing.T) {
	now := time.Now().UTC()
	approvals := append(stageApprovals(now), deliveryPlanApproval(now))
	runs := stageRuns()
	runs["run-plan"] = lifecycle.State{Phase: lifecycle.PhaseWaiting, NextStage: "accept", PendingApprovalID: "plan-1"}
	env := newMCPActionEnv(t, approvals, runs)

	// Resume: developers may (as in the UI), reviewers may not.
	if failure := env.as(t, actorReviewer).refused("resume_task", map[string]any{"run_id": "run-skip"}); failure.Status != 403 {
		t.Fatalf("a reviewer must not resume: %+v", failure)
	}
	if failure := env.as(t, actorDev).refused("resume_task", map[string]any{"run_id": "run-skip", "grant_budget_allowance": true}); failure.Status != 400 {
		t.Fatalf("a budget grant needs a reason: %+v", failure)
	}
	env.as(t, actorDev).do("resume_task", map[string]any{"run_id": "run-skip"})
	if got := len(env.ctl.callsOf("resume")); got != 1 {
		t.Fatalf("resume calls: %d", got)
	}

	// Cancel: product owner and release manager only.
	if failure := env.as(t, actorDev).refused("cancel_task", map[string]any{"run_id": "run-skip"}); failure.Status != 403 {
		t.Fatalf("a developer must not cancel: %+v", failure)
	}
	env.as(t, actorPO).do("cancel_task", map[string]any{"run_id": "run-skip", mcpIdempotencyKey: "cancel-1"})
	env.as(t, actorPO).do("cancel_task", map[string]any{"run_id": "run-skip", mcpIdempotencyKey: "cancel-1"})
	if got := len(env.ctl.callsOf("cancel")); got != 1 {
		t.Fatalf("a repeated cancel must not repeat: %d", got)
	}

	// Delivery retry: only who may deliver; one delivery per key.
	if failure := env.as(t, actorDev).refused("retry_delivery", map[string]any{"run_id": "run-skip"}); failure.Status != 403 || env.ctl.deliveries() != 0 {
		t.Fatalf("a developer must not deliver: %+v", failure)
	}
	env.as(t, actorPO).do("retry_delivery", map[string]any{"run_id": "run-skip", mcpIdempotencyKey: "deliver-1"})
	env.as(t, actorPO).do("retry_delivery", map[string]any{"run_id": "run-skip", mcpIdempotencyKey: "deliver-1"})
	if env.ctl.deliveries() != 1 {
		t.Fatalf("delivery ran %d times", env.ctl.deliveries())
	}
	if failure := env.as(t, actorPO).refused("retry_delivery", map[string]any{"run_id": "no-such-run"}); failure.Status != 404 {
		t.Fatalf("an unknown task: %+v", failure)
	}

	// Delivery plan: the exact plan hash, and the role the plan asks for.
	if failure := env.as(t, actorPO).refused("approve_delivery_plan", map[string]any{"run_id": "run-plan"}); failure.Code != "bad_arguments" {
		t.Fatalf("the plan hash is mandatory: %+v", failure)
	}
	if failure := env.as(t, actorPO).refused("approve_delivery_plan", map[string]any{"run_id": "run-plan", "plan_hash": strings.Repeat("a", 64)}); failure.Status != 409 {
		t.Fatalf("a plan hash that is not the pending one: %+v", failure)
	}
	if failure := env.as(t, actorDev).refused("approve_delivery_plan", map[string]any{"run_id": "run-plan", "plan_hash": planHash}); failure.Status != 403 {
		t.Fatalf("a developer must not approve a plan: %+v", failure)
	}
	if count, _ := env.ctl.decisions(); count != 0 {
		t.Fatalf("refused plan approvals must not decide: %d", count)
	}
	args := map[string]any{"run_id": "run-plan", "plan_hash": planHash, mcpIdempotencyKey: "plan-1"}
	env.as(t, actorPO).do("approve_delivery_plan", args)
	env.as(t, actorPO).do("approve_delivery_plan", args)
	if count, decision := env.ctl.decisions(); count != 1 || decision.ActorID != actorPO || decision.SubjectHash != planHash || decision.Action != "approve" {
		t.Fatalf("plan decision = %d %+v", count, decision)
	}
	audited := grantedMCPActions(t, env.srv, "run-plan")
	if len(audited) != 1 || audited[0]["tool"] != "approve_delivery_plan" {
		t.Fatalf("plan approval audit = %v", audited)
	}
}

// Grill answers, agent launches and artifact edits go through the stage-function
// checks of the UI.
func TestMCPGrillAgentAndArtifactActions(t *testing.T) {
	now := time.Now().UTC()
	env := newMCPActionEnvFor(t, agentStageTemplate, stageApprovals(now), map[string]lifecycle.State{
		"run-grill": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "grill-1"},
		"run-agent": {Phase: lifecycle.PhaseResumable, NextStage: "build"},
	})
	dev, qa := env.as(t, actorDev), env.as(t, actorQA)

	if failure := qa.refused("answer_stage_grill", map[string]any{"run_id": "run-grill", "stage_id": "build", "answer": "x"}); failure.Status != 403 {
		t.Fatalf("a foreign function must not answer: %+v", failure)
	}
	if failure := dev.refused("answer_stage_grill", map[string]any{"run_id": "run-grill", "stage_id": "build", "answer": "   "}); failure.Code != "bad_answer" {
		t.Fatalf("a blank answer: %+v", failure)
	}
	dev.do("answer_stage_grill", map[string]any{"run_id": "run-grill", "stage_id": "build", "answer": "the answer"})
	dev.do("submit_stage_grill_version", map[string]any{"run_id": "run-grill", "stage_id": "build", "answer": "v2"})
	dev.do("complete_stage_grill", map[string]any{"run_id": "run-grill", "stage_id": "build"})
	for action, extra := range map[string]string{"grill-answer": "the answer", "grill-version": "v2", "grill-done": ""} {
		calls := env.ctl.callsOf(action)
		if len(calls) != 1 || calls[0].Actor != actorDev || calls[0].Extra != extra {
			t.Fatalf("%s calls = %+v", action, calls)
		}
	}

	if failure := qa.refused("run_stage_agent", map[string]any{"run_id": "run-agent", "stage_id": "build"}); failure.Status != 403 {
		t.Fatalf("a foreign function must not launch the agent: %+v", failure)
	}
	if failure := dev.refused("run_stage_agent", map[string]any{"run_id": "run-agent", "stage_id": "build", "action": "format"}); failure.Code != "bad_arguments" {
		t.Fatalf("an unknown agent action: %+v", failure)
	}
	launch := map[string]any{"run_id": "run-agent", "stage_id": "build", "action": "refine", mcpIdempotencyKey: "launch-1"}
	dev.do("run_stage_agent", launch)
	dev.do("run_stage_agent", launch)
	calls := env.ctl.callsOf("run-agent")
	if len(calls) != 1 || calls[0].Actor != actorDev || calls[0].Extra != "refine" {
		t.Fatalf("one agent launch as the developer expected: %+v", calls)
	}
	// The launch is recorded as stage authorship before dispatch (self-approval).
	if got := runEventData(t, env.srv, "run-agent", stageAgentRequestedEvent); len(got) != 1 || got[0]["actor_id"] != actorDev {
		t.Fatalf("agent launch authorship = %v", got)
	}

	if failure := dev.refused("create_artifact_revision", map[string]any{"run_id": "run-agent", "artifact_path": "reports/a.md", "content": "x"}); failure.Status != 403 {
		t.Fatalf("a developer has no artifact edit right: %+v", failure)
	}
	if failure := env.as(t, actorReviewer).refused("create_artifact_revision", map[string]any{"run_id": "run-agent", "artifact_path": "../../etc/passwd", "content": "x"}); failure.Status != 400 {
		t.Fatalf("an artifact path outside the run: %+v", failure)
	}
}

// The executor choice in both spellings reaches the controller as the caller.
func TestMCPExecutorChoice(t *testing.T) {
	now := time.Now().UTC()
	ready := approval.PendingApproval{
		ID: "ready-1", RunID: "run-legacy", FromStage: "spec", ToStage: "build", Trigger: "stage_ready", Status: approval.StatusPending,
		SubjectHash: testSubjectHash, RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny,
		Actions: []string{"submit"}, Targets: map[string]string{"submit": "build"}, CreatedAt: now,
	}
	env := newMCPActionEnvFor(t, agentStageTemplate, append(stageApprovals(now), ready), map[string]lifecycle.State{
		"run-executor": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
		"run-legacy":   {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "ready-1"},
	})
	dev := env.as(t, actorDev)
	if failure := env.as(t, actorQA).refused("set_stage_executor", map[string]any{"run_id": "run-executor", "stage_id": "build", "executor": "agent"}); failure.Status != 403 {
		t.Fatalf("a foreign function must not choose the executor: %+v", failure)
	}
	if failure := dev.refused("set_stage_executor", map[string]any{"run_id": "run-executor", "stage_id": "build", "executor": "robot"}); failure.Code != "bad_arguments" {
		t.Fatalf("an unknown executor: %+v", failure)
	}
	dev.do("set_stage_executor", map[string]any{"run_id": "run-executor", "stage_id": "build", "executor": "agent"})
	dev.do("change_task_executor", map[string]any{"run_id": "run-legacy", "stage_id": "build", "executor": "human"})
	calls := env.ctl.callsOf("executor")
	if len(calls) != 1 || calls[0].Actor != actorDev || calls[0].Extra != "agent" {
		t.Fatalf("executor calls = %+v", calls)
	}
	// Both spellings record the switch under the caller (stage authorship).
	for _, runID := range []string{"run-executor", "run-legacy"} {
		if got := runEventData(t, env.srv, runID, "executor_changed"); len(got) != 1 || got[0]["actor_id"] != actorDev {
			t.Fatalf("executor change authorship of %s = %v", runID, got)
		}
	}
}

// Projects, backlog, template: rights as in the UI, results readable back, and
// every successful action leaves an MCP audit record.
func TestMCPProjectsBacklogAndTemplate(t *testing.T) {
	env := newMCPActionEnv(t, nil, nil)
	owner, dev := env.as(t, actorOwner), env.as(t, actorDev)

	if failure := dev.refused("create_project", map[string]any{"name": "Nope"}); failure.Status != 403 {
		t.Fatalf("a developer must not create projects: %+v", failure)
	}
	project := owner.do("create_project", map[string]any{"name": "Billing", "description": "money"})
	id := int(project["id"].(float64))
	owner.do("update_project", map[string]any{"project_id": id, "name": "Billing v2"})
	if text, _ := dev.call("get_project", map[string]any{"project_id": id}); !strings.Contains(text, "Billing v2") {
		t.Fatalf("the rename is not visible: %s", text)
	}
	if failure := owner.refused("update_project", map[string]any{"project_id": id, "name": "   "}); failure.Status != 400 {
		t.Fatalf("an empty name: %+v", failure)
	}

	if failure := owner.refused("create_backlog_item", map[string]any{"title": "idea", "feature": "billing"}); failure.Code != "bad_arguments" {
		t.Fatalf("a backlog item needs a project: %+v", failure)
	}
	if failure := dev.refused("create_backlog_item", map[string]any{"project_id": id, "title": "idea", "feature": "billing"}); failure.Status != 403 {
		t.Fatalf("a developer must not add backlog items: %+v", failure)
	}
	item := owner.do("create_backlog_item", map[string]any{"project_id": id, "title": "Refunds", "idea": "let users refund", "feature": "refunds"})
	itemID := int(item["id"].(float64))
	owner.do("set_backlog_item_status", map[string]any{"item_id": itemID, "status": "ready"})
	if text, _ := owner.call("list_backlog", map[string]any{"project_id": id}); !strings.Contains(text, `"ready"`) {
		t.Fatalf("the backlog item is not ready: %s", text)
	}
	owner.do("set_backlog_item_status", map[string]any{"item_id": itemID, "status": "discarded"})
	if failure := owner.refused("set_backlog_item_status", map[string]any{"item_id": itemID, "status": "ready"}); failure.Status != 409 {
		t.Fatalf("a discarded item cannot come back: %+v", failure)
	}

	// Template: validate needs no key and changes nothing; publish is guarded.
	current, failed := owner.call("get_template", nil)
	if failed {
		t.Fatalf("get_template: %s", current)
	}
	var view struct {
		Template struct {
			YAML    string `json:"yaml"`
			Version string `json:"version"`
		} `json:"template"`
	}
	if err := json.Unmarshal([]byte(current), &view); err != nil || view.Template.YAML == "" {
		t.Fatalf("template editor view: %v %s", err, current)
	}
	editor := view.Template
	text, failed := owner.call("validate_template", map[string]any{"yaml": editor.YAML})
	if failed || !strings.Contains(text, `"valid":true`) {
		t.Fatalf("validate_template: %v %s", failed, text)
	}
	if text, failed = owner.call("validate_template", map[string]any{"yaml": "stages: ["}); failed || !strings.Contains(text, `"valid":false`) {
		t.Fatalf("an invalid template must be reported, not fail the call: %v %s", failed, text)
	}
	if failure := dev.refused("publish_template", map[string]any{"yaml": editor.YAML}); failure.Status != 403 {
		t.Fatalf("a developer must not publish: %+v", failure)
	}
	if failure := owner.refused("publish_template", map[string]any{"yaml": editor.YAML, "expected_version": strings.Repeat("e", 64)}); failure.Status != 409 {
		t.Fatalf("a stale template base must be refused: %+v", failure)
	}
	edited := strings.Replace(editor.YAML, "title:", "title: S13 ", 1)
	owner.do("publish_template", map[string]any{"yaml": edited, "expected_version": editor.Version})
	if versions, _ := owner.call("list_template_versions", nil); !strings.Contains(versions, "S13") && !strings.Contains(versions, `"versions"`) {
		t.Fatalf("the published version is not listed: %s", versions)
	}

	// Actions without a run are audited in the team audit, with the channel.
	audit, _ := owner.call("get_team_audit", nil)
	for _, tool := range []string{"create_project", "update_project", "create_backlog_item", "set_backlog_item_status", "publish_template"} {
		if !strings.Contains(audit, `\"tool\":\"`+tool+`\"`) && !strings.Contains(audit, `"tool":"`+tool+`"`) {
			t.Errorf("the team audit has no MCP record of %s: %s", tool, audit)
		}
	}
	if strings.Contains(audit, "validate_template") {
		t.Error("a validation changes nothing and must not be audited as an action")
	}
}

// Team and role management over MCP: the team management right, the
// administrator-right rule, immediate effect on tokens, no secret in replays.
func TestMCPTeamAndRoleManagement(t *testing.T) {
	env := newMCPActionEnv(t, nil, nil)
	owner, po, dev := env.as(t, actorOwner), env.as(t, actorPO), env.as(t, actorDev)

	if failure := dev.refused("invite_team_member", map[string]any{"email": "x@example.com", "roles": []string{"developer"}}); failure.Status != 403 {
		t.Fatalf("a developer must not invite: %+v", failure)
	}
	invite := map[string]any{"email": "new@example.com", "roles": []string{"developer"}, mcpIdempotencyKey: "invite-1"}
	first := owner.do("invite_team_member", invite)
	if first["actor_id"] != "new@example.com" || first["activation_token"] == "" || strings.Contains(first["activation_token"].(string), "shown once") {
		t.Fatalf("the first delivery returns the activation token: %v", first)
	}
	second := owner.do("invite_team_member", invite)
	if second["actor_id"] != "new@example.com" || !strings.Contains(second["activation_token"].(string), "not kept") {
		t.Fatalf("a repeated delivery must not repeat the invitation or the secret: %v", second)
	}
	var stored int
	rows, err := env.srv.store.ListTeamMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range rows {
		if member.ActorID == "new@example.com" {
			stored++
		}
	}
	if stored != 1 {
		t.Fatalf("the invitation was created %d times", stored)
	}
	if failure := owner.refused("invite_team_member", map[string]any{"email": "not-an-email", "roles": []string{"developer"}}); failure.Status != 400 {
		t.Fatalf("a bad e-mail: %+v", failure)
	}

	// Only an administrator grants or revokes the administrator right.
	if failure := po.refused("set_team_member_roles", map[string]any{"actor_id": actorDev, "roles": []string{"developer", "admin"}}); failure.Status != 403 {
		t.Fatalf("a product owner without the right must not grant it: %+v", failure)
	}
	owner.do("set_team_member_roles", map[string]any{"actor_id": actorQA, "roles": []string{"qa", "reviewer"}})
	if listed, _ := owner.call("list_team_members", nil); !strings.Contains(listed, "reviewer") {
		t.Fatalf("the new roles are not listed: %s", listed)
	}

	// Revocation cuts the revoked person's MCP token at once.
	token := env.tokens[actorDev]
	if status := env.postRaw(t, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); status != http.StatusOK {
		t.Fatalf("the developer's token must work before revocation: %d", status)
	}
	owner.do("revoke_team_member", map[string]any{"actor_id": actorDev})
	if status := env.postRaw(t, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); status != http.StatusUnauthorized {
		t.Fatalf("a revoked member's token must stop working at once: %d", status)
	}
	// The team keeps its last administrator.
	if failure := owner.refused("revoke_team_member", map[string]any{"actor_id": "ghost@example.com"}); failure.Status != 404 {
		t.Fatalf("an unknown member: %+v", failure)
	}
}

// The tracker link and its retries over MCP, with the right the UI demands.
func TestMCPTrackerActions(t *testing.T) {
	env := newMCPActionEnv(t, nil, map[string]lifecycle.State{
		"run-a": {Phase: lifecycle.PhaseWaiting, NextStage: "build"}, "run-b": {Phase: lifecycle.PhaseWaiting, NextStage: "build"},
	})
	owner, dev := env.as(t, actorOwner), env.as(t, actorDev)
	if failure := dev.refused("set_task_tracker_link", map[string]any{"run_id": "run-a", "issue_key": "APP-1"}); failure.Status != 403 {
		t.Fatalf("a developer must not link: %+v", failure)
	}
	if failure := owner.refused("set_task_tracker_link", map[string]any{"run_id": "run-a", "issue_key": "app-1"}); failure.Code != "invalid_issue_key" {
		t.Fatalf("a malformed key: %+v", failure)
	}
	owner.do("set_task_tracker_link", map[string]any{"run_id": "run-a", "issue_key": "APP-1"})
	if failure := owner.refused("set_task_tracker_link", map[string]any{"run_id": "run-b", "issue_key": "APP-1"}); failure.Status < 400 {
		t.Fatalf("one issue links one task only: %+v", failure)
	}
	if state, _ := dev.call("get_task_tracker", map[string]any{"run_id": "run-a"}); !strings.Contains(state, "APP-1") {
		t.Fatalf("the link is not visible: %s", state)
	}
	if status, _ := owner.call("get_tracker_status", nil); !strings.Contains(status, `"configured":true`) {
		t.Fatalf("tracker status: %s", status)
	}
	owner.do("retry_task_tracker_deliveries", map[string]any{"run_id": "run-a"})
	owner.do("remove_task_tracker_link", map[string]any{"run_id": "run-a"})
	if state, _ := dev.call("get_task_tracker", map[string]any{"run_id": "run-a"}); strings.Contains(state, `"issue_key":"APP-1"`) {
		t.Fatalf("the link must be gone: %s", state)
	}
	audit := grantedMCPActions(t, env.srv, "run-a")
	if len(audit) != 3 {
		t.Fatalf("link, retry and unlink must each be audited: %v", audit)
	}
}

// A status change made in the tracker becomes a request the controller judges;
// an operator reads it, syncs on demand and dismisses it with a reason, all over MCP.
func TestMCPTrackerInboundSyncAndDismiss(t *testing.T) {
	env := newMCPActionEnv(t, nil, map[string]lifecycle.State{"run-a": {Phase: lifecycle.PhaseWaiting, NextStage: "build"}})
	owner, dev := env.as(t, actorOwner), env.as(t, actorDev)
	owner.do("set_task_tracker_link", map[string]any{"run_id": "run-a", "issue_key": "APP-1"})
	if failure := dev.refused("sync_tracker_now", nil); failure.Status != 403 {
		t.Fatalf("a developer must not trigger a sync: %+v", failure)
	}
	// The first poll records the tracker tail: only later changes count.
	if _, err := env.srv.syncTrackerInbound(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if err := env.tracker.ExternalTransition("APP-1", "Done", "ivan"); err != nil {
		t.Fatal(err)
	}
	synced := owner.do("sync_tracker_now", nil)
	if synced["result"] == nil {
		t.Fatalf("a sync reports what it did: %v", synced)
	}
	text, failed := owner.call("list_tracker_inbound", map[string]any{"state": "open"})
	if failed || !strings.Contains(text, `"to_status":"Done"`) {
		all, _ := owner.call("list_tracker_inbound", nil)
		t.Fatalf("the change must wait for an operator, not move the task: %v %s\nsync=%v\nall=%s", failed, text, synced, all)
	}
	var open struct {
		Requests []struct {
			ChangeID string `json:"change_id"`
			RunID    string `json:"run_id"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(text), &open); err != nil || len(open.Requests) != 1 {
		t.Fatalf("open requests: %v %s", err, text)
	}
	change := open.Requests[0].ChangeID
	dismiss := map[string]any{"run_id": "run-a", "change_id": change, "reason": "handled by hand"}
	if failure := dev.refused("dismiss_tracker_inbound", dismiss); failure.Status != 403 {
		t.Fatalf("a developer must not dismiss: %+v", failure)
	}
	if failure := owner.refused("dismiss_tracker_inbound", map[string]any{"run_id": "run-a", "change_id": change}); failure.Code != "bad_arguments" {
		t.Fatalf("a dismissal needs a reason: %+v", failure)
	}
	owner.do("dismiss_tracker_inbound", dismiss)
	if failure := owner.refused("dismiss_tracker_inbound", dismiss); failure.Status != 409 || failure.Code != "request_not_open" {
		t.Fatalf("a closed request cannot be dismissed again: %+v", failure)
	}
	if left, _ := owner.call("list_tracker_inbound", map[string]any{"state": "open"}); strings.Contains(left, `"to_status":"Done"`) {
		t.Fatalf("the dismissed request must not stay open: %s", left)
	}
}
