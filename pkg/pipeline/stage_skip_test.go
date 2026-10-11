package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestRunEngineSkipSkippableAgentStageAndContinue(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	var cancelPolls atomic.Int32
	n := &captureNotifier{}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "skip-agent-test",
		Title:         "Skip agent test",
		Stages: []config.TemplateStage{
			{ID: "setup", Title: "Setup", Function: "operator", Result: "md", Executor: "agent", Agent: "questioner", Confirm: "auto"},
			{ID: "optional", Title: "Optional", Function: "developer", Result: "md", Executor: "agent", Agent: "analyst", Confirm: "auto", Skippable: true},
			{ID: "finish", Title: "Finish", Function: "operator", Result: "md", Executor: "agent", Agent: "questioner", Confirm: "auto"},
		},
	}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithNotifier(n))
	engine := NewRunEngine(p)
	started, startErr := engine.Start(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "exercise an agent-stage skip", TargetDir: dir,
		CancelRequested: func() bool { return cancelPolls.Add(1) >= 3 },
	})
	if !errors.Is(startErr, context.Canceled) || started.RunID == "" {
		t.Fatalf("start should stop at the optional stage boundary: result=%+v err=%v", started, startErr)
	}
	lifecycleStore, err := lifecycle.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := lifecycleStore.Load(started.RunID)
	if err != nil || checkpoint.Phase != lifecycle.PhaseResumable || checkpoint.NextStage != "optional" {
		t.Fatalf("run should be resumable at optional stage: state=%+v err=%v", checkpoint, err)
	}
	if _, err := engine.SkipStage(context.Background(), SkipStageConfig{
		RunID: started.RunID, TargetDir: dir, StageID: "finish", Reason: "Try to skip a non-skippable stage.",
	}); err == nil || !strings.Contains(err.Error(), "not configured as skippable") {
		t.Fatalf("non-skippable stage must be rejected: %v", err)
	}

	result, err := engine.SkipStage(context.Background(), SkipStageConfig{
		RunID: started.RunID, TargetDir: dir, StageID: "optional", Reason: "The optional review is out of scope for this run.",
	})
	if err != nil || result.Outcome != workflow.RunCompleted {
		t.Fatalf("skip should follow OutcomeSkipped to finish: result=%+v err=%v", result, err)
	}
	if rt.calls["questioner"] != 2 || rt.calls["analyst"] != 0 {
		t.Fatalf("the skipped agent must not execute and the next stage must run: calls=%+v", rt.calls)
	}

	runDir := filepath.Join(dir, ".ai-team", "runs", started.RunID)
	replayed, err := evidence.ReplayEventLog(filepath.Join(runDir, "events.jsonl"), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.StageSkips) != 1 || replayed.StageSkips[0].Stage != "optional" ||
		replayed.StageSkips[0].Reason != "The optional review is out of scope for this run." {
		t.Fatalf("expected one durable skip reason, got %+v", replayed.StageSkips)
	}
	var skippedAttempt *evidence.ReplayedAttempt
	for i := range replayed.Attempts {
		if replayed.Attempts[i].AttemptID == replayed.StageSkips[0].AttemptID {
			skippedAttempt = &replayed.Attempts[i]
		}
	}
	if skippedAttempt == nil || skippedAttempt.Executor != "agent" || skippedAttempt.State.Outcome != workflow.OutcomeSkipped {
		t.Fatalf("skip must be represented by a zero-usage synthetic attempt: %+v", skippedAttempt)
	}
	_, manifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), runDir, started.RunID, skippedAttempt.AttemptID)
	if err != nil || manifest.Usage == nil || !manifest.Usage.Attested || manifest.Usage.TokensInput != 0 || manifest.Usage.TokensOutput != 0 {
		t.Fatalf("skip manifest must attest zero model usage: manifest=%+v err=%v", manifest, err)
	}
	var skippedTransition, warning bool
	for _, transition := range replayed.Transitions {
		if transition.AttemptID == skippedAttempt.AttemptID {
			skippedTransition = transition.Outcome == string(workflow.OutcomeSkipped) && transition.EdgeTarget == "finish"
		}
	}
	events, err := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "stage_skipped" && event.AttemptID == skippedAttempt.AttemptID {
			reason, reasonOK := event.Data["reason"].(string)
			warning = event.Data["warning"] == true && reasonOK && strings.TrimSpace(reason) != ""
		}
	}
	if !skippedTransition || !warning {
		t.Fatalf("skip evidence must warn and follow the graph's skipped edge: transition=%t warning=%t", skippedTransition, warning)
	}
	if len(n.calls) != 3 || n.calls[1].Name != "optional" || n.calls[1].State.Outcome != workflow.OutcomeSkipped {
		t.Fatalf("notifier should receive the synthetic skipped attempt: %+v", n.calls)
	}
}

func TestAgentSkipClearsStaleOutputsAndRecoversMissingWarning(t *testing.T) {
	dir := env(t)
	staleOutput := filepath.Join(dir, ".ai-team", "artifacts", "feat", "proposal.md")
	staleSummary := filepath.Join(dir, ".ai-team", "artifacts", "feat", ".stage-summary", "optional.md")
	for path, content := range map[string]string{
		staleOutput:  "stale proposal from an earlier visit",
		staleSummary: "stale summary from an earlier visit",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	rt := newScripted()
	factory := &humanCrashEvidenceFactory{point: "after-skipped-attempt-finished"}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "agent-skip-crash-recovery-test",
		Title:         "Agent skip crash recovery test",
		Stages: []config.TemplateStage{
			{ID: "optional", Title: "Optional", Function: "developer", Result: "md", Executor: "agent", Agent: "analyst", Confirm: "auto", Skippable: true},
			{ID: "finish", Title: "Finish", Function: "developer", Result: "md", Executor: "agent", Agent: "coder", Confirm: "auto"},
		},
	}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithEvidenceStoreFactory(factory),
		WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	engine := NewRunEngine(p)
	started, startErr := engine.Start(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "skip an optional stage", TargetDir: dir,
		CancelRequested: func() bool { return true },
	})
	if !errors.Is(startErr, context.Canceled) || started.RunID == "" {
		t.Fatalf("run should be resumable before its first stage: result=%+v err=%v", started, startErr)
	}
	const reason = "This optional proposal is outside the task scope."
	expectHumanCrash(t, func() {
		_, _ = engine.SkipStage(context.Background(), SkipStageConfig{
			RunID: started.RunID, TargetDir: dir, StageID: "optional", Reason: reason,
		})
	})
	if !factory.crashed {
		t.Fatal("crash was not injected after durable skipped attempt_finished")
	}
	for _, path := range []string{staleOutput, staleSummary} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("skipping agent stage left stale artifact %s: %v", path, err)
		}
	}
	eventsPath := filepath.Join(dir, ".ai-team", "runs", started.RunID, "events.jsonl")
	if _, err := evidence.ReplayEventLog(eventsPath, started.RunID); err == nil || !strings.Contains(err.Error(), "no stage_skipped warning") {
		t.Fatalf("strict replay must reject a finished skipped attempt without its warning: %v", err)
	}
	_, resumeErr := engine.SkipStage(context.Background(), SkipStageConfig{
		RunID: started.RunID, TargetDir: dir, StageID: "optional", Reason: reason,
	})
	if resumeErr == nil || !strings.Contains(resumeErr.Error(), "proposal") {
		t.Fatalf("downstream collection should fail after the stale proposal is removed: %v", resumeErr)
	}
	if rt.calls["analyst"] != 0 || rt.calls["coder"] != 0 {
		t.Fatalf("recovery must reuse the skipped attempt and downstream must not consume stale bytes: calls=%+v", rt.calls)
	}
	replayed, err := evidence.ReplayEventLog(eventsPath, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var skippedAttempts int
	for _, attempt := range replayed.Attempts {
		if attempt.Stage == "optional" && attempt.State.Outcome == workflow.OutcomeSkipped {
			skippedAttempts++
			if attempt.SkipReason != reason {
				t.Fatalf("finished agent skip lost its reason: %+v", attempt)
			}
		}
	}
	if skippedAttempts != 1 || len(replayed.StageSkips) != 1 || replayed.StageSkips[0].Reason != reason {
		t.Fatalf("recovery should append one reason-bound warning without a new attempt: attempts=%+v skips=%+v", replayed.Attempts, replayed.StageSkips)
	}
}

func TestRunEngineSkipPendingHumanStageRequiresReasonAndLeavesNoOutput(t *testing.T) {
	dir := env(t)
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "skip-human-test",
		Title:         "Skip human test",
		Stages: []config.TemplateStage{{
			ID: "optional", Title: "Optional input", Function: "product_owner", Result: "md",
			Executor: "human", Confirm: "auto", Skippable: true,
		}},
	}
	p := New(cfg, testRegistry(), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	engine := NewRunEngine(p)
	started, startErr := engine.Start(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "exercise a human-stage skip", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(startErr, &required) || started.RunID == "" {
		t.Fatalf("human stage should pause for typed input: result=%+v err=%v", started, startErr)
	}
	if _, err := engine.SkipStage(context.Background(), SkipStageConfig{
		RunID: started.RunID, TargetDir: dir, StageID: "optional", Reason: "   ",
	}); err == nil || !strings.Contains(err.Error(), "причину") {
		t.Fatalf("blank skip reason must be rejected: %v", err)
	}

	result, err := engine.SkipStage(context.Background(), SkipStageConfig{
		RunID: started.RunID, TargetDir: dir, StageID: "optional", Reason: "No document is needed for this task.",
	})
	if err != nil || result.Outcome != workflow.RunCompleted {
		t.Fatalf("human input skip should complete through OutcomeSkipped: result=%+v err=%v", result, err)
	}
	outputPath := filepath.Join(dir, ".ai-team", "artifacts", "feat", "optional.md")
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skipping human input must not synthesize a result artifact: stat err=%v", err)
	}
	store, storeErr := approval.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	input, err := store.Load(started.RunID, required.ApprovalID)
	if err != nil || input.Status != approval.StatusResolved || input.ResolvedAction != "skip" {
		t.Fatalf("human input approval must retain the skip decision: approval=%+v err=%v", input, err)
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", started.RunID, "events.jsonl"), started.RunID)
	if err != nil || len(replayed.StageSkips) != 1 || replayed.StageSkips[0].Stage != "optional" {
		t.Fatalf("human stage skip must replay from evidence: skips=%+v err=%v", replayed.StageSkips, err)
	}
}

func TestReplayedStageSkipTransitionRequiresMatchingReasonAndEdge(t *testing.T) {
	graph := workflow.Graph{Edges: []workflow.Edge{{
		From: "optional", Outcome: workflow.OutcomeSkipped, To: "finish",
	}}}
	replayed := evidence.ReplayedRun{
		StageSkips: []evidence.ReplayedStageSkip{{AttemptID: "attempt-skip", Stage: "optional", Reason: "Not needed."}},
		Transitions: []evidence.ReplayedTransition{{
			AttemptID: "attempt-skip", From: "optional", Outcome: string(workflow.OutcomeSkipped), EdgeTarget: "finish",
		}},
	}
	if !replayedStageSkipTransition(replayed, graph, "optional", "Not needed.") {
		t.Fatal("a persisted skip and its exact graph transition should reconcile after a crash")
	}
	if replayedStageSkipTransition(replayed, graph, "optional", "Different reason") {
		t.Fatal("a retry with a different reason must not claim the persisted skip")
	}
	replayed.Transitions[0].EdgeTarget = "other"
	if replayedStageSkipTransition(replayed, graph, "optional", "Not needed.") {
		t.Fatal("a transition to a different edge target must not reconcile")
	}
}

func TestTemplateReturnRequiresReasonReexecutesIntermediateStagesAndDeliversFeedback(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.contentFn["reviewer"] = func(call int) map[string]string {
		verdict := "APPROVED"
		if call == 1 {
			verdict = "CHANGES_REQUESTED"
		}
		return map[string]string{"review": "**Verdict:** " + verdict + "\n"}
	}
	var targetFeedback string
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name != "analyst" || rt.calls[name] < 2 {
			return
		}
		for _, input := range inputs {
			if input.Name != "human-return-feedback" {
				continue
			}
			data, err := os.ReadFile(input.Path)
			if err != nil {
				t.Errorf("read returned stage feedback: %v", err)
				return
			}
			targetFeedback = string(data)
		}
	}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "return-feedback-test",
		Title:         "Return feedback test",
		Stages: []config.TemplateStage{
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "md", Executor: "agent", Agent: "analyst", Confirm: "auto"},
			{ID: "code_review", Title: "Code review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "questioner", Confirm: "auto"},
			{ID: "qa", Title: "QA", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer", Confirm: "auto"},
		},
		Returns: []config.TemplateReturn{{From: "qa", To: "implementation", MaxVisits: 2}},
	}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	engine := NewRunEngine(p)
	started, startErr := engine.Start(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "rework the implementation after QA", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(startErr, &required) || started.RunID == "" {
		t.Fatalf("QA return must pause for a durable decision: result=%+v err=%v", started, startErr)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(started.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(pending.Actions, "return_to_implementation") || pending.Targets["return_to_implementation"] != "implementation" {
		t.Fatalf("only the configured return target should be offered: %+v", pending)
	}
	const reason = "Update the implementation to handle the empty response."
	if _, err := store.Decide(started.RunID, pending.ID, approval.Decision{
		ActorID: "qa-operator", ActorRole: "reviewer", Action: "return_to_implementation",
		SubjectHash: pending.SubjectHash,
	}); err == nil {
		t.Fatal("return without a reason must be rejected")
	}
	if _, err := store.Decide(started.RunID, pending.ID, approval.Decision{
		ActorID: "qa-operator", ActorRole: "reviewer", Action: "return_to_implementation",
		Comment: reason, SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Resume(context.Background(), ResumeConfig{RunID: started.RunID, TargetDir: dir})
	var finalGate *ApprovalRequiredError
	if !errors.As(err, &finalGate) {
		t.Fatalf("configured returns require an explicit choice even when confirm:auto: result=%+v err=%v", result, err)
	}
	finalDecision, loadErr := store.Load(started.RunID, finalGate.ApprovalID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !containsString(finalDecision.Actions, "return_to_implementation") {
		t.Fatalf("configured return must remain available after an auto-confirm pass: %+v", finalDecision.Actions)
	}
	if _, decideErr := store.Decide(started.RunID, finalDecision.ID, approval.Decision{
		ActorID: "qa-operator", ActorRole: "reviewer", Action: "approve",
		Comment: "QA accepted the revised implementation.", SubjectHash: finalDecision.SubjectHash,
	}); decideErr != nil {
		t.Fatal(decideErr)
	}
	result, err = engine.Resume(context.Background(), ResumeConfig{RunID: started.RunID, TargetDir: dir})
	if err != nil || (result.Outcome != workflow.RunCompleted && result.Outcome != workflow.RunCompletedWithWarnings) {
		t.Fatalf("return should replay target and intermediate stages: result=%+v err=%v", result, err)
	}
	if rt.calls["analyst"] != 2 || rt.calls["questioner"] != 2 || rt.calls["reviewer"] != 2 {
		t.Fatalf("return must rerun implementation, code_review, and qa: calls=%+v", rt.calls)
	}
	if !strings.Contains(targetFeedback, reason) {
		t.Fatalf("return reason was not delivered to the target executor: %q", targetFeedback)
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", started.RunID, "events.jsonl"), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var returned bool
	for _, transition := range replayed.Transitions {
		if transition.From == "qa" && transition.Outcome == string(workflow.OutcomeRejected) {
			returned = transition.Action == "return_to_implementation" && transition.Target == "implementation"
		}
	}
	if !returned {
		t.Fatalf("evidence must retain the exact configured return transition: %+v", replayed.Transitions)
	}
}

func TestTemplateReturnHonorsMaxVisitsBeforeStartingTarget(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.contentFn["reviewer"] = func(int) map[string]string {
		return map[string]string{"review": "**Verdict:** CHANGES_REQUESTED\n"}
	}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "return-max-visits-test",
		Title:         "Return max visits test",
		Stages: []config.TemplateStage{
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "md", Executor: "agent", Agent: "analyst", Confirm: "auto"},
			{ID: "code_review", Title: "Code review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "questioner", Confirm: "auto"},
			{ID: "qa", Title: "QA", Function: "reviewer", Result: "md", Executor: "agent", Agent: "reviewer", Confirm: "auto"},
		},
		Returns: []config.TemplateReturn{{From: "qa", To: "implementation", MaxVisits: 2}},
	}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	engine := NewRunEngine(p)
	started, err := engine.Start(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "exercise max_visits", TargetDir: dir,
	})
	if started.RunID == "" {
		t.Fatalf("start failed before creating a run: %+v %v", started, err)
	}
	store, storeErr := approval.NewStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	for iteration := 1; iteration <= 2; iteration++ {
		var required *ApprovalRequiredError
		if !errors.As(err, &required) {
			t.Fatalf("return %d should pause for its configured approval: result=%+v err=%v", iteration, started, err)
		}
		pending, loadErr := store.Load(started.RunID, required.ApprovalID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		reason := []string{"Rework the implementation after QA pass one.", "Rework the implementation after QA pass two."}[iteration-1]
		if _, decideErr := store.Decide(started.RunID, pending.ID, approval.Decision{
			ActorID: "qa-operator", ActorRole: "reviewer", Action: "return_to_implementation",
			Comment: reason, SubjectHash: pending.SubjectHash,
		}); decideErr != nil {
			t.Fatal(decideErr)
		}
		started, err = engine.Resume(context.Background(), ResumeConfig{RunID: started.RunID, TargetDir: dir})
	}
	if err == nil || !strings.Contains(err.Error(), "max_visits=2") {
		t.Fatalf("third implementation visit must fail before executing: result=%+v err=%v", started, err)
	}
	if rt.calls["analyst"] != 2 || rt.calls["questioner"] != 2 || rt.calls["reviewer"] != 2 {
		t.Fatalf("visit limit must block the third target before another intermediate cycle: calls=%+v", rt.calls)
	}
}
