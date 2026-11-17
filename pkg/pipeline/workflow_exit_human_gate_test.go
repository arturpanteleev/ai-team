package pipeline

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// workflow_exit_human_gate_test.go — finding A: a workflow-level human_approval
// on the exit chain must really require a decision even when the stage declares
// no gate of its own. ApproveGates stays false in runGrillPipeline.

// workflowExitHumanConfig builds a v7 config whose active workflow carries the
// only human_approval gate. Stage "tech" has no exit chain; "next" gives its
// forward transition a real target.
func workflowExitHumanConfig(onReject string) *config.Config {
	cfg := waveConfig(nil)
	// An empty Confirm gets the legacy "required" default for md results and
	// would inject a stage-level human_approval@1 that overrides the workflow
	// gate by instance name. Keep the stage chain empty so the workflow gate is
	// the only one in the effective chain.
	cfg.Stages[0].Confirm = "auto"
	cfg.SchemaVersion = config.WorkflowSchemaVersion
	cfg.ActiveWorkflow = config.DefaultWorkflowID
	cfg.Workflows = []config.WorkflowMeta{{
		ID:      config.DefaultWorkflowID,
		Title:   "SDLC",
		Version: 1,
		Graphs: []config.WorkflowGraph{{
			ID: config.DefaultGraphID, Template: "stage-check-test",
		}},
		ExitGateGroups: workflow.GateGroups{{
			workflow.GateRef{Gate: gatelib.GateHumanApproval, Version: 2,
				Params: map[string]any{"on_reject": onReject}},
		}},
	}}
	cfg.Stages = append(cfg.Stages, config.TemplateStage{
		ID: "next", Title: "Следующий", Function: "architect", Result: "md",
		Executor: "agent", Agent: "verifier", Confirm: "auto",
	})
	return cfg
}

func TestWorkflowExitHumanGate_ApprovalRequiredAndStayReject(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	cfg := workflowExitHumanConfig(gatelib.OnRejectStay)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)

	// ApproveGates is false: the declared workflow confirmation must pause the
	// run and raise a transition approval instead of moving on silently.
	if err := runGrillPipeline(t, p, dir, ""); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("workflow-only human_approval must require a decision: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := pendingTransition(t, store, runID)
	if got := pending.Targets["reject"]; got != "tech" {
		t.Fatalf("on_reject:stay from the workflow gate must keep the task at tech: %+v", pending.Targets)
	}
	if attempts := humanGateAttempts(t, dir); len(attempts) != 1 ||
		attempts[0].Execution != workflow.GateExecutionPending ||
		attempts[0].Executor != workflow.GateKindHuman {
		t.Fatalf("the workflow human gate must be recorded as waiting: %+v", attempts)
	}

	// A rejection returns the task to tech for a new version and a new decision.
	decide(t, store, runID, pending, "reject", "нужны метрики")
	if err := runGrillPipeline(t, p, dir, runID); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("after the refusal the task must stay at tech and wait again: %v", err)
	}
	state, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	techRuns := 0
	for _, name := range rt.executed {
		if name == "tech" {
			techRuns++
		}
	}
	if loaded.Phase != lifecycle.PhaseWaiting || techRuns != 2 ||
		pendingTransition(t, store, runID).AttemptID == pending.AttemptID {
		t.Fatalf("the task must stay at tech and ask again (runs=%d): %+v", techRuns, loaded)
	}
	attempts := humanGateAttempts(t, dir)
	if len(attempts) != 2 || attempts[0].Verdict != workflow.GateVerdictFail ||
		attempts[1].Execution != workflow.GateExecutionPending {
		t.Fatalf("want a recorded refusal and a new waiting decision: %+v", attempts)
	}
}
