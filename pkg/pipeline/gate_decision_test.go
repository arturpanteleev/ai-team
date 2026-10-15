package pipeline

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_decision_test.go — the LLM gate and the human gate in the common chain
// (S05): code gate, then LLM gate, then the person.

func reviewGate(params map[string]any) workflow.GateRef {
	return workflow.GateRef{Gate: gatelib.GateAgentReview, Version: 2, Params: params}
}

func humanGate(onReject string) workflow.GateRef {
	ref := workflow.GateRef{Gate: gatelib.GateHumanApproval, Version: 2}
	if onReject != "" {
		ref.Params = map[string]any{"on_reject": onReject}
	}
	return ref
}

// specChain is the product_spec shape: a code gate, an LLM gate, the person.
func specChain(onReject string) workflow.GateGroups {
	return workflow.GateGroups{
		{codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}})},
		{reviewGate(map[string]any{"agent": "spec-checker", "verdict_schema": "json", "max_attempts": 2})},
		{humanGate(onReject)},
	}
}

// humanChainConfig is stage "tech" with the given chain followed by stage
// "next", so the stage's forward transition is a real decision.
func humanChainConfig(groups workflow.GateGroups) *config.Config {
	cfg := waveConfig(groups)
	cfg.Stages = append(cfg.Stages, config.TemplateStage{
		ID: "next", Title: "Следующий", Function: "architect", Result: "md", Executor: "agent", Agent: "verifier", Confirm: "auto",
	})
	return cfg
}

const goodSpec = "# Спецификация\n\n## Критерии приёмки\n\n- конверсия растёт на 5%\n"

func humanGateAttempts(t *testing.T, dir string) []workflow.GateAttempt {
	t.Helper()
	history, err := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	if err != nil {
		t.Fatal(err)
	}
	var out []workflow.GateAttempt
	for _, attempt := range history {
		if attempt.Gate == gatelib.GateHumanApproval {
			out = append(out, attempt)
		}
	}
	return out
}

func pendingTransition(t *testing.T, store *approval.Store, runID string) approval.PendingApproval {
	t.Helper()
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Kind == approval.KindApprove && value.Status == approval.StatusPending && value.FromStage == "tech" {
			return value
		}
	}
	t.Fatalf("no pending transition approval of tech in %+v", values)
	return approval.PendingApproval{}
}

func decide(t *testing.T, store *approval.Store, runID string, pending approval.PendingApproval, action, comment string) {
	t.Helper()
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "po-1", ActorRole: pending.RequiredRoles[0], Action: action,
		SubjectHash: pending.SubjectHash, Comment: comment,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLLMGate_InvalidVerdictIsErrorNeverPass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		answer string
		calls  int
	}{
		{"marker schema, no marker", map[string]any{"agent": "verifier"}, "# Отчёт\n\nвсё хорошо, но вердикта нет\n", 1},
		{"json schema, prose", map[string]any{"agent": "verifier", "verdict_schema": "json"}, "# Отчёт\n\nвсё хорошо\n", 1},
		{"json schema, spent attempts", map[string]any{"agent": "verifier", "verdict_schema": "json", "max_attempts": 3}, "# Отчёт\n\n{\"verdict\":\"maybe\"}\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := env(t)
			rt := newScripted()
			rt.content["tech"] = map[string]string{"result": goodSpec}
			rt.content["verifier"] = map[string]string{"verification": tc.answer}
			cfg := waveConfig(workflow.GateGroups{{reviewGate(tc.params)}})
			if err := runWave(t, dir, cfg, rt); !errors.Is(err, ErrUserStopped) {
				t.Fatalf("an answer outside the schema must block the transition: %v", err)
			}
			verifierCalls := 0
			for _, name := range rt.executed {
				if name == "verifier" {
					verifierCalls++
				}
			}
			if verifierCalls != tc.calls {
				t.Fatalf("reviewer called %d times, want %d (max_attempts)", verifierCalls, tc.calls)
			}
			history, _ := gatesched.AllAttempts(gateEvents(t, dir), "tech")
			if len(history) != 1 {
				t.Fatalf("history = %+v", history)
			}
			attempt := history[0]
			if attempt.Execution != workflow.GateExecutionError || attempt.Verdict != "" {
				t.Fatalf("an invalid answer must end execution=error without a verdict: %+v", attempt)
			}
			if !strings.Contains(events(t, dir), `"type":"check_error"`) {
				t.Fatal("the technical failure must be a check_error")
			}
		})
	}
}

func events(t *testing.T, dir string) string { return readEvents(t, dir) }

func TestLLMGate_RefusalOfRuntimeIsErrorAndRetried(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	rt.execErr["verifier"] = errors.New("model unavailable")
	cfg := waveConfig(workflow.GateGroups{{reviewGate(map[string]any{"agent": "verifier", "max_attempts": 2})}})
	if err := runWave(t, dir, cfg, rt); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a refusing runtime must block the transition: %v", err)
	}
	history, _ := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	if len(history) != 1 || history[0].Execution != workflow.GateExecutionError || !strings.Contains(history[0].Reason, "попытка 2/2") {
		t.Fatalf("history = %+v", history)
	}
}

func TestLLMGate_RetryRecoversFromBadAnswerAndJSONVerdictsAreHonored(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	rt.contentFn["verifier"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"verification": "не знаю"}
		}
		return map[string]string{"verification": "```json\n{\"verdict\":\"pass\",\"reason\":\"критерии измеримы\"}\n```\n"}
	}
	cfg := waveConfig(workflow.GateGroups{{reviewGate(map[string]any{"agent": "verifier", "verdict_schema": "json", "max_attempts": 2})}})
	if err := runWave(t, dir, cfg, rt); err != nil {
		t.Fatalf("a valid verdict on the second attempt must pass: %v", err)
	}

	// A valid negative verdict is a fail (execution succeeded), not an error,
	// and it is not retried.
	dir = env(t)
	rt = newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	rt.content["verifier"] = map[string]string{"verification": "{\"verdict\":\"fail\",\"reason\":\"критерии не измеримы\"}"}
	if err := runWave(t, dir, cfg, rt); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a negative verdict must block: %v", err)
	}
	history, _ := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	if len(history) != 1 || history[0].Execution != workflow.GateExecutionSucceeded || history[0].Verdict != workflow.GateVerdictFail ||
		!strings.Contains(history[0].Reason, "критерии не измеримы") {
		t.Fatalf("history = %+v", history)
	}
	calls := 0
	for _, name := range rt.executed {
		if name == "verifier" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("an opinion of the reviewer must not be retried: %d calls", calls)
	}
}

func TestHumanGate_AskedOnlyAfterAutomaticGatesAndRejectKeepsTaskAtStage(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	rt.content["spec-checker"] = map[string]string{"spec-check": "{\"verdict\":\"pass\"}"}
	rt.content["verifier"] = map[string]string{"verification": "ok"}
	cfg := humanChainConfig(specChain(gatelib.OnRejectStay))
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)

	// 1. code gate -> LLM gate -> the person is asked.
	if err := runGrillPipeline(t, p, dir, ""); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("the person must be asked: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	evs := gateEvents(t, dir)
	reviewerDone := indexOfEvent(evs, gatesched.EventAttemptFinished, gatelib.GateAgentReview)
	humanWaiting := indexOfEvent(evs, gatesched.EventAttemptWaiting, gatelib.GateHumanApproval)
	if reviewerDone < 0 || humanWaiting < 0 || humanWaiting < reviewerDone {
		t.Fatalf("the person must be asked after the LLM gate finished (finished at %d, human waiting at %d)", reviewerDone, humanWaiting)
	}
	pending := pendingTransition(t, store, runID)
	if !containsString(pending.Actions, "reject") || pending.Targets["reject"] != "tech" {
		t.Fatalf("on_reject: stay must keep the task at the stage: %+v", pending.Targets)
	}
	if attempts := humanGateAttempts(t, dir); len(attempts) != 1 || attempts[0].Execution != workflow.GateExecutionPending ||
		attempts[0].Executor != workflow.GateKindHuman || attempts[0].ArtifactSHA256 == "" {
		t.Fatalf("a waiting human gate must be one pending attempt bound to the result hash: %+v", attempts)
	}

	// 2. The person rejects: the task stays at the stage, the refusal is a
	// durable event, and the new version repeats the whole chain.
	decide(t, store, runID, pending, "reject", "нужны метрики по сегментам")
	if err := runGrillPipeline(t, p, dir, runID); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("after the refusal the task must wait for the new version at the stage: %v", err)
	}
	state, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	// The lifecycle waits on the forward transition of the stage; the stage
	// itself ran again (the rework) and the person is asked about its result.
	techRuns := 0
	for _, name := range rt.executed {
		if name == "tech" {
			techRuns++
		}
	}
	if loaded.Phase != lifecycle.PhaseWaiting || techRuns != 2 || pendingTransition(t, store, runID).AttemptID == pending.AttemptID {
		t.Fatalf("the task must stay at tech, rerun it and wait for a decision on the new attempt (runs=%d): %+v", techRuns, loaded)
	}
	attempts := humanGateAttempts(t, dir)
	if len(attempts) != 2 {
		t.Fatalf("want the refused attempt and the new waiting one: %+v", attempts)
	}
	refused, again := attempts[0], attempts[1]
	if refused.Execution != workflow.GateExecutionSucceeded || refused.Verdict != workflow.GateVerdictFail ||
		!strings.Contains(refused.Reason, "reject") || !strings.Contains(refused.Reason, "po-1") || !strings.Contains(refused.Reason, "нужны метрики") {
		t.Fatalf("the refusal must be recorded with action, author and comment: %+v", refused)
	}
	if again.Execution != workflow.GateExecutionPending {
		t.Fatalf("the new version must ask the person again: %+v", again)
	}
	if got := countEvents(gateEvents(t, dir), gatesched.EventAttemptStarted, gatelib.GateRequiredSections); got != 2 {
		t.Fatalf("the new version must repeat the whole chain from the first group: required_sections started %d times", got)
	}
	if got := countEvents(gateEvents(t, dir), gatesched.EventAttemptStarted, gatelib.GateAgentReview); got != 2 {
		t.Fatalf("the LLM gate must run again for the new version: %d", got)
	}

	// 3. The second request is approved: the task moves on.
	decide(t, store, runID, pendingTransition(t, store, runID), "approve", "")
	if err := runGrillPipeline(t, p, dir, runID); err != nil {
		t.Fatalf("approval must let the task through: %v", err)
	}
	attempts = humanGateAttempts(t, dir)
	last := attempts[len(attempts)-1]
	if last.Execution != workflow.GateExecutionSucceeded || last.Verdict != workflow.GateVerdictPass {
		t.Fatalf("approval must finish the human gate with pass: %+v", last)
	}
	if !strings.Contains(strings.Join(rt.executed, ","), "verifier") {
		t.Fatalf("the next stage must run after approval: %v", rt.executed)
	}
}

func TestHumanGate_NotAskedWhenAutomaticGateFailed(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Спецификация\n\nбез критериев\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "{\"verdict\":\"pass\"}"}
	cfg := humanChainConfig(specChain(""))
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)
	if err := runGrillPipeline(t, p, dir, ""); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("the failed code gate must block: %v", err)
	}
	if got := countEvents(gateEvents(t, dir), gatesched.EventAttemptStarted, gatelib.GateAgentReview); got != 0 {
		t.Fatal("the LLM gate must not start after a failed code gate")
	}
	if attempts := humanGateAttempts(t, dir); len(attempts) != 0 {
		t.Fatalf("a person must not be asked about a result the machines rejected: %+v", attempts)
	}
	values, err := store.List(filepath.Base(onlyRunDir(t, dir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Kind == approval.KindApprove {
			t.Fatalf("no transition approval may exist before the automatic groups end: %+v", value)
		}
	}
}

func TestHumanGate_DefaultRejectStopsTheRun(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	rt.content["spec-checker"] = map[string]string{"spec-check": "{\"verdict\":\"pass\"}"}
	cfg := humanChainConfig(specChain(""))
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)
	if err := runGrillPipeline(t, p, dir, ""); !errors.Is(err, ErrUserStopped) {
		t.Fatal(err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := pendingTransition(t, store, runID)
	if pending.Targets["reject"] != workflow.TerminalStop {
		t.Fatalf("version 1 semantics: reject ends the run, got %v", pending.Targets)
	}
	decide(t, store, runID, pending, "reject", "не согласен")
	if err := runGrillPipeline(t, p, dir, runID); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("reject must stop the run: %v", err)
	}
	attempts := humanGateAttempts(t, dir)
	if len(attempts) != 1 || attempts[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("the refusal must be recorded as a failed human gate: %+v", attempts)
	}
}

// Reject-stay is a decision of a person, not a loopback: several refusals in a
// row, each decided while the process was stopped (Web/REST) and resumed, must
// not consume the max_visits allowance of the stage.
func TestHumanGate_RepeatedRejectStayThroughResumeKeepsVisitAllowance(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodSpec}
	rt.content["spec-checker"] = map[string]string{"spec-check": "{\"verdict\":\"pass\"}"}
	rt.content["verifier"] = map[string]string{"verification": "ok"}
	cfg := humanChainConfig(specChain(gatelib.OnRejectStay))
	cfg.Returns = []config.TemplateReturn{{From: "next", To: "tech", MaxVisits: 2}}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)
	if err := runGrillPipeline(t, p, dir, ""); !errors.Is(err, ErrUserStopped) {
		t.Fatalf("the person must be asked: %v", err)
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	for round := 1; round <= 3; round++ {
		decide(t, store, runID, pendingTransition(t, store, runID), "reject", "доработать")
		if err := runGrillPipeline(t, p, dir, runID); !errors.Is(err, ErrUserStopped) {
			t.Fatalf("reject #%d through resume must keep the task at the stage waiting: %v", round, err)
		}
	}
	decide(t, store, runID, pendingTransition(t, store, runID), "approve", "")
	// The task moves on to the next stage; it then waits for the person's
	// decision of the final transition, but never fails on max_visits.
	err = runGrillPipeline(t, p, dir, runID)
	if err != nil && (strings.Contains(err.Error(), "max_visits") || !errors.Is(err, ErrUserStopped)) {
		t.Fatalf("approval after several refusals must pass the stage: %v", err)
	}
	if !strings.Contains(strings.Join(rt.executed, ","), "verifier") {
		t.Fatalf("the next stage must run after approval: %v", rt.executed)
	}
}
