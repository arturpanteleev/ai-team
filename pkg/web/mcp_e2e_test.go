package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/humandecision"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// mcp_e2e_test.go — the S13 acceptance scenarios against the real run engine,
// task controller, approval store and lifecycle (only the model is absent).
// Every step is an MCP tool call by a different person of a cloud team.

// realControllerEnv wires the real run engine and task controller into a
// cloud-mode server, so MCP actions travel through the same approval store,
// lifecycle and pipeline as the web UI.
func realControllerEnv(t *testing.T, template string) *mcpActionEnv {
	t.Helper()
	env := newMCPActionEnvFor(t, template, nil, nil)
	cfg, err := config.ParseYAML([]byte(strings.Replace(template, "%FORBID%", "true", 1)))
	if err != nil {
		t.Fatal(err)
	}
	engine := pipeline.NewRunEngine(pipeline.New(cfg, e2eRegistry(),
		pipeline.WithRuntimeFactory(func(string) (runtime.Runtime, error) { return resultWithoutSection{}, nil }),
		pipeline.WithNotifier(e2eNotifier{}),
		pipeline.WithRecorder(NewStoreRecorder(env.srv.store))))
	controller, err := control.New(engine, env.target)
	if err != nil {
		t.Fatal(err)
	}
	env.srv.controller = controller
	taskDir := filepath.Join(env.target, ".ai-team", "artifacts", "tasks", "feat")
	if err = os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(taskDir, "task.md"), []byte("задача"), 0o644); err != nil {
		t.Fatal(err)
	}
	return env
}

type pendingStep struct {
	ID, Hash, Stage, Trigger, Kind string
	Actions, RequiredRoles         []string
}

// waitPending polls get_task (an MCP read) until the task waits on an approval
// the predicate accepts, and returns it. Long actions return at once; this is
// the "separate way to get the state" of the action contract.
func waitPending(t *testing.T, env *mcpActionEnv, reader *mcpActor, runID, what string, accept func(pendingStep) bool) pendingStep {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		text, failed := reader.call("get_task", map[string]any{"run_id": runID})
		if !failed {
			var task struct {
				Approvals []struct {
					ID          string   `json:"id"`
					Kind        string   `json:"kind"`
					FromStage   string   `json:"from_stage"`
					Trigger     string   `json:"trigger"`
					SubjectHash string   `json:"subject_hash"`
					Status      string   `json:"status"`
					Actions     []string `json:"actions"`
					Roles       []string `json:"required_roles"`
				} `json:"approvals"`
			}
			if err := json.Unmarshal([]byte(text), &task); err != nil {
				t.Fatalf("get_task: %v", err)
			}
			for _, value := range task.Approvals {
				step := pendingStep{ID: value.ID, Hash: value.SubjectHash, Stage: value.FromStage, Trigger: value.Trigger, Kind: value.Kind,
					Actions: value.Actions, RequiredRoles: value.Roles}
				if value.Status == "pending" && accept(step) {
					// The engine persists the approval a moment before the task
					// lifecycle points at it; act only when both agree, as a
					// person looking at the screen would.
					waitForPhase(t, env, runID, func(s lifecycle.State) bool { return s.PendingApprovalID == step.ID }, "the lifecycle to reach "+what)
					for reporter, ok := env.srv.controller.(activeRunReporter); ok && reporter.RunActive(runID); {
						if time.Now().After(deadline) {
							t.Fatalf("the worker of %s did not park", what)
						}
						time.Sleep(10 * time.Millisecond)
					}
					return step
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last get_task: %s", what, text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForPhase(t *testing.T, env *mcpActionEnv, runID string, accept func(lifecycle.State) bool, what string) lifecycle.State {
	t.Helper()
	states, err := lifecycle.NewStore(env.target)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		state, loadErr := states.Load(runID)
		if loadErr == nil && accept(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v %v", what, state, loadErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The acceptance scenario of S13 with human stages: several people, each with
// their own token, create the project and the task, pass the stages, submit
// results, decide on the exact versions they reviewed, are refused where the
// process forbids it, and a redelivered request does not double a decision.
func TestMCPEndToEnd_PeopleCreateRunSubmitDecideAndReturn(t *testing.T) {
	env := realControllerEnv(t, functionPermissionsTemplate)
	owner, po, dev, reviewer, qa := env.as(t, actorOwner), env.as(t, actorPO), env.as(t, actorDev), env.as(t, actorReviewer), env.as(t, actorQA)

	project := owner.do("create_project", map[string]any{"name": "Checkout"})
	started := owner.do("start_task", map[string]any{"project_id": project["id"], "feature": "feat", "task": "Add one-click checkout", mcpIdempotencyKey: "task-1"})
	runID := started["run_id"].(string)
	if replay := owner.do("start_task", map[string]any{"project_id": project["id"], "feature": "feat", "task": "Add one-click checkout", mcpIdempotencyKey: "task-1"}); replay["run_id"] != runID {
		t.Fatalf("a redelivered start must give the same task: %v", replay)
	}
	if text, _ := owner.call("list_tasks", map[string]any{"project_id": project["id"]}); strings.Count(text, runID) != 1 {
		t.Fatalf("exactly one task must exist: %s", text)
	}

	// Stage 1 (spec): the Product Owner function submits; somebody else decides.
	waitPending(t, env, po, runID, "the spec input", func(p pendingStep) bool { return p.Stage == "spec" && p.Kind == "input" })
	if failure := dev.refused("submit_stage_result", map[string]any{"run_id": runID, "stage_id": "spec", "result": "md", "content": "# Spec"}); failure.Status != 403 {
		t.Fatalf("a developer must not submit the spec: %+v", failure)
	}
	po.do("submit_stage_result", map[string]any{"run_id": runID, "stage_id": "spec", "result": "md", "content": "# Spec\n\nOne click.\n"})
	confirmSpec := waitPending(t, env, po, runID, "the spec confirmation", func(p pendingStep) bool { return p.Stage == "spec" && p.Kind != "input" })
	if !contains(confirmSpec.RequiredRoles, "product_owner") {
		t.Fatalf("the spec confirmation must ask for the Product Owner: %v", confirmSpec.RequiredRoles)
	}

	decide := func(hash string) map[string]any {
		return map[string]any{"run_id": runID, "approval_id": confirmSpec.ID, "action": "approve", "actor_role": "product_owner", "subject_hash": hash}
	}
	if failure := po.refused("decide_approval", decide(confirmSpec.Hash)); failure.Status != 403 || failure.Code != "self_approval_forbidden" {
		t.Fatalf("the author must not approve the own result: %+v", failure)
	}
	if failure := owner.refused("decide_approval", decide(strings.Repeat("0", 64))); failure.Code != "subject_hash_mismatch" {
		t.Fatalf("a stale hash must be refused: %+v", failure)
	}
	if failure := qa.refused("decide_approval", decide(confirmSpec.Hash)); failure.Status != 403 {
		t.Fatalf("a person without the role must not decide: %+v", failure)
	}
	if failure := owner.refused("decide_approval", map[string]any{"run_id": runID, "approval_id": confirmSpec.ID, "action": "approve", "actor_role": "product_owner"}); failure.Code != "bad_arguments" {
		t.Fatalf("a decision without the hash must be refused: %+v", failure)
	}
	approve := withKey(decide(confirmSpec.Hash), "confirm-spec")
	owner.do("decide_approval", approve)
	owner.do("decide_approval", approve) // redelivery
	// The generic decision door records the decision only; the task is carried
	// on by resume_task (confirm_stage does both in one step).
	owner.do("resume_task", map[string]any{"run_id": runID})

	// Stage 2 (build): developer submits, only the reviewer decides.
	waitPending(t, env, dev, runID, "the build input", func(p pendingStep) bool { return p.Stage == "build" && p.Kind == "input" })
	dev.do("submit_stage_result", map[string]any{"run_id": runID, "stage_id": "build", "result": "md", "content": "# Build\n\nImplemented.\n"})
	confirmBuild := waitPending(t, env, reviewer, runID, "the build confirmation", func(p pendingStep) bool { return p.Stage == "build" && p.Kind != "input" })
	if failure := dev.refused("decide_approval", map[string]any{"run_id": runID, "approval_id": confirmBuild.ID, "action": "approve", "actor_role": "developer", "subject_hash": confirmBuild.Hash}); failure.Status != 403 {
		t.Fatalf("the developer is not the approver: %+v", failure)
	}
	if !contains(confirmBuild.Actions, "return_to_spec") {
		t.Fatalf("the build confirmation must offer the configured return: %v", confirmBuild.Actions)
	}
	// The reviewer sends the work back to the spec; the process loops to its input.
	reviewer.do("decide_approval", map[string]any{"run_id": runID, "approval_id": confirmBuild.ID, "action": "return_to_spec",
		"actor_role": "reviewer", "subject_hash": confirmBuild.Hash, "comment": "the spec lacks error handling"})
	owner.do("resume_task", map[string]any{"run_id": runID})
	waitPending(t, env, po, runID, "the returned spec input", func(p pendingStep) bool { return p.Stage == "spec" && p.Kind == "input" && p.ID != confirmSpec.ID })

	// Evidence: the decision on the spec exists once, as the owner, over MCP.
	audit := runEventData(t, env.srv, runID, humandecision.AuditEvent)
	var specDecisions []map[string]any
	for _, record := range audit {
		if record["stage"] == "spec" && record["actor_id"] == actorOwner {
			specDecisions = append(specDecisions, record)
		}
	}
	if len(specDecisions) != 1 || specDecisions[0]["channel"] != "mcp" || specDecisions[0]["subject_hash"] != confirmSpec.Hash ||
		specDecisions[0]["request_key"] != "confirm-spec" || specDecisions[0]["hash_confirmed_by_actor"] != true {
		t.Fatalf("the spec confirmation must be recorded once, over MCP, with the exact hash: %v", audit)
	}
	approvals, err := approval.NewStore(env.target)
	if err != nil {
		t.Fatal(err)
	}
	all, err := approvals.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range all {
		if value.ID == confirmSpec.ID && len(value.Decisions) != 1 {
			t.Fatalf("the redelivered confirmation was stored %d times", len(value.Decisions))
		}
	}
	// Every MCP action is attributable in the run's event feed.
	var actions []string
	for _, record := range runEventData(t, env.srv, runID, mcpActionEvent) {
		if record["channel"] != "mcp" || record["token_id"] == nil || record["client"] == nil {
			t.Fatalf("an MCP audit record must name the channel and the client: %v", record)
		}
		actions = append(actions, record["tool"].(string)+"/"+record["actor_id"].(string))
	}
	for _, want := range []string{"start_task/" + actorOwner, "submit_stage_result/" + actorPO, "decide_approval/" + actorOwner,
		"submit_stage_result/" + actorDev, "decide_approval/" + actorReviewer} {
		if !containsString(actions, want) {
			t.Errorf("the audit lacks %s: %v", want, actions)
		}
	}
}

// The override path with real gates: the required gate fails, the technical
// state is readable as gate reports, only an administrator with a reason passes
// the task on, the gate stays failed, and a redelivery does not override twice.
func TestMCPEndToEnd_FailedGateIsOverriddenOnlyByAnAdministrator(t *testing.T) {
	cfg := e2eConfig()
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	env := realControllerEnv(t, string(data))
	owner, po, dev, admin := env.as(t, actorOwner), env.as(t, actorPO), env.as(t, actorDev), env.as(t, actorAdmin)

	project := owner.do("create_project", map[string]any{"name": "Gates"})
	started := owner.do("start_task", map[string]any{"project_id": project["id"], "feature": "feat", "task": "задача"})
	runID := started["run_id"].(string)
	check := waitPending(t, env, po, runID, "the failed gate decision", func(p pendingStep) bool { return p.Kind == string(approval.KindCheckDecision) })

	// The gate reports are readable by everybody who may read the task.
	text, failed := dev.call("get_task_gates", map[string]any{"run_id": runID})
	var report taskGatesResponse
	if failed || json.Unmarshal([]byte(text), &report) != nil || len(report.Stages) != 1 || report.Stages[0].Current == nil {
		t.Fatalf("gate report: %v %s", failed, text)
	}
	var required *gatesched.GateStatus
	for index := range report.Stages[0].Current.Gates {
		if report.Stages[0].Current.Gates[index].Gate == gatelib.GateRequiredSections {
			required = &report.Stages[0].Current.Gates[index]
		}
	}
	if required == nil || required.Attempt == nil || required.Attempt.Verdict != workflow.GateVerdictFail || required.Overridden {
		t.Fatalf("the required gate must have failed: %+v", required)
	}
	if !contains(check.Actions, "override") {
		t.Fatalf("the decision must offer the override: %v", check.Actions)
	}

	override := map[string]any{"run_id": runID, "stage_id": "tech", "reason": "риск принят, релиз срочный"}
	for name, who := range map[string]*mcpActor{"product owner": po, "developer": dev, "owner without an administrator token": po} {
		if failure := who.refused("override_stage_gate", override); failure.Status != 403 || failure.Code != "admin_required" {
			t.Fatalf("%s must not override a failed gate: %+v", name, failure)
		}
	}
	// The generic decision door cannot be used to get around the right.
	if failure := po.refused("decide_approval", map[string]any{"run_id": runID, "approval_id": check.ID, "action": "override", "actor_role": "architect", "subject_hash": check.Hash}); failure.Status < 400 {
		t.Fatalf("the generic door must not override: %+v", failure)
	}
	states, err := lifecycle.NewStore(env.target)
	if err != nil {
		t.Fatal(err)
	}
	if state, loadErr := states.Load(runID); loadErr != nil || state.Phase != lifecycle.PhaseWaiting {
		t.Fatalf("refused overrides must leave the task parked: %+v %v", state, loadErr)
	}
	if failure := admin.refused("override_stage_gate", map[string]any{"run_id": runID, "stage_id": "tech"}); failure.Code != "bad_arguments" {
		t.Fatalf("an administrator without a reason: %+v", failure)
	}

	key := withKey(override, "override-1")
	admin.do("override_stage_gate", key)
	admin.do("override_stage_gate", key) // the same delivery again
	waitForPhase(t, env, runID, func(s lifecycle.State) bool {
		return s.Phase != lifecycle.PhaseWaiting && s.Phase != lifecycle.PhaseRunning
	}, "the override to carry the task on")

	events, err := evidence.VerifyEventLogWithSource(filepath.Join(env.target, ".ai-team", "runs", runID, "events.jsonl"), runID, nil)
	if err != nil {
		t.Fatal(err)
	}
	overrides := eventsOfTypeWeb(events, "check_overridden")
	if len(overrides) != 1 {
		t.Fatalf("one override record expected (a redelivery must not repeat it): %d", len(overrides))
	}
	record, ok := gatesched.DecodeOverride(overrides[0])
	if !ok || record.Authority != approval.AuthoritySystemAdmin || record.ActorID != actorAdmin || record.Reason != override["reason"] || record.ArtifactSHA256 == "" {
		t.Fatalf("override evidence = %+v", record)
	}
	// The report shows the override and the gate is still red.
	text, _ = dev.call("get_task_gates", map[string]any{"run_id": runID})
	if err = json.Unmarshal([]byte(text), &report); err != nil || len(report.Overrides) != 1 || report.Overrides[0].ActorID != actorAdmin {
		t.Fatalf("the override must be visible in the gate report: %v %s", err, text)
	}
	if got := report.Stages[0].Current.Gates[0]; got.Attempt == nil || got.Attempt.Verdict != workflow.GateVerdictFail {
		t.Fatalf("the overridden gate must stay a fail: %+v", got)
	}
	// And the MCP audit tells who did it through which client.
	var seen int
	for _, entry := range runEventData(t, env.srv, runID, mcpActionEvent) {
		if entry["tool"] == "override_stage_gate" {
			seen++
			if entry["actor_id"] != actorAdmin || entry["channel"] != "mcp" || entry["idempotency_key"] != "override-1" {
				t.Fatalf("override audit = %v", entry)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("the override was audited %d times", seen)
	}
}

func contains(values []string, want string) bool { return containsString(values, want) }
