package web

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// The browser e2e stub in web/e2e/task-detail-flow.spec.js derives
// allowed_actions with the same rules for the same two templates. Keep the
// cases here and the stub's allowedActions() in sync: this test pins the
// server contract the stub imitates.
const allowedActionsManualTemplate = `schema_version: %d
stages:
  - {id: idea, title: Идея, function: product_owner, result: md, executor: human, skippable: true}
  - {id: analysis, title: Анализ, function: analyst, result: md, executor: agent, agent: analyst}
  - {id: qa, title: Проверка, function: qa, result: md, executor: human, skippable: true}
  - {id: delivery, title: План доставки, function: release_manager, result: approve, executor: human}
returns:
  - {from: qa, to: idea}
`

const allowedActionsAutoTemplate = `schema_version: %d
stages:
  - {id: build, title: Сборка, function: developer, result: md, executor: agent, agent: coder}
  - {id: verify, title: Проверка, function: qa, result: md, executor: agent, agent: tester}
`

func TestTaskAllowedActionsByPhaseMatchesBrowserContract(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", filepath.Join(target, ".ai-team", "artifacts"),
		WithTargetDir(target), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	templateStore, err := srv.templateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinDataForRun("run-manual", "run-manual", fmt.Appendf(nil, allowedActionsManualTemplate, config.CurrentSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	if _, err := templateStore.PinDataForRun("run-auto", "run-auto", fmt.Appendf(nil, allowedActionsAutoTemplate, config.CurrentSchemaVersion)); err != nil {
		t.Fatal(err)
	}

	pending := func(id, stage, trigger string, kind approval.Kind, actions ...string) *approval.PendingApproval {
		return &approval.PendingApproval{ID: id, FromStage: stage, ToStage: stage, Trigger: trigger,
			Kind: kind, Actions: actions, Status: approval.StatusPending}
	}
	checkDecision := func(id, stage, overrideTarget string) *approval.PendingApproval {
		value := pending(id, stage, approval.StageCheckTrigger, approval.KindCheckDecision, "new_version", "override", "return")
		value.Targets = map[string]string{"new_version": stage, "override": overrideTarget, "return": stage}
		return value
	}
	cases := []struct {
		name    string
		runID   string
		stage   string
		phase   lifecycle.Phase
		pending *approval.PendingApproval
		want    []string
	}{
		{"human stage waits for input", "run-manual", "idea", lifecycle.PhaseWaiting,
			pending("input-1", "idea", humanInputTrigger, approval.KindInput, "submit"), []string{"take", "results", "skip"}},
		{"agent stage paused before run", "run-manual", "analysis", lifecycle.PhaseResumable, nil, []string{"run-agent"}},
		{"agent stage waits for human input", "run-manual", "analysis", lifecycle.PhaseWaiting,
			pending("input-3", "analysis", humanInputTrigger, approval.KindInput, "submit"),
			[]string{"take", "results", "executor", "run-agent"}},
		{"agent stage is running", "run-manual", "analysis", lifecycle.PhaseRunning, nil, []string{}},
		{"agent asks grill questions", "run-manual", "analysis", lifecycle.PhaseWaiting,
			pending("grill-1", "analysis", approval.GrillTrigger, approval.KindGrillQuestions,
				approval.GrillActionAnswer, approval.GrillActionNewVersion, approval.GrillActionDone),
			[]string{"grill/answer", "grill/new-version", "grill/done"}},
		{"stage check awaits decision", "run-manual", "analysis", lifecycle.PhaseWaiting,
			pending("check-1", "analysis", approval.StageCheckTrigger, approval.KindApprove, "approve", "override_approve"),
			[]string{"confirm", "override"}},
		{"template stage check failed", "run-manual", "analysis", lifecycle.PhaseWaiting,
			checkDecision("check-2", "analysis", "analysis"), []string{"override"}},
		{"template stage check targets another stage", "run-manual", "analysis", lifecycle.PhaseWaiting,
			checkDecision("check-3", "analysis", "idea"), []string{}},
		{"returnable human stage", "run-manual", "qa", lifecycle.PhaseWaiting,
			pending("input-2", "qa", humanInputTrigger, approval.KindInput, "submit"), []string{"take", "results", "skip", "return"}},
		// While a transition confirmation waits, the run's next stage is the
		// destination; the return route and the confirmation belong to the stage
		// that was completed (from_stage), and a skippable completed stage is
		// not offered for skipping.
		{"transition confirmation names the completed stage", "run-manual", "delivery", lifecycle.PhaseWaiting,
			&approval.PendingApproval{ID: "gate-9", FromStage: "qa", ToStage: "delivery", Trigger: "stage_completed",
				Kind: approval.KindApprove, Actions: []string{"approve", "reject"}, Status: approval.StatusPending},
			[]string{"confirm", "return"}},
		{"delivery plan awaits approval", "run-manual", "delivery", lifecycle.PhaseWaiting,
			pending("plan-1", "delivery", "delivery_plan", approval.KindApprove, "approve", "reject"), []string{"plan/approve"}},
		{"terminal run", "run-manual", "", lifecycle.PhaseTerminal, nil, []string{}},
		{"auto first agent stage paused", "run-auto", "build", lifecycle.PhaseResumable, nil, []string{"run-agent"}},
		{"auto second agent stage paused", "run-auto", "verify", lifecycle.PhaseResumable, nil, []string{"run-agent"}},
		{"auto agent stage running", "run-auto", "build", lifecycle.PhaseRunning, nil, []string{}},
		{"auto agent stage waiting without approval", "run-auto", "build", lifecycle.PhaseWaiting, nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := lifecycle.State{RunID: tc.runID, Phase: tc.phase, NextStage: tc.stage}
			controller.approvals = nil
			if tc.pending != nil {
				value := *tc.pending
				value.RunID = tc.runID
				controller.approvals = []approval.PendingApproval{value}
				state.PendingApprovalID = value.ID
			}
			got := srv.taskAllowedActions(tc.runID, tc.stage, state)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("allowed actions = %#v, want %#v", got, tc.want)
			}
		})
	}
}
