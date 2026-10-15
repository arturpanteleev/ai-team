package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/humandecision"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// mcp_actions_test.go — every product action over MCP (S13). A client of the
// official SDK talks to a real HTTP listener; the server runs the same router,
// handlers and controller as for the web UI. The tests prove the actor is the
// token owner, the stage function and administrator rights are checked, hashes
// and reasons are mandatory where they are for the UI, and a repeated delivery
// does not repeat the action.

// stageApprovals are the pending approvals the seeded runs wait on.
func stageApprovals(now time.Time) []approval.PendingApproval {
	return []approval.PendingApproval{
		buildInputApproval("input-take", approval.StatusPending, now),
		{
			Kind: approval.KindCheckDecision, ID: "check-1", RunID: "run", FromStage: "build", ToStage: "build",
			Trigger: approval.StageCheckTrigger, Status: approval.StatusPending, SubjectHash: testSubjectHash,
			RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny, Actions: []string{"override", "reject"},
			Targets: map[string]string{"override": "build"}, CreatedAt: now,
		},
		{
			Kind: approval.KindGrillQuestions, ID: "grill-1", RunID: "run", FromStage: "build", ToStage: "build",
			Trigger: approval.GrillTrigger, Status: approval.StatusPending, SubjectHash: testSubjectHash,
			RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny,
			Actions: []string{approval.GrillActionAnswer, approval.GrillActionNewVersion, "done"}, CreatedAt: now,
		},
		buildConfirmApproval(now.Add(time.Minute)),
	}
}

func stageRuns() map[string]lifecycle.State {
	return map[string]lifecycle.State{
		"run-input":    {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
		"run-check":    {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "check-1"},
		"run-grill":    {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "grill-1"},
		"run-skip":     {Phase: lifecycle.PhaseResumable, NextStage: "build"},
		"run-confirm":  {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
		"run-return":   {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
		"run-executor": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
	}
}

// The same people and stage actions as TestStageActionsAreAuthorizedByStageFunction,
// but over MCP: each action is allowed exactly where the web UI allows it, runs
// the same controller method, and is recorded under the token owner.
func TestMCPActionsAreAuthorizedByStageFunctionAsInTheWebUI(t *testing.T) {
	type action struct {
		name, right, tool, controllerAction string
		args                                map[string]any
	}
	actions := []action{
		{"take", "take", "take_stage", "", map[string]any{"run_id": "run-input", "stage_id": "build"}},
		{"executor", "take", "set_stage_executor", "executor", map[string]any{"run_id": "run-executor", "stage_id": "build", "executor": "human"}},
		{"grill done", "submit", "complete_stage_grill", "grill-done", map[string]any{"run_id": "run-grill", "stage_id": "build"}},
		{"override", "override", "override_stage_gate", "override", map[string]any{"run_id": "run-check", "stage_id": "build", "reason": "flaky check"}},
		{"skip", "bypass", "skip_stage", "skip", map[string]any{"run_id": "run-skip", "stage_id": "build", "reason": "not needed"}},
		{"confirm", "confirm", "confirm_stage", "", map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": testSubjectHash}},
		{"return", "confirm", "return_stage", "return", map[string]any{"run_id": "run-return", "stage_id": "build", "to_stage": "spec", "reason": "spec gap"}},
	}
	members := []struct {
		name, actor string
		allowed     map[string]bool
	}{
		{"developer executes but cannot confirm", actorDev, map[string]bool{"take": true, "submit": true, "bypass": true}},
		{"reviewer confirms but cannot execute", actorReviewer, map[string]bool{"confirm": true}},
		{"member with both functions", actorBoth, map[string]bool{"take": true, "submit": true, "bypass": true, "confirm": true}},
		{"foreign function is denied", actorQA, map[string]bool{}},
		{"system administrator overrides without any stage function", actorAdmin, map[string]bool{"override": true}},
		{"product owner without stage function is denied", actorPO, map[string]bool{}},
	}
	for _, member := range members {
		// One server per person: taking a stage is exclusive, so a second
		// person's take would conflict with the first one's.
		env := newMCPActionEnv(t, stageApprovals(time.Now().UTC()), stageRuns())
		client := env.as(t, member.actor)
		for _, current := range actions {
			t.Run(member.name+"/"+current.name, func(t *testing.T) {
				client.t = t
				beforeCalls := env.ctl.total()
				beforeDecisions, _ := env.ctl.decisions()
				if member.allowed[current.right] {
					client.do(current.tool, current.args)
					if current.controllerAction != "" {
						calls := env.ctl.callsOf(current.controllerAction)
						if len(calls) == 0 || calls[len(calls)-1].Actor != member.actor {
							t.Fatalf("%s must reach the controller as %s: %+v", current.name, member.actor, calls)
						}
					}
					if current.tool == "confirm_stage" {
						count, decision := env.ctl.decisions()
						if count != beforeDecisions+1 || decision.ActorID != member.actor || decision.Action != "approve" {
							t.Fatalf("the confirmation must be decided as %s: %d %+v", member.actor, count, decision)
						}
					}
					return
				}
				failure := client.refused(current.tool, current.args)
				if failure.Status != 403 || failure.Code != "forbidden" && failure.Code != "admin_required" {
					t.Fatalf("expected %s to be forbidden: %+v", current.name, failure)
				}
				if afterDecisions, _ := env.ctl.decisions(); env.ctl.total() != beforeCalls || afterDecisions != beforeDecisions {
					t.Fatalf("forbidden %s reached the controller", current.name)
				}
			})
		}
	}
}

// A tool never takes an actor from its arguments: the person is the token owner.
func TestMCPActionsNeverTakeTheActorFromArguments(t *testing.T) {
	env := newMCPActionEnv(t, stageApprovals(time.Now().UTC()), stageRuns())
	client := env.as(t, actorDev)
	failure := client.refused("take_stage", map[string]any{"run_id": "run-input", "stage_id": "build", "actor_id": actorOwner})
	if failure.Code != "bad_arguments" || !strings.Contains(failure.Error, "actor_id") {
		t.Fatalf("an actor argument must be refused: %+v", failure)
	}
	failure = client.refused("decide_approval", map[string]any{"run_id": "run-confirm", "approval_id": "gate-1", "action": "approve",
		"subject_hash": testSubjectHash, "actor_role": "reviewer"})
	if failure.Status != 403 {
		t.Fatalf("a developer must not decide as a reviewer by claiming the role: %+v", failure)
	}
}

func TestMCPConfirmNeedsTheExactHashAndTheRoleAndHonoursSelfApproval(t *testing.T) {
	submitted := time.Now().UTC().Add(-time.Hour)
	approvals := []approval.PendingApproval{
		buildInputApproval("input-1", approval.StatusResolved, submitted,
			approval.Decision{ActorID: actorBoth, ActorRole: "developer", Action: "submit"}),
		buildConfirmApproval(submitted.Add(30 * time.Minute)),
	}
	runs := map[string]lifecycle.State{"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"}}
	env := newMCPActionEnv(t, approvals, runs)
	reviewer := env.as(t, actorReviewer)
	confirm := map[string]any{"run_id": "run-confirm", "stage_id": "build"}

	// No hash: refused by the tool contract, before anything is touched.
	if failure := reviewer.refused("confirm_stage", confirm); failure.Code != "bad_arguments" || !strings.Contains(failure.Error, "subject_hash") {
		t.Fatalf("a confirmation without the hash must be refused: %+v", failure)
	}
	// Not a SHA-256: refused.
	if failure := reviewer.refused("confirm_stage", map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": "latest"}); failure.Code != "bad_arguments" {
		t.Fatalf("a non-hash must be refused: %+v", failure)
	}
	// A hash of another version: refused by the decision service (stale).
	stale := strings.Repeat("b", 64)
	if failure := reviewer.refused("confirm_stage", map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": stale}); failure.Status != 409 || failure.Code != "subject_hash_mismatch" {
		t.Fatalf("a stale hash must be refused: %+v", failure)
	}
	// The author of the stage result holds the approver function too, and is refused.
	if failure := env.as(t, actorBoth).refused("confirm_stage", map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": testSubjectHash}); failure.Status != 403 || failure.Code != "self_approval_forbidden" {
		t.Fatalf("self-approval must be refused: %+v", failure)
	}
	if count, _ := env.ctl.decisions(); count != 0 {
		t.Fatalf("refused confirmations must not be decided: %d", count)
	}
	// The reviewer who reviewed the exact version confirms; the audit shows MCP.
	key := freshKey()
	reviewer.do("confirm_stage", map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": testSubjectHash, mcpIdempotencyKey: key})
	count, decision := env.ctl.decisions()
	if count != 1 || decision.ActorID != actorReviewer || decision.ActorRole != "reviewer" || decision.SubjectHash != testSubjectHash {
		t.Fatalf("decision = %d %+v", count, decision)
	}
	audit := runEventData(t, env.srv, "run-confirm", humandecision.AuditEvent)
	if len(audit) != 1 || audit[0]["channel"] != "mcp" || audit[0]["actor_id"] != actorReviewer ||
		audit[0]["request_key"] != key || audit[0]["hash_confirmed_by_actor"] != true {
		t.Fatalf("decision audit = %v", audit)
	}
}

// decide_approval is the generic decision door: it needs the same hash, the
// role the approval requires, and it never resolves a check decision.
func TestMCPDecideApprovalUsesTheDecisionService(t *testing.T) {
	env := newMCPActionEnv(t, stageApprovals(time.Now().UTC()), stageRuns())
	args := func(extra map[string]any) map[string]any {
		out := map[string]any{"run_id": "run-confirm", "approval_id": "gate-1", "action": "approve", "actor_role": "reviewer", "subject_hash": testSubjectHash}
		for name, value := range extra {
			out[name] = value
		}
		return out
	}
	missing := args(nil)
	delete(missing, "subject_hash")
	if failure := env.as(t, actorReviewer).refused("decide_approval", missing); failure.Code != "bad_arguments" {
		t.Fatalf("a decision without the hash must be refused: %+v", failure)
	}
	if failure := env.as(t, actorDev).refused("decide_approval", args(nil)); failure.Status != 403 {
		t.Fatalf("a developer is not the approver: %+v", failure)
	}
	if failure := env.as(t, actorReviewer).refused("decide_approval", args(map[string]any{"action": "explode"})); failure.Status != 409 {
		t.Fatalf("an action the approval does not offer must be refused: %+v", failure)
	}
	if failure := env.as(t, actorReviewer).refused("decide_approval", args(map[string]any{"subject_hash": strings.Repeat("c", 64)})); failure.Code != "subject_hash_mismatch" {
		t.Fatalf("a stale hash must be refused: %+v", failure)
	}
	// A failed check cannot be resolved here: it has its own override/retry tools.
	failure := env.as(t, actorOwner).refused("decide_approval", map[string]any{"run_id": "run-check", "approval_id": "check-1", "action": "override",
		"actor_role": "developer", "subject_hash": testSubjectHash})
	if failure.Status != 409 {
		t.Fatalf("the generic door must not override a failed gate: %+v", failure)
	}
	env.as(t, actorReviewer).do("decide_approval", args(nil))
	if count, decision := env.ctl.decisions(); count != 1 || decision.ActorID != actorReviewer || decision.Action != "approve" {
		t.Fatalf("decision = %d %+v", count, decision)
	}
	audit := runEventData(t, env.srv, "run-confirm", humandecision.AuditEvent)
	if len(audit) != 1 || audit[0]["channel"] != "mcp" {
		t.Fatalf("decision audit = %v", audit)
	}
}

// Overriding a failed gate needs the system administrator right and a reason,
// over MCP exactly as in the UI; a product owner without the right is refused.
func TestMCPOverrideNeedsAdministratorRightAndReason(t *testing.T) {
	env := newMCPActionEnv(t, stageApprovals(time.Now().UTC()), stageRuns())
	args := map[string]any{"run_id": "run-check", "stage_id": "build", "reason": "risk accepted"}
	for _, actor := range []string{actorPO, actorDev, actorReviewer} {
		if failure := env.as(t, actor).refused("override_stage_gate", args); failure.Status != 403 || failure.Code != "admin_required" {
			t.Fatalf("%s must not override: %+v", actor, failure)
		}
	}
	if len(env.ctl.callsOf("override")) != 0 {
		t.Fatal("a refused override reached the controller")
	}
	admin := env.as(t, actorAdmin)
	if failure := admin.refused("override_stage_gate", map[string]any{"run_id": "run-check", "stage_id": "build"}); failure.Code != "bad_arguments" {
		t.Fatalf("an override without a reason must be refused: %+v", failure)
	}
	if failure := admin.refused("override_stage_gate", map[string]any{"run_id": "run-check", "stage_id": "build", "reason": "   "}); failure.Code != "reason_required" {
		t.Fatalf("a blank reason must be refused: %+v", failure)
	}
	admin.do("override_stage_gate", args)
	calls := env.ctl.callsOf("override")
	if len(calls) != 1 || calls[0].Actor != actorAdmin || calls[0].Reason != "risk accepted" {
		t.Fatalf("override calls = %+v", calls)
	}
}

// Resending an action with the same key returns the first answer and does not
// repeat it; the same key for another request is refused; a missing key is
// refused for a POST.
func TestMCPRepeatedDeliveryDoesNotRepeatTheAction(t *testing.T) {
	env := newMCPActionEnv(t, stageApprovals(time.Now().UTC()), stageRuns())
	reviewer := env.as(t, actorReviewer)
	args := map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": testSubjectHash, mcpIdempotencyKey: "deliver-1"}
	first, failed := reviewer.call("confirm_stage", args)
	if failed {
		t.Fatalf("first delivery: %s", first)
	}
	result, err := reviewer.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "confirm_stage", Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("second delivery: %v %+v", err, result)
	}
	if replayed, _ := result.Meta["idempotent_replay"].(bool); !replayed {
		t.Fatalf("the repeated delivery must say it is a replay: %+v", result.Meta)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; text != first {
		t.Fatalf("the replay must return the first answer:\n%s\n%s", first, text)
	}
	if count, _ := env.ctl.decisions(); count != 1 {
		t.Fatalf("the decision was recorded %d times", count)
	}
	if got := len(runEventData(t, env.srv, "run-confirm", humandecision.AuditEvent)); got != 1 {
		t.Fatalf("decision audited %d times", got)
	}
	if got := len(runEventData(t, env.srv, "run-confirm", mcpActionEvent)); got != 1 {
		t.Fatalf("the MCP action was audited %d times", got)
	}
	// The same key for a different request is a client bug: refused, not run.
	other := map[string]any{"run_id": "run-return", "stage_id": "build", "subject_hash": testSubjectHash, mcpIdempotencyKey: "deliver-1"}
	if failure := reviewer.refused("confirm_stage", other); failure.Code != "idempotency_conflict" || failure.Status != 409 {
		t.Fatalf("reused key = %+v", failure)
	}
	if count, _ := env.ctl.decisions(); count != 1 {
		t.Fatalf("the conflicting request must not run: %d", count)
	}
	// Another person's key space is separate: the same key from another
	// person is a new request, judged on its own.
	env.as(t, actorBoth).do("confirm_stage", args)
	if count, _ := env.ctl.decisions(); count != 2 {
		t.Fatalf("a second person's confirmation must be decided on its own: %d", count)
	}
	// A POST action without a key is refused up front.
	text, failed := reviewer.call("confirm_stage", map[string]any{"run_id": "run-return", "stage_id": "build", "subject_hash": testSubjectHash})
	if !failed || !strings.Contains(text, mcpIdempotencyKey) {
		t.Fatalf("a POST without idempotency_key must be refused: %s", text)
	}
	// A failing action that was refused is also remembered, so a retry of the
	// very same delivery cannot slip through after the situation changed.
	bad := map[string]any{"run_id": "run-confirm", "stage_id": "build", "subject_hash": strings.Repeat("d", 64), mcpIdempotencyKey: "deliver-2"}
	firstFailure, failedFirst := reviewer.call("confirm_stage", bad)
	secondFailure, failedSecond := reviewer.call("confirm_stage", bad)
	if !failedFirst || !failedSecond || firstFailure != secondFailure {
		t.Fatalf("a refused delivery must be answered the same way: %q / %q", firstFailure, secondFailure)
	}
}

// Starting a task twice with one key creates one task.
func TestMCPStartTaskIsIdempotentAndNeedsAProject(t *testing.T) {
	env := newMCPActionEnv(t, nil, map[string]lifecycle.State{})
	owner := env.as(t, actorOwner)
	project := owner.do("create_project", map[string]any{"name": "Payments", "description": "billing"})
	projectID := int(project["id"].(float64))

	if failure := owner.refused("start_task", map[string]any{"feature": "billing", "task": "do it"}); failure.Code != "bad_arguments" || !strings.Contains(failure.Error, "project_id") {
		t.Fatalf("a task without a project must be refused: %+v", failure)
	}
	start := map[string]any{"project_id": projectID, "feature": "billing", "task": "do it", "title": "T", "mode": "manual", mcpIdempotencyKey: "start-1"}
	first := owner.do("start_task", start)
	second := owner.do("start_task", start)
	if first["run_id"] == nil || first["run_id"] != second["run_id"] {
		t.Fatalf("the same key must give the same task: %v %v", first, second)
	}
	if got := len(env.ctl.callsOf("start")); got != 1 {
		t.Fatalf("the task was started %d times", got)
	}
	// The task is visible to the project filter and carries the actor.
	listed, _ := owner.call("list_tasks", map[string]any{"project_id": projectID})
	if !strings.Contains(listed, first["run_id"].(string)) {
		t.Fatalf("the new task is not in its project: %s", listed)
	}
	runID := first["run_id"].(string)
	queued := runEventData(t, env.srv, runID, "run_queued")
	if len(queued) != 1 || queued[0]["actor_id"] != actorOwner {
		t.Fatalf("run_queued = %v", queued)
	}
	// A developer has no right to start tasks... unless the role allows; the
	// answer must equal the REST answer for that role.
	devStatus := restStatusAs(t, env, actorDev, "POST", "/api/runs", `{"project_id":`+itoa(projectID)+`,"feature":"billing","task":"x"}`)
	_, devFailed := env.as(t, actorDev).call("start_task", map[string]any{"project_id": projectID, "feature": "billing", "task": "x", mcpIdempotencyKey: freshKey()})
	if (devStatus < 300) == devFailed {
		t.Fatalf("MCP and REST disagree for a developer starting a task: rest=%d mcp failed=%v", devStatus, devFailed)
	}
	// An archived project accepts no new tasks.
	owner.do("archive_project", map[string]any{"project_id": projectID})
	if failure := owner.refused("start_task", map[string]any{"project_id": projectID, "feature": "billing", "task": "more"}); failure.Code != "project_archived" {
		t.Fatalf("an archived project must refuse tasks: %+v", failure)
	}
	owner.do("unarchive_project", map[string]any{"project_id": projectID})
	owner.do("start_task", map[string]any{"project_id": projectID, "feature": "billing", "task": "more"})
}

func itoa(n int) string { return strings.TrimSpace(string(mustJSON(n))) }

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// runEventData lists the data of the events of one type of a run.
func runEventData(t *testing.T, srv *Server, runID, eventType string) []map[string]any {
	t.Helper()
	events, err := srv.store.GetRunEventsAfter(runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(event.DataJSON), &data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

// restStatusAs sends the same request through the REST door, with the browser
// session of the person, and returns the HTTP status.
func restStatusAs(t *testing.T, env *mcpActionEnv, actor, method, path, body string) int {
	t.Helper()
	return teamRequest(env.srv, env.cookies[actor], env.csrf[actor], method, path, body).Code
}
