package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// workflow_exit_gates_test.go — finding 1 (runtime wiring): workflow exit gates
// execute on a stage that has no exit chain of its own, and the human gate
// resolves against the merged (workflow + stage) chain.

// workflowExitConfig is the single-agent "tech" stage (stageCheckConfig) with
// a schema-v7 workflow whose exit chain is the given groups. The stage itself
// declares no chain, so the whole effective exit chain comes from the workflow.
func workflowExitConfig(exit workflow.GateGroups) *config.Config {
	cfg := stageCheckConfig(nil)
	cfg.SchemaVersion = config.WorkflowSchemaVersion
	cfg.Workflows = []config.WorkflowMeta{{
		ID:             config.DefaultWorkflowID,
		Title:          "SDLC",
		Version:        1,
		Graphs:         []config.WorkflowGraph{{ID: config.DefaultGraphID, Template: cfg.Template}},
		ExitGateGroups: exit,
	}}
	return cfg
}

func TestWorkflowExitGate_RunsOnStageWithoutOwnChain(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез маркера вердикта\n"}
	cfg := workflowExitConfig(workflow.GateGroups{{codeGate(gatelib.GateVerdictMarker, nil)}})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a failing workflow exit gate must block a stage without its own chain: %v", err)
	}
	if !strings.Contains(readEvents(t, dir), "check_failed") {
		t.Fatalf("expected check_failed from the workflow exit gate:\n%s", readEvents(t, dir))
	}
	history, err := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	if err != nil || len(history) != 1 || history[0].Gate != gatelib.GateVerdictMarker || history[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("the workflow gate must record a failed attempt: %+v err=%v", history, err)
	}
}

func TestWorkflowExitGate_PassesOnStageWithoutOwnChain(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n**Verdict:** APPROVED\n"}
	cfg := workflowExitConfig(workflow.GateGroups{{codeGate(gatelib.GateVerdictMarker, nil)}})
	if err := runStageCheckPipeline(t, dir, cfg, rt); err != nil {
		t.Fatalf("a passing workflow exit gate must let the stage through: %v", err)
	}
}

func TestWorkflowExitGate_RequiredPhrasesBlocks(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез критериев\n"}
	cfg := workflowExitConfig(workflow.GateGroups{{codeGate(gatelib.GateRequiredPhrases, map[string]any{
		"phrases": []any{"Критерии приёмки"}, "case": "insensitive", "scope": "result", "match": "all",
	})}})
	err := runStageCheckPipeline(t, dir, cfg, rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a failing workflow required_phrases gate must block: %v", err)
	}
	history, _ := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	if len(history) != 1 || history[0].Gate != gatelib.GateRequiredPhrases || history[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("the workflow required_phrases gate must record a fail: %+v", history)
	}
}

func TestAutomaticGateGroups_DropsWorkflowHumanApproval(t *testing.T) {
	cfg := workflowExitConfig(workflow.GateGroups{
		{codeGate(gatelib.GateVerdictMarker, nil)},
		{codeGate(gatelib.GateHumanApproval, nil)},
	})
	rs := &runState{p: &Pipeline{cfg: cfg}}
	groups, err := rs.automaticGateGroups(cfg.Stages[0])
	if err != nil {
		t.Fatal(err)
	}
	refs := groups.Refs()
	if len(refs) != 1 || refs[0].Gate != gatelib.GateVerdictMarker {
		t.Fatalf("automatic groups must keep the workflow auto gate and drop human_approval: %+v", refs)
	}
}

func TestStageHumanGate_SeesWorkflowHumanGate(t *testing.T) {
	cfg := workflowExitConfig(workflow.GateGroups{{codeGate(gatelib.GateHumanApproval, nil)}})
	rs := &runState{p: &Pipeline{cfg: cfg}}
	ref, group, ok, err := rs.stageHumanGate(cfg.Stages[0])
	if err != nil || !ok || ref.Gate != gatelib.GateHumanApproval || group != 0 {
		t.Fatalf("a workflow human gate must be visible for a stage without its own chain: ref=%+v group=%d ok=%v err=%v", ref, group, ok, err)
	}
}

func TestStageHumanGate_GroupMatchesMergedChain(t *testing.T) {
	// The workflow prepends an automatic gate; the stage's own human_approval
	// (confirm: required) moves to group 1 of the merged chain, so the human
	// attempt's group index lines up with the chain the automatic gates ran in.
	cfg := workflowExitConfig(workflow.GateGroups{{codeGate(gatelib.GateVerdictMarker, nil)}})
	cfg.Stages[0].Confirm = "required"
	rs := &runState{p: &Pipeline{cfg: cfg}}
	ref, group, ok, err := rs.stageHumanGate(cfg.Stages[0])
	if err != nil || !ok || ref.Gate != gatelib.GateHumanApproval || group != 1 {
		t.Fatalf("the human gate must sit in the merged chain's group: ref=%+v group=%d ok=%v err=%v", ref, group, ok, err)
	}
}
