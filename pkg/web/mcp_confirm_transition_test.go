package web

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// startSpecConfirmation drives a real task up to the confirmation of the spec
// stage (function po, the run moves on to build) and returns the confirmation.
func startSpecConfirmation(t *testing.T, env *mcpActionEnv, key string) (string, pendingStep) {
	t.Helper()
	owner, po := env.as(t, actorOwner), env.as(t, actorPO)
	project := owner.do("create_project", map[string]any{"name": "Checkout " + key})
	started := owner.do("start_task", map[string]any{"project_id": project["id"], "feature": "feat", "task": "x", mcpIdempotencyKey: key})
	runID := started["run_id"].(string)
	waitPending(t, env, po, runID, "the spec input", func(p pendingStep) bool { return p.Stage == "spec" && p.Kind == "input" })
	po.do("submit_stage_result", map[string]any{"run_id": runID, "stage_id": "spec", "result": "md", "content": "# Spec\n"})
	confirmation := waitPending(t, env, po, runID, "the spec confirmation", func(p pendingStep) bool { return p.Stage == "spec" && p.Kind != "input" })
	state := waitForPhase(t, env, runID, func(s lifecycle.State) bool { return s.PendingApprovalID == confirmation.ID }, "the spec confirmation")
	if state.NextStage != "build" {
		t.Fatalf("while the spec confirmation waits the run's next stage is the destination: %q", state.NextStage)
	}
	return runID, confirmation
}

// Confirming a transition names the stage being confirmed (spec), not the one
// the run moves to (build). The Product Owner function confirms it, the author
// does not confirm the own result, a person without the function is refused,
// and the stage the run moves to is still accepted for older clients.
func TestMCPConfirmStageOfTheCompletedStageOnARealController(t *testing.T) {
	env := realControllerEnv(t, functionPermissionsTemplate)
	owner, po, dev, reviewer := env.as(t, actorOwner), env.as(t, actorPO), env.as(t, actorDev), env.as(t, actorReviewer)
	runID, confirmation := startSpecConfirmation(t, env, "confirm-new")
	args := func(stage, hash string) map[string]any {
		return map[string]any{"run_id": runID, "stage_id": stage, "subject_hash": hash}
	}

	if failure := dev.refused("confirm_stage", args("spec", confirmation.Hash)); failure.Status != 403 {
		t.Fatalf("a developer must not confirm the spec: %+v", failure)
	}
	if failure := reviewer.refused("confirm_stage", args("spec", confirmation.Hash)); failure.Status != 403 {
		t.Fatalf("a reviewer must not confirm the spec: %+v", failure)
	}
	if failure := po.refused("confirm_stage", args("spec", confirmation.Hash)); failure.Status != 403 || failure.Code != "self_approval_forbidden" {
		t.Fatalf("the author of the spec must not confirm it: %+v", failure)
	}
	if failure := owner.refused("confirm_stage", args("accept", confirmation.Hash)); failure.Status != 409 {
		t.Fatalf("a stage that is neither confirmed nor next must be refused: %+v", failure)
	}
	if failure := owner.refused("confirm_stage", args("spec", strings.Repeat("0", 64))); failure.Code != "subject_hash_mismatch" {
		t.Fatalf("the exact hash is still required: %+v", failure)
	}
	if failure := owner.refused("confirm_stage", map[string]any{"run_id": runID, "stage_id": "spec"}); failure.Code != "bad_arguments" {
		t.Fatalf("MCP must name the hash: %+v", failure)
	}
	owner.do("confirm_stage", args("spec", confirmation.Hash))
	waitPending(t, env, dev, runID, "the build input", func(p pendingStep) bool { return p.Stage == "build" && p.Kind == "input" })
}

// The run's next stage as the stage_id (what the web UI sends) keeps working,
// and the confirmed stage's function, not the next stage's, decides who may act.
func TestMCPConfirmStageAcceptsTheNextStageForOlderClients(t *testing.T) {
	env := realControllerEnv(t, functionPermissionsTemplate)
	owner, dev := env.as(t, actorOwner), env.as(t, actorDev)
	runID, confirmation := startSpecConfirmation(t, env, "confirm-legacy")
	args := map[string]any{"run_id": runID, "stage_id": "build", "subject_hash": confirmation.Hash}
	if failure := dev.refused("confirm_stage", args); failure.Status != 403 {
		t.Fatalf("the developer function of the next stage gives no right to confirm the spec: %+v", failure)
	}
	owner.do("confirm_stage", args)
	waitPending(t, env, dev, runID, "the build input", func(p pendingStep) bool { return p.Stage == "build" && p.Kind == "input" })
}

// A return from a transition confirmation starts at the confirmed stage.
func TestMCPReturnStageFromAConfirmationOnARealController(t *testing.T) {
	env := realControllerEnv(t, functionPermissionsTemplate)
	owner, po, dev, reviewer := env.as(t, actorOwner), env.as(t, actorPO), env.as(t, actorDev), env.as(t, actorReviewer)
	runID, confirmation := startSpecConfirmation(t, env, "return-new")
	owner.do("confirm_stage", map[string]any{"run_id": runID, "stage_id": "spec", "subject_hash": confirmation.Hash})
	waitPending(t, env, dev, runID, "the build input", func(p pendingStep) bool { return p.Stage == "build" && p.Kind == "input" })
	dev.do("submit_stage_result", map[string]any{"run_id": runID, "stage_id": "build", "result": "md", "content": "# Build\n"})
	build := waitPending(t, env, reviewer, runID, "the build confirmation", func(p pendingStep) bool { return p.Stage == "build" && p.Kind != "input" })
	waitForPhase(t, env, runID, func(s lifecycle.State) bool { return s.PendingApprovalID == build.ID }, "the build confirmation")

	back := map[string]any{"run_id": runID, "stage_id": "build", "to_stage": "spec", "reason": "the spec lacks error handling"}
	if failure := po.refused("return_stage", back); failure.Status != 403 {
		t.Fatalf("a Product Owner must not send the build back: %+v", failure)
	}
	reviewer.do("return_stage", back)
	waitPending(t, env, po, runID, "the returned spec input", func(p pendingStep) bool { return p.Stage == "spec" && p.Kind == "input" && p.ID != confirmation.ID })
}
