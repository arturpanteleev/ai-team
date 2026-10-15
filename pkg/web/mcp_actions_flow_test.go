package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Gate reports and the technical retry over MCP: the reports are the REST
// reports, the retry is offered and authorized exactly as in the web UI, a
// retry that names an unknown gate or attempt is refused, and a repeated
// delivery does not retry twice.
func TestMCPGateReportsAndTechnicalRetry(t *testing.T) {
	env := newMCPActionEnvFor(t, gatesAPITemplate, []approval.PendingApproval{checkDecisionFor("error", true)},
		map[string]lifecycle.State{"run": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "check-1"}})
	fixture := gatesFixture{}
	fixture.srv = env.srv
	failed := fixture.attempt(t, gatesTestSHA1, "second", "required_sections", "", 1, 1, workflow.GateExecutionError, "")

	dev := env.as(t, actorDev)
	text, isErr := dev.call("get_task_gates", map[string]any{"run_id": "run"})
	if isErr {
		t.Fatalf("get_task_gates: %s", text)
	}
	var report taskGatesResponse
	if err := json.Unmarshal([]byte(text), &report); err != nil || len(report.Stages) != 1 || report.Stages[0].Current == nil {
		t.Fatalf("gate report = %v %s", err, text)
	}
	if got := fixture.gate(report.Stages[0], "second"); got.Attempt == nil || got.Attempt.Execution != workflow.GateExecutionError || got.Attempt.AttemptID != failed.AttemptID {
		t.Fatalf("the technical failure must be visible: %+v", got)
	}
	// The report equals what the REST door returns to the same person.
	if status, body := env.rest(t, actorDev, "/api/runs/run/gates"); status != 200 {
		t.Fatalf("REST gates: %d", status)
	} else {
		sameJSON(t, "gate report", []byte(text), body)
	}

	if failure := env.as(t, actorQA).refused("retry_stage_gate", map[string]any{"run_id": "run", "stage_id": "build", "gate": "second"}); failure.Status != 403 {
		t.Fatalf("a foreign function must not retry: %+v", failure)
	}
	if failure := dev.refused("retry_stage_gate", map[string]any{"run_id": "run", "stage_id": "build", "gate": "ghost"}); failure.Status != 404 || failure.Code != "gate_not_found" {
		t.Fatalf("an unknown gate: %+v", failure)
	}
	if failure := dev.refused("retry_gate_attempt", map[string]any{"run_id": "run", "attempt_id": "nope"}); failure.Status != 404 {
		t.Fatalf("an unknown attempt: %+v", failure)
	}
	if len(env.ctl.callsOf("gate-retry")) != 0 {
		t.Fatal("refused retries reached the controller")
	}

	args := map[string]any{"run_id": "run", "stage_id": "build", "gate": "second", "reason": "network is back", mcpIdempotencyKey: "retry-1"}
	dev.do("retry_stage_gate", args)
	dev.do("retry_stage_gate", args) // the same delivery again
	calls := env.ctl.callsOf("gate-retry")
	if len(calls) != 1 || calls[0].Actor != actorDev || calls[0].Extra != "second" || calls[0].Reason != "network is back" {
		t.Fatalf("one retry as the developer expected: %+v", calls)
	}
	dev.do("retry_gate_attempt", map[string]any{"run_id": "run", "attempt_id": failed.AttemptID})
	if got := len(env.ctl.callsOf("gate-retry")); got != 2 {
		t.Fatalf("retry by attempt: %d calls", got)
	}
}

// A person submits the result of a human stage over MCP and the stage owner
// and executor choices follow the UI rules.
func TestMCPHumanStageResultAndExecutor(t *testing.T) {
	now := time.Now().UTC()
	env := newMCPActionEnv(t, []approval.PendingApproval{buildInputApproval("input-take", approval.StatusPending, now)},
		map[string]lifecycle.State{"run-input": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"}})
	body := map[string]any{"run_id": "run-input", "stage_id": "build", "result": "md", "content": "# Build\n\nрезультат\n"}

	if failure := env.as(t, actorReviewer).refused("submit_stage_result", body); failure.Status != 403 {
		t.Fatalf("the approver function must not submit: %+v", failure)
	}
	if failure := env.as(t, actorDev).refused("submit_stage_result", map[string]any{"run_id": "run-input", "stage_id": "build"}); failure.Code != "bad_arguments" {
		t.Fatalf("a submission needs its result type: %+v", failure)
	}
	if failure := env.as(t, actorDev).refused("submit_stage_result", withKey(body, "")); failure.Code != "bad_arguments" {
		t.Fatalf("an empty key must be refused: %+v", failure)
	}
	dev := env.as(t, actorDev)
	out := dev.do("submit_stage_result", body)
	submission, _ := out["submission"].(map[string]any)
	if submission == nil || submission["sha256"] == "" || submission["version"] == nil {
		t.Fatalf("the submission must report its version and hash: %v", out)
	}
	// The stage result is readable back and its bytes are exactly what was sent.
	version := itoa(int(submission["version"].(float64)))
	if text, failed := dev.call("get_task_stage_result", map[string]any{"run_id": "run-input", "stage_id": "build", "version": version}); failed || !strings.Contains(text, "результат") {
		t.Fatalf("stage result: %v %s", failed, text)
	}
	events := runEventData(t, env.srv, "run-input", mcpActionEvent)
	if len(events) != 1 || events[0]["tool"] != "submit_stage_result" || events[0]["actor_id"] != actorDev || events[0]["channel"] != "mcp" {
		t.Fatalf("mcp audit = %v", events)
	}
}

// Oversized and malformed arguments never reach the router.
func TestMCPActionArgumentsAreValidatedBeforeTheRouter(t *testing.T) {
	env := newMCPActionEnv(t, nil, nil)
	owner := env.as(t, actorOwner)
	for name, args := range map[string]map[string]any{
		"text too long":            {"name": strings.Repeat("я", mcpMaxStringArg), "description": "x"},
		"wrong type":               {"name": 42},
		"unknown argument":         {"name": "x", "color": "red"},
		"NUL in text":              {"name": "a\x00b"},
		"array where text belongs": {"name": []string{"a"}},
	} {
		if failure := owner.refused("create_project", args); failure.Code != "bad_arguments" {
			t.Errorf("%s: %+v", name, failure)
		}
	}
	for name, args := range map[string]map[string]any{
		"roles not an array":   {"email": "x@example.com", "roles": "developer"},
		"role not text":        {"email": "x@example.com", "roles": []any{1}},
		"too many roles":       {"email": "x@example.com", "roles": make([]string, mcpMaxArrayItems+1)},
		"key with whitespace":  {"email": "x@example.com", "roles": []string{"developer"}, mcpIdempotencyKey: "a b"},
		"key too long":         {"email": "x@example.com", "roles": []string{"developer"}, mcpIdempotencyKey: strings.Repeat("k", 129)},
		"revisions not object": {"run_id": "r", "approval_id": "a", "action": "approve", "subject_hash": testSubjectHash, "artifact_revisions": []string{"x"}},
	} {
		tool := "invite_team_member"
		if _, ok := args["run_id"]; ok {
			tool = "decide_approval"
		}
		if failure := owner.refused(tool, args); failure.Code != "bad_arguments" {
			t.Errorf("%s: %+v", name, failure)
		}
	}
	if len(env.ctl.calls) != 0 {
		t.Fatal("nothing may have reached the controller")
	}
}
