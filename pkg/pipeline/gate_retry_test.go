package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_retry_test.go — advisory gates, technical retries and the evidence of
// administrator overrides (S06).

const goodTechResult = "# Решение\n\n## Критерии приёмки\n\nвсё ок\n\n**Verdict:** APPROVED\n"

func codeGatesThenVerifier(verifier workflow.GateRef) workflow.GateGroups {
	return workflow.GateGroups{
		{
			codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}}),
			codeGate(gatelib.GateVerdictMarker, nil),
		},
		{verifier},
	}
}

func verifierGate() workflow.GateRef {
	return codeGate(gatelib.GateAgentReview, map[string]any{"agent": "verifier"})
}

// brokenReviewer returns a report without any verdict: a technical failure of
// the LLM gate (error), never a verdict.
func brokenReviewer(int) map[string]string {
	return map[string]string{"verification": "# Отчёт\n\nВердикт не вынесен.\n"}
}

func passingReviewer(int) map[string]string {
	return map[string]string{"verification": "# Отчёт\n\n**Verdict:** PASSED\n"}
}

func resumeWave(t *testing.T, dir string, rt *scriptedRuntime, cfg *config.Config, runID string) error {
	t.Helper()
	p := New(cfg, stageCheckRegistry(true),
		WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	return p.Run(context.Background(), RunConfig{Feature: "", TargetDir: dir, ResumeRunID: runID})
}

func decideCheck(t *testing.T, dir string, value *approval.PendingApproval, decision approval.Decision) {
	t.Helper()
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	decision.SubjectHash = value.SubjectHash
	decision.DecidedAt = time.Now().UTC()
	if decision.ActorID == "" {
		decision.ActorID = "operator"
	}
	if decision.ActorRole == "" {
		decision.ActorRole = "architect"
	}
	// What the authenticated controller sets: retry and override come only
	// through it; the CLI path (without these fields) is refused by the store.
	if decision.Action == approval.ActionRetryGate || decision.Action == "override" {
		decision.ControllerAuthenticated = true
	}
	if _, err := store.Decide(value.RunID, value.ID, decision); err != nil {
		t.Fatalf("decide %s: %v", decision.Action, err)
	}
}

func checkDecisionPayload(t *testing.T, value *approval.PendingApproval) approval.CheckDecisionPayload {
	t.Helper()
	var payload approval.CheckDecisionPayload
	if err := json.Unmarshal(value.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func eventsOfType(events []evidence.Event, eventType string) []evidence.Event {
	var out []evidence.Event
	for _, event := range events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

// Acceptance: a retry of a technical error does not repeat the successful gates
// of the earlier group, keeps the earlier attempt and runs on the same version.
func TestGateRetry_OnlyTheFailedGateRunsAgainOnTheSameVersion(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodTechResult}
	rt.contentFn["verifier"] = func(call int) map[string]string {
		if call == 1 {
			return brokenReviewer(call)
		}
		return passingReviewer(call)
	}
	cfg := waveConfig(codeGatesThenVerifier(verifierGate()))
	if err := runWave(t, dir, cfg, rt); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a technical error of a required gate must block: %v", err)
	}
	decision := findCheckDecision(t, dir, approval.StageCheckTrigger)
	payload := checkDecisionPayload(t, decision)
	if payload.Status != "error" || len(payload.RetryableGates) != 1 || payload.RetryableGates[0] != gatelib.GateAgentReview || payload.ArtifactSHA256 == "" {
		t.Fatalf("the decision must name the retryable gate and the version: %+v", payload)
	}
	if !containsString(decision.Actions, approval.ActionRetryGate) {
		t.Fatalf("retry_gate must be offered after a technical error: %v", decision.Actions)
	}
	shaBefore := payload.ArtifactSHA256

	decideCheck(t, dir, decision, approval.Decision{Action: approval.ActionRetryGate, Gates: []string{gatelib.GateAgentReview}})
	runID := filepath.Base(onlyRunDir(t, dir))
	if err := resumeWave(t, dir, rt, cfg, runID); err != nil {
		t.Fatalf("the retried chain must pass: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech,verifier,verifier" {
		t.Fatalf("the stage must not run again, only the failed gate: %v", rt.executed)
	}
	events := gateEvents(t, dir)
	for _, gate := range []string{gatelib.GateRequiredSections, gatelib.GateVerdictMarker} {
		if got := countEvents(events, gatesched.EventAttemptStarted, gate); got != 1 {
			t.Fatalf("the passed gate %s must not be repeated, started %d times", gate, got)
		}
	}
	if countEvents(events, gatesched.EventAttemptStarted, gatelib.GateAgentReview) != 2 ||
		countEvents(events, gatesched.EventAttemptFinished, gatelib.GateAgentReview) != 2 {
		t.Fatal("both attempts of the retried gate must be recorded")
	}
	history, err := gatesched.AllAttempts(events, "tech")
	if err != nil {
		t.Fatal(err)
	}
	var reviewer []workflow.GateAttempt
	for _, attempt := range history {
		if attempt.GateName == gatelib.GateAgentReview {
			reviewer = append(reviewer, attempt)
		}
	}
	if len(reviewer) != 2 || reviewer[0].Execution != workflow.GateExecutionError || reviewer[0].Verdict != "" ||
		reviewer[1].Execution != workflow.GateExecutionSucceeded || reviewer[1].Verdict != workflow.GateVerdictPass ||
		reviewer[0].AttemptID == reviewer[1].AttemptID || reviewer[0].ArtifactSHA256 != reviewer[1].ArtifactSHA256 || reviewer[0].ArtifactSHA256 != shaBefore {
		t.Fatalf("the error attempt stays, the retry is a new attempt on the same hash: %+v", reviewer)
	}
	requests := eventsOfType(events, gatesched.EventGateRetryRequested)
	if len(requests) != 1 || requests[0].Data["artifact_sha256"] != shaBefore || requests[0].Data["actor_id"] != "operator" {
		t.Fatalf("retry request evidence: %+v", requests)
	}
	if countEvent(readEvents(t, dir), "check_passed") != 1 {
		t.Fatal("the retried chain must end in a passed check")
	}
}

// A retry that fails technically again raises its own decision, even though the
// report is byte-identical; the administrator can then override it. The error
// attempt stays an error in every record.
func TestGateRetry_RepeatedErrorRaisesANewDecisionAndOverrideKeepsItRed(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodTechResult}
	rt.contentFn["verifier"] = brokenReviewer
	cfg := waveConfig(codeGatesThenVerifier(verifierGate()))
	if err := runWave(t, dir, cfg, rt); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("first run must block: %v", err)
	}
	first := findCheckDecision(t, dir, approval.StageCheckTrigger)
	decideCheck(t, dir, first, approval.Decision{Action: approval.ActionRetryGate, Gates: []string{gatelib.GateAgentReview}})
	runID := filepath.Base(onlyRunDir(t, dir))
	if err := resumeWave(t, dir, rt, cfg, runID); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a retry that fails again must block again: %v", err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	var pending *approval.PendingApproval
	for index := range values {
		if values[index].Kind == approval.KindCheckDecision && values[index].Status == approval.StatusPending {
			pending = &values[index]
		}
	}
	if pending == nil || pending.ID == first.ID {
		t.Fatalf("a new pending decision is required, first=%s values=%+v", first.ID, values)
	}

	decideCheck(t, dir, pending, approval.Decision{
		Action: "override", Comment: "рецензент недоступен, риск принят", ActorID: "admin-1",
		Authority: approval.AuthoritySystemAdmin,
	})
	if err := resumeWave(t, dir, rt, cfg, runID); err != nil {
		t.Fatalf("the override must let the run on: %v", err)
	}
	events := gateEvents(t, dir)
	overrides := eventsOfType(events, "check_overridden")
	if len(overrides) != 1 {
		t.Fatalf("one override record expected, got %d", len(overrides))
	}
	override, ok := gatesched.DecodeOverride(overrides[0])
	if !ok || override.ActorID != "admin-1" || override.Authority != gatesched.AuthoritySystemAdmin ||
		override.Reason != "рецензент недоступен, риск принят" || override.ArtifactSHA256 == "" || override.Visit != 1 ||
		len(override.Gates) != 1 || override.Gates[0].GateName != gatelib.GateAgentReview ||
		override.Gates[0].Execution != workflow.GateExecutionError {
		t.Fatalf("override record = %+v", override)
	}
	if overrides[0].Data["outcome"] != "error" {
		t.Fatalf("the overridden outcome stays an error: %v", overrides[0].Data["outcome"])
	}
	history, _ := gatesched.AllAttempts(events, "tech")
	for _, attempt := range history {
		if attempt.GateName == gatelib.GateAgentReview && attempt.Execution != workflow.GateExecutionError {
			t.Fatalf("the override must not rewrite attempts: %+v", attempt)
		}
	}
	if override.Gates[0].AttemptID == "" {
		t.Fatal("the override names the exact attempt")
	}
}

// Acceptance: an override lets the run on, but the failed gate stays a fail in
// the attempt history, and the override is bound to the checked version.
func TestGateOverride_RecordsTheFailedGateAndDoesNotChangeItsOutcome(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез раздела\n\n**Verdict:** APPROVED\n"}
	rt.contentFn["verifier"] = passingReviewer
	cfg := waveConfig(codeGatesThenVerifier(verifierGate()))
	if err := runWave(t, dir, cfg, rt); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a failed required gate must block: %v", err)
	}
	decision := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if containsString(decision.Actions, approval.ActionRetryGate) {
		t.Fatalf("a verdict fail is not retryable: %v", decision.Actions)
	}
	shaBefore := checkDecisionPayload(t, decision).ArtifactSHA256
	decideCheck(t, dir, decision, approval.Decision{
		Action: "override", Comment: "формат раздела пересмотрим позже", ActorID: "admin-1", Authority: approval.AuthoritySystemAdmin,
	})
	runID := filepath.Base(onlyRunDir(t, dir))
	if err := resumeWave(t, dir, rt, cfg, runID); err != nil {
		t.Fatalf("override must let the run on: %v", err)
	}
	events := gateEvents(t, dir)
	overrides := eventsOfType(events, "check_overridden")
	if len(overrides) != 1 {
		t.Fatalf("overrides = %d", len(overrides))
	}
	override, _ := gatesched.DecodeOverride(overrides[0])
	if override.ArtifactSHA256 != shaBefore || len(override.Gates) != 1 || override.Gates[0].Verdict != workflow.GateVerdictFail ||
		override.Gates[0].GateName != gatelib.GateRequiredSections || override.Authority != gatesched.AuthoritySystemAdmin {
		t.Fatalf("override record = %+v", override)
	}
	if overrides[0].Data["outcome"] != "fail" {
		t.Fatalf("outcome of an overridden fail stays fail: %v", overrides[0].Data["outcome"])
	}
	history, _ := gatesched.AllAttempts(events, "tech")
	for _, attempt := range history {
		if attempt.GateName == gatelib.GateRequiredSections && (attempt.Verdict != workflow.GateVerdictFail || attempt.Execution != workflow.GateExecutionSucceeded) {
			t.Fatalf("the gate must stay red after the override: %+v", attempt)
		}
	}
	// The chain stopped at the failed group: the reviewer never ran, and the
	// override says so by naming only the attempts that were recorded.
	if countEvents(events, gatesched.EventAttemptStarted, gatelib.GateAgentReview) != 0 {
		t.Fatal("the dependent gate must not run because of an override")
	}
}

// Acceptance: an advisory fail does not block, but is visible in evidence; the
// transition depends only on the required gates of a mixed group.
func TestGateAdvisory_FailIsAVisibleWarningAndDoesNotBlock(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n## Критерии приёмки\n\nбез маркера\n"}
	rt.contentFn["verifier"] = passingReviewer
	advisoryMarker := codeGate(gatelib.GateVerdictMarker, nil)
	advisoryMarker.Mode = workflow.GateModeAdvisory
	cfg := waveConfig(workflow.GateGroups{
		{codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}}), advisoryMarker},
		{verifierGate()},
	})
	if err := runWave(t, dir, cfg, rt); err != nil {
		t.Fatalf("an advisory fail must not block the transition: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech,verifier" {
		t.Fatalf("the next group must start after a mixed group: %v", rt.executed)
	}
	events := gateEvents(t, dir)
	checks := eventsOfType(events, "check_passed")
	if len(checks) != 1 {
		t.Fatalf("check_passed events = %d", len(checks))
	}
	warnings, _ := checks[0].Data["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("the passed check must carry the warning: %+v", checks[0].Data)
	}
	warning, _ := warnings[0].(map[string]any)
	if warning["gate"] != gatelib.GateVerdictMarker || warning["verdict"] != "fail" || warning["mode"] != "advisory" {
		t.Fatalf("warning = %+v", warning)
	}
	if checks[0].Data["severity"] != "warning" {
		t.Fatalf("a check with warnings is a warning event: %+v", checks[0].Data)
	}
	history, _ := gatesched.AllAttempts(events, "tech")
	found := false
	for _, attempt := range history {
		if attempt.GateName == gatelib.GateVerdictMarker {
			found = true
			if attempt.Mode != workflow.GateModeAdvisory || attempt.Verdict != workflow.GateVerdictFail {
				t.Fatalf("the advisory attempt keeps its fail: %+v", attempt)
			}
		}
	}
	if !found {
		t.Fatal("advisory attempt not recorded")
	}
	for _, event := range eventsOfType(events, gatesched.EventAttemptFinished) {
		if event.Data["gate_name"] == gatelib.GateVerdictMarker && (event.Data["mode"] != "advisory" || event.Data["advisory_warning"] != true) {
			t.Fatalf("attempt event must say advisory and warning: %+v", event.Data)
		}
	}
	if _, err := approval.NewStore(dir); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readEvents(t, dir), "check_failed") {
		t.Fatal("an advisory fail must not produce check_failed")
	}
}

// A technical error of an advisory gate is visible too and is not a success.
func TestGateAdvisory_TechnicalErrorIsVisibleAndNotASuccess(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": goodTechResult}
	rt.contentFn["verifier"] = brokenReviewer
	soft := verifierGate()
	soft.Mode = workflow.GateModeAdvisory
	cfg := waveConfig(codeGatesThenVerifier(soft))
	if err := runWave(t, dir, cfg, rt); err != nil {
		t.Fatalf("an advisory technical error must not block: %v", err)
	}
	events := gateEvents(t, dir)
	checks := eventsOfType(events, "check_passed")
	warnings, _ := checks[len(checks)-1].Data["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("the error must be a visible warning: %+v", checks[len(checks)-1].Data)
	}
	warning, _ := warnings[0].(map[string]any)
	if warning["execution"] != "error" || warning["verdict"] != nil {
		t.Fatalf("an error stays an error, no verdict: %+v", warning)
	}
}

// An override covers one result version only: the pure fold of the event log
// ignores it for another hash.
func TestFoldStageCheck_OverrideAndRetryAreBoundToTheVersion(t *testing.T) {
	const shaA, shaB = "aaaa", "bbbb"
	failed := evidence.Event{Type: "check_failed", Stage: "tech", AttemptID: "att-1", Data: map[string]any{"artifact_sha256": shaA, "status": "failed"}}
	overridden := evidence.Event{Type: "check_overridden", Stage: "tech", AttemptID: "att-1", Data: map[string]any{"artifact_sha256": shaA}}
	retry := evidence.Event{Type: gatesched.EventGateRetryRequested, Stage: "tech", AttemptID: "att-1", Data: map[string]any{"artifact_sha256": shaA}}
	passed := evidence.Event{Type: "check_passed", Stage: "tech", AttemptID: "att-1", Data: map[string]any{"artifact_sha256": shaA, "status": "passed"}}

	if check, found := foldStageCheck([]evidence.Event{failed, overridden}, "tech", "att-1", shaA); !found || check.Status != "passed" {
		t.Fatalf("an override of this version satisfies the check: %+v %v", check, found)
	}
	if check, found := foldStageCheck([]evidence.Event{failed, overridden}, "tech", "att-1", shaB); found {
		t.Fatalf("an override of another version must not satisfy a new one: %+v", check)
	}
	if check, found := foldStageCheck([]evidence.Event{failed, retry}, "tech", "att-1", shaA); found {
		t.Fatalf("a retry request supersedes the earlier check until the chain ran again: %+v", check)
	}
	if check, found := foldStageCheck([]evidence.Event{failed, retry, passed}, "tech", "att-1", shaA); !found || check.Status != "passed" {
		t.Fatalf("the check after the retry is the effective one: %+v %v", check, found)
	}
	failedB := evidence.Event{Type: "check_failed", Stage: "tech", AttemptID: "att-1", Data: map[string]any{"artifact_sha256": shaB, "status": "failed"}}
	if check, found := foldStageCheck([]evidence.Event{failedB, retry}, "tech", "att-1", shaB); !found || check.Status != "failed" {
		t.Fatalf("a retry of another version does not touch this one: %+v %v", check, found)
	}
	legacy := evidence.Event{Type: "check_overridden", Stage: "tech", AttemptID: "att-1", Data: map[string]any{}}
	if _, found := foldStageCheck([]evidence.Event{failed, legacy}, "tech", "att-1", shaA); !found {
		t.Fatal("an override recorded before versions existed keeps its meaning")
	}
}

// Regression (M1): the override is bound to the system administrator right at
// the enforcement point. A decision without the server-confirmed authority is
// refused by the store (CLI path) and, if one reached the run anyway, by the
// pipeline: it is never applied.
func TestGateOverride_WithoutAdminAuthorityDoesNotAdvance(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез раздела\n\n**Verdict:** APPROVED\n"}
	rt.contentFn["verifier"] = passingReviewer
	cfg := waveConfig(codeGatesThenVerifier(verifierGate()))
	if err := runWave(t, dir, cfg, rt); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a failed required gate must block: %v", err)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, decision := range map[string]approval.Decision{
		"cli like (no authority)":  {ActorID: "anyone", ActorRole: "architect", Action: "override", Comment: "x", SubjectHash: value.SubjectHash},
		"authority not confirmed":  {ActorID: "anyone", ActorRole: "architect", Action: "override", Comment: "x", SubjectHash: value.SubjectHash, Authority: approval.AuthoritySystemAdmin},
		"authenticated, no rights": {ActorID: "anyone", ActorRole: "architect", Action: "override", Comment: "x", SubjectHash: value.SubjectHash, ControllerAuthenticated: true},
		"retry from the CLI":       {ActorID: "anyone", ActorRole: "architect", Action: approval.ActionRetryGate, Comment: "x", SubjectHash: value.SubjectHash, Gates: []string{"g"}},
	} {
		if _, err := store.Decide(value.RunID, value.ID, decision); err == nil {
			t.Fatalf("%s: the store must refuse the decision", name)
		}
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	err = resumeWave(t, dir, rt, cfg, runID)
	if err == nil {
		t.Fatal("the run must not advance without a valid override")
	}
	if overrides := eventsOfType(gateEvents(t, dir), "check_overridden"); len(overrides) != 0 {
		t.Fatalf("no override may be recorded: %d", len(overrides))
	}

	// Enforcement point inside the pipeline: an override decision that reached
	// the run without the confirmed authority is refused, not applied.
	rs := &runState{resumedApproval: &approval.PendingApproval{ID: "a", Decisions: []approval.Decision{
		{ActorID: "anyone", Action: "override", Comment: "причина"},
	}}}
	if _, err := rs.applyCheckOverride("tech"); err == nil || !strings.Contains(err.Error(), "system_admin") {
		t.Fatalf("applyCheckOverride must refuse a decision without authority: %v", err)
	}
}
