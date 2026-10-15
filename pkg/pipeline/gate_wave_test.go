package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_wave_test.go — the group scheduler wired into the stage executor (S03).

// waveConfig is one agent stage "tech" whose exit chain is the given groups.
func waveConfig(groups workflow.GateGroups) *config.Config {
	cfg := stageCheckConfig(nil)
	cfg.Stages[0].Confirm = ""
	cfg.Stages[0].ExitGateGroups = groups
	return cfg
}

func codeGate(id string, params map[string]any) workflow.GateRef {
	return workflow.GateRef{Gate: id, Version: 1, Params: params}
}

func twoCodeGatesThenReviewer() workflow.GateGroups {
	return workflow.GateGroups{
		{
			codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}}),
			codeGate(gatelib.GateVerdictMarker, nil),
		},
		{codeGate(gatelib.GateAgentReview, map[string]any{"agent": "verifier"})},
	}
}

// eventsOnlyStore serves a finished run's verified events to replay helpers.
type eventsOnlyStore struct {
	EvidenceStore
	events []evidence.Event
}

func (s eventsOnlyStore) ReadEvents() ([]evidence.Event, error) { return s.events, nil }

// runWave runs the single-stage template with the reviewer agents registered.
func runWave(t *testing.T, dir string, cfg *config.Config, rt *scriptedRuntime) error {
	t.Helper()
	p := New(cfg, stageCheckRegistry(true),
		WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	return p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir, ApproveGates: true})
}

func gateEvents(t *testing.T, dir string) []evidence.Event {
	t.Helper()
	runDir := onlyRunDir(t, dir)
	events, err := evidence.VerifyEventLogWithSource(filepath.Join(runDir, "events.jsonl"), filepath.Base(runDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func countEvents(events []evidence.Event, eventType, gateName string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType && (gateName == "" || event.Data["gate_name"] == gateName) {
			count++
		}
	}
	return count
}

func indexOfEvent(events []evidence.Event, eventType, gateName string) int {
	for i, event := range events {
		if event.Type == eventType && event.Data["gate_name"] == gateName {
			return i
		}
	}
	return -1
}

func TestGateWave_GroupsRunInOrderAndEveryAttemptIsRecorded(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n## Критерии приёмки\n\nвсё ок\n\n**Verdict:** APPROVED\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт\n\n**Verdict:** PASSED\n"}
	if err := runWave(t, dir, waveConfig(twoCodeGatesThenReviewer()), rt); err != nil {
		t.Fatalf("a chain whose gates all pass must let the stage through: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech,verifier" {
		t.Fatalf("executed = %v", rt.executed)
	}
	events := gateEvents(t, dir)
	for _, gate := range []string{gatelib.GateRequiredSections, gatelib.GateVerdictMarker, gatelib.GateAgentReview} {
		if countEvents(events, gatesched.EventAttemptStarted, gate) != 1 || countEvents(events, gatesched.EventAttemptFinished, gate) != 1 {
			t.Fatalf("gate %s must have exactly one started and one finished record", gate)
		}
	}
	// The reviewer (second group) starts only after both code gates finished.
	reviewerStart := indexOfEvent(events, gatesched.EventAttemptStarted, gatelib.GateAgentReview)
	for _, gate := range []string{gatelib.GateRequiredSections, gatelib.GateVerdictMarker} {
		if finished := indexOfEvent(events, gatesched.EventAttemptFinished, gate); finished < 0 || finished > reviewerStart {
			t.Fatalf("reviewer started before %s finished", gate)
		}
	}
	// History: times and outcomes of every attempt.
	history, err := gatesched.AllAttempts(events, "tech")
	if err != nil || len(history) != 3 {
		t.Fatalf("history = %d attempts, err %v", len(history), err)
	}
	for _, attempt := range history {
		if err := attempt.Validate(); err != nil {
			t.Fatalf("attempt record invalid: %v", err)
		}
		if attempt.Execution != workflow.GateExecutionSucceeded || attempt.Verdict != workflow.GateVerdictPass ||
			attempt.StartedAt.IsZero() || attempt.FinishedAt.IsZero() || attempt.ArtifactSHA256 == "" {
			t.Fatalf("history entry lacks outcome or time: %+v", attempt)
		}
	}
	if !strings.Contains(readEvents(t, dir), `"type":"check_passed"`) {
		t.Fatal("the legacy check_passed record must still be written")
	}
}

func TestGateWave_CodeFailureStopsBeforeTheReviewer(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	// No "Критерии приёмки" section: the first group fails.
	rt.content["tech"] = map[string]string{"result": "# Решение\n\nбез раздела\n\n**Verdict:** APPROVED\n"}
	rt.content["verifier"] = map[string]string{"verification": "# Отчёт\n\n**Verdict:** PASSED\n"}
	err := runWave(t, dir, waveConfig(twoCodeGatesThenReviewer()), rt)
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a failed required gate must block the transition: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech" {
		t.Fatalf("the dependent LLM gate started although the code group failed: %v", rt.executed)
	}
	events := gateEvents(t, dir)
	if countEvents(events, gatesched.EventAttemptStarted, gatelib.GateAgentReview) != 0 {
		t.Fatal("no attempt may be recorded for a gate that did not start")
	}
	history, _ := gatesched.AllAttempts(events, "tech")
	verdicts := map[string]workflow.GateVerdict{}
	for _, attempt := range history {
		verdicts[attempt.GateName] = attempt.Verdict
	}
	if verdicts[gatelib.GateRequiredSections] != workflow.GateVerdictFail || verdicts[gatelib.GateVerdictMarker] != workflow.GateVerdictPass {
		t.Fatalf("the sibling gate must run to its own verdict: %v", verdicts)
	}
	value := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if value.Status != approval.StatusPending {
		t.Fatalf("check_decision = %q", value.Status)
	}
	if report := checkReportFile(t, dir); !strings.Contains(report, "не запущены") || !strings.Contains(report, gatelib.GateAgentReview) {
		t.Fatalf("the report must say which gates did not start:\n%s", report)
	}
}

func TestGateWave_RestartDoesNotRepeatCompletedGates(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "## Спецификация\n\nПроверяемый критерий.\n"}
	rt.contentFn["spec-checker"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"spec-check": "**Question:** Как измерить критерий?\n"}
		}
		return map[string]string{"spec-check": "**Verdict:** PASSED\n"}
	}
	cfg := waveConfig(workflow.GateGroups{
		{codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Спецификация"}})},
		{codeGate(gatelib.GateAgentReview, map[string]any{"agent": "spec-checker", "mode": "grill", "max_rounds": 3})},
	})
	cfg.Stages[0].Function = "product_owner"
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := newGrillPipeline(cfg, rt, store)
	if err := runGrillPipeline(t, p, dir, ""); err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("the grill gate must wait for an answer: %v", err)
	}
	events := gateEvents(t, dir)
	if countEvents(events, gatesched.EventAttemptWaiting, gatelib.GateAgentReview) != 1 {
		t.Fatal("a waiting gate must be recorded as waiting, not finished")
	}
	runID := filepath.Base(onlyRunDir(t, dir))
	pending := grillApproval(t, store, runID)
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: approval.GrillActionAnswer,
		Comment: "Порог — рост конверсии минимум на 5%.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runGrillPipeline(t, p, dir, runID); err != nil {
		t.Fatalf("answered grill must finish the chain: %v", err)
	}
	events = gateEvents(t, dir)
	if got := countEvents(events, gatesched.EventAttemptStarted, gatelib.GateRequiredSections); got != 1 {
		t.Fatalf("the completed code gate was started %d times across the restart", got)
	}
	if got := countEvents(events, gatesched.EventAttemptStarted, gatelib.GateAgentReview); got != 1 {
		t.Fatalf("the waiting gate must keep one attempt across the wait, got %d", got)
	}
	if countEvents(events, gatesched.EventAttemptFinished, gatelib.GateAgentReview) != 1 {
		t.Fatal("the resumed gate must finish exactly once")
	}
	if got := countEvents(events, gatesched.EventPinsRecorded, ""); got != 1 {
		t.Fatalf("gate pins recorded %d times, want once per task", got)
	}
}

func TestGateWave_PinsAreRecordedAtStartWithDigests(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n**Verdict:** APPROVED\n"}
	cfg := waveConfig(workflow.GateGroups{{codeGate(gatelib.GateVerdictMarker, nil)}})
	if err := runWave(t, dir, cfg, rt); err != nil {
		t.Fatal(err)
	}
	var pins evidence.Event
	for _, event := range gateEvents(t, dir) {
		if event.Type == gatesched.EventPinsRecorded {
			pins = event
		}
	}
	items, _ := pins.Data["pins"].([]any)
	if len(items) != 1 {
		t.Fatalf("pins event = %+v", pins)
	}
	pin, _ := items[0].(map[string]any)
	definition, _ := gatelib.Default().Lookup(gatelib.GateVerdictMarker, 1)
	if pin["gate"] != gatelib.GateVerdictMarker || pin["digest"] != gatelib.Digest(definition) || pin["version"] != float64(1) {
		t.Fatalf("pin = %+v", pin)
	}
}

func TestGateWave_ChangedDefinitionIsRejectedOnResume(t *testing.T) {
	definition, _ := gatelib.Default().Lookup(gatelib.GateVerdictMarker, 1)
	pin := gatelib.Pin(definition)
	recorded := evidence.Event{Type: gatesched.EventPinsRecorded, Data: map[string]any{"pins": []any{
		map[string]any{"gate": pin.Gate, "version": float64(pin.Version), "kind": string(pin.Kind), "digest": pin.Digest},
	}}}
	if err := verifyRecordedPins(recorded, []workflow.GatePin{pin}); err != nil {
		t.Fatalf("identical pin rejected: %v", err)
	}
	tampered := pin
	tampered.Digest = strings.Repeat("0", 64)
	if err := verifyRecordedPins(recorded, []workflow.GatePin{tampered}); err == nil || !strings.Contains(err.Error(), "изменилось") {
		t.Fatalf("a definition that changed since the task began must be rejected: %v", err)
	}
	other := workflow.GatePin{Gate: "pr_open", Version: 1, Kind: workflow.GateKindBuiltin, Digest: "x"}
	if err := verifyRecordedPins(recorded, []workflow.GatePin{other}); err == nil || !strings.Contains(err.Error(), "не было закреплено") {
		t.Fatalf("an unpinned definition must be rejected: %v", err)
	}
}

// A check recorded for one result version is never reused for another.
func TestGateWave_RecordedCheckOfAnotherVersionIsIgnored(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Решение\n\n**Verdict:** APPROVED\n"}
	cfg := waveConfig(workflow.GateGroups{{codeGate(gatelib.GateVerdictMarker, nil)}})
	if err := runWave(t, dir, cfg, rt); err != nil {
		t.Fatal(err)
	}
	runDir := onlyRunDir(t, dir)
	runID := filepath.Base(runDir)
	var attemptID, sha string
	for _, event := range gateEvents(t, dir) {
		if event.Type == "check_passed" {
			attemptID = event.AttemptID
			sha, _ = event.Data["artifact_sha256"].(string)
		}
	}
	if attemptID == "" || sha == "" {
		t.Fatal("check_passed must carry the result version it covers")
	}
	rs := &runState{p: &Pipeline{}, evidence: eventsOnlyStore{events: gateEvents(t, dir)}, runID: runID}
	if _, found, err := rs.replayedStageCheckVersion("tech", attemptID, sha); err != nil || !found {
		t.Fatalf("same version must be found: found=%v err=%v", found, err)
	}
	if _, found, err := rs.replayedStageCheckVersion("tech", attemptID, strings.Repeat("f", 64)); err != nil || found {
		t.Fatalf("a success recorded for old content must not satisfy new content: found=%v err=%v", found, err)
	}
}

func TestGateWave_ConcurrencyLimitComesFromConfig(t *testing.T) {
	cfg := waveConfig(twoCodeGatesThenReviewer())
	cfg.GateConcurrency = 2
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "gate_concurrency: 2") {
		t.Fatalf("the limit must round-trip through the template:\n%s", data)
	}
}
