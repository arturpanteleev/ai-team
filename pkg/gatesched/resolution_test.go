package gatesched

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func advisory(id string) workflow.GateRef {
	r := ref(id)
	r.Mode = workflow.GateModeAdvisory
	return r
}

// Acceptance: a technical error is retried on the same version; the gates of
// the group that already passed are not run again, the earlier attempt stays,
// and the chain goes on to the next group.
func TestRetryOfTechnicalErrorKeepsPassedSiblingsAndContinues(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), ref("b")}, {ref("c")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin), def("c", workflow.GateKindBuiltin))
	journal := &MemoryJournal{}
	calls := newCounters()
	runner := RunnerFunc(func(_ context.Context, in Invocation) Outcome {
		calls.hit(in.Ref.Gate)
		if in.Ref.Gate == "b" && calls.count("b") == 1 {
			return Outcome{Execution: workflow.GateExecutionError, Reason: "сбой запуска"}
		}
		return Pass("")
	})
	s := newScheduler(t, journal, Options{}, runner)
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions}
	first, err := s.Run(context.Background(), wave)
	if err != nil || first.Status != StatusError || first.StoppedAtGroup != 0 || calls.count("c") != 0 {
		t.Fatalf("first run: %+v err=%v", first, err)
	}
	wave.Retry = map[string]bool{"b": true}
	second, err := s.Run(context.Background(), wave)
	if err != nil || second.Status != StatusPassed {
		t.Fatalf("retry: %+v err=%v", second, err)
	}
	if calls.count("a") != 1 || calls.count("b") != 2 || calls.count("c") != 1 {
		t.Fatalf("a must not repeat, b runs twice, c once: a=%d b=%d c=%d", calls.count("a"), calls.count("b"), calls.count("c"))
	}
	history, _ := journal.Load(wave.Key)
	var bAttempts int
	for _, attempt := range history {
		if attempt.GateName == "b" {
			bAttempts++
		}
	}
	if bAttempts != 2 {
		t.Fatalf("both attempts of b must stay in the history, got %d", bAttempts)
	}
}

// A retry never rescues a verdict: a recorded fail is content, not a
// technical failure, and is not run again even when it is named.
func TestRetryDoesNotRerunANegativeVerdict(t *testing.T) {
	groups := workflow.GateGroups{{ref("a")}}
	definitions := defs(def("a", workflow.GateKindBuiltin))
	calls := newCounters()
	s := newScheduler(t, &MemoryJournal{}, Options{}, RunnerFunc(func(_ context.Context, in Invocation) Outcome {
		calls.hit("a")
		return Fail("нет раздела")
	}))
	wave := Wave{Key: key(hashV1), Groups: groups, Definitions: definitions, Retry: map[string]bool{"a": true}}
	if _, err := s.Run(context.Background(), wave); err != nil {
		t.Fatal(err)
	}
	result, err := s.Run(context.Background(), wave)
	if err != nil || result.Status != StatusFailed || calls.count("a") != 1 {
		t.Fatalf("status=%s calls=%d err=%v", result.Status, calls.count("a"), err)
	}
}

// Acceptance: a technical error of an advisory gate is visible as a warning,
// is not a success, and does not block; a mixed group waits for all its gates.
func TestAdvisoryTechnicalErrorIsAVisibleWarningInAMixedGroup(t *testing.T) {
	groups := workflow.GateGroups{{ref("a"), advisory("b")}, {ref("c")}}
	definitions := defs(def("a", workflow.GateKindBuiltin), def("b", workflow.GateKindBuiltin), def("c", workflow.GateKindBuiltin))
	release := make(chan struct{})
	finished := make(chan string, 4)
	runner := RunnerFunc(func(_ context.Context, in Invocation) Outcome {
		defer func() { finished <- in.Ref.Gate }()
		if in.Ref.Gate == "b" {
			<-release
			return Outcome{Execution: workflow.GateExecutionTimeout, Reason: "таймаут рекомендации"}
		}
		return Pass("")
	})
	s := newScheduler(t, &MemoryJournal{}, Options{}, runner)
	done := make(chan Result, 1)
	go func() {
		result, _ := s.Run(context.Background(), Wave{Key: key(hashV1), Groups: groups, Definitions: definitions})
		done <- result
	}()
	select {
	case gate := <-finished:
		if gate != "a" {
			t.Fatalf("unexpected first finisher %s", gate)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a did not finish")
	}
	select {
	case <-done:
		t.Fatal("the next group must wait for the whole group, advisory gate included")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	var result Result
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wave did not finish")
	}
	if result.Status != StatusPassed {
		t.Fatalf("advisory technical error must not block: %s", result.Status)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Execution != workflow.GateExecutionTimeout || result.Warnings[0].Verdict != "" {
		t.Fatalf("the timeout must stay a visible timeout warning, not a success: %+v", result.Warnings)
	}
}

func attemptOf(name string, mode workflow.GateMode, group, n int, execution workflow.GateExecution, verdict workflow.GateVerdict) workflow.GateAttempt {
	a := NewAttempt(key(hashV1), workflow.GateRef{Gate: name, Version: 1, Mode: mode}, def(name, workflow.GateKindBuiltin), group, n, time.Unix(1700000000, 0).UTC())
	a.Execution, a.Verdict = execution, verdict
	if execution.Terminal() {
		a.FinishedAt = a.StartedAt.Add(time.Second)
	}
	return a
}

func TestResolveAggregatesTheTransitionDecision(t *testing.T) {
	refs := []workflow.GateRef{ref("a"), advisory("b"), ref("c")}
	pass := attemptOf("a", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)

	t.Run("all passed with an advisory warning", func(t *testing.T) {
		attempts := []workflow.GateAttempt{pass,
			attemptOf("b", workflow.GateModeAdvisory, 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail),
			attemptOf("c", "", 1, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)}
		statuses, res := Resolve(refs, nil, hashV1, attempts, nil)
		if !res.Allowed || res.State != ResolutionPassedWithWarnings || len(res.Warnings) != 1 || res.Warnings[0] != "b" {
			t.Fatalf("resolution = %+v", res)
		}
		if !statuses[1].Warning || statuses[1].Blocking {
			t.Fatalf("advisory fail must warn and not block: %+v", statuses[1])
		}
	})

	failedC := attemptOf("c", "", 1, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	t.Run("required fail blocks", func(t *testing.T) {
		_, res := Resolve(refs, nil, hashV1, []workflow.GateAttempt{pass, failedC}, nil)
		if res.Allowed || res.State != ResolutionBlocked || len(res.Blocking) != 1 || res.Blocking[0] != "c" {
			t.Fatalf("resolution = %+v", res)
		}
	})

	t.Run("technical error is retryable and distinct from fail", func(t *testing.T) {
		timeout := attemptOf("c", "", 1, 1, workflow.GateExecutionTimeout, "")
		statuses, res := Resolve(refs, nil, hashV1, []workflow.GateAttempt{pass, timeout}, nil)
		if res.Allowed || res.State != ResolutionError || len(res.Retryable) != 1 || !statuses[2].Retryable {
			t.Fatalf("resolution = %+v statuses=%+v", res, statuses)
		}
	})

	t.Run("a required gate that never started blocks", func(t *testing.T) {
		_, res := Resolve(refs, nil, hashV1, []workflow.GateAttempt{pass}, nil)
		if res.Allowed || res.State != ResolutionIncomplete {
			t.Fatalf("resolution = %+v", res)
		}
	})

	override := &Override{
		Stage: "spec", Scope: "attempt-1", ArtifactSHA256: hashV1, Reason: "срочно", ActorID: "admin-1",
		Authority: AuthoritySystemAdmin, Gates: []OverriddenGate{{GateName: "c", AttemptID: failedC.AttemptID}},
	}
	t.Run("override allows the transition but keeps the outcome red", func(t *testing.T) {
		statuses, res := Resolve(refs, nil, hashV1, []workflow.GateAttempt{pass, failedC}, override)
		if !res.Allowed || res.State != ResolutionOverridden || len(res.Overridden) != 1 {
			t.Fatalf("resolution = %+v", res)
		}
		got := statuses[2]
		if !got.Overridden || !got.Blocking || got.Attempt.Execution != workflow.GateExecutionSucceeded || got.Attempt.Verdict != workflow.GateVerdictFail {
			t.Fatalf("the override must not turn fail into pass: %+v", got)
		}
		if !strings.Contains(strings.Join(res.Reasons, " "), "admin-1") {
			t.Fatalf("the reason must name the administrator: %v", res.Reasons)
		}
	})

	t.Run("an override of another version does not apply", func(t *testing.T) {
		_, res := Resolve(refs, nil, hashV2, []workflow.GateAttempt{pass, failedC}, override)
		if res.Allowed || res.State != ResolutionBlocked {
			t.Fatalf("resolution = %+v", res)
		}
		if !strings.Contains(strings.Join(res.Reasons, " "), "другой версии") {
			t.Fatalf("the refusal must say why: %v", res.Reasons)
		}
	})

	t.Run("an override does not cover a later attempt", func(t *testing.T) {
		later := attemptOf("c", "", 1, 2, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
		_, res := Resolve(refs, nil, hashV1, []workflow.GateAttempt{pass, failedC, later}, override)
		if res.Allowed {
			t.Fatalf("a new attempt needs its own override: %+v", res)
		}
	})

	t.Run("a legacy override without a version covers nothing", func(t *testing.T) {
		legacy := &Override{Stage: "spec", Scope: "attempt-1", Reason: "старый"}
		_, res := Resolve(refs, nil, hashV1, []workflow.GateAttempt{pass, failedC}, legacy)
		if res.Allowed {
			t.Fatalf("resolution = %+v", res)
		}
	})
}

func TestDecodeOverrideRoundTripsTheEventPayload(t *testing.T) {
	failed := attemptOf("c", "", 1, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	failed.Reason = "нет раздела"
	event := evidence.Event{
		Type: EventCheckOverridden, Stage: "spec", AttemptID: "attempt-1", Timestamp: time.Unix(1700000100, 0).UTC(),
		Data: map[string]any{
			"reason": "срочно", "actor_id": "admin-1", "authority": AuthoritySystemAdmin, "artifact_sha256": hashV1,
			"visit": 2, "approval_id": "approval-1", "check_status": "failed",
			"gates": OverriddenGates([]workflow.GateAttempt{failed}),
		},
	}
	// Events come back from JSON storage with generic types.
	roundTripped := mustJSONRoundTrip(t, event)
	got, ok := DecodeOverride(roundTripped)
	if !ok || got.ActorID != "admin-1" || got.ArtifactSHA256 != hashV1 || got.Visit != 2 || len(got.Gates) != 1 ||
		got.Gates[0].AttemptID != failed.AttemptID || got.Gates[0].Verdict != workflow.GateVerdictFail || got.Gates[0].Reason != "нет раздела" {
		t.Fatalf("decoded = %+v ok=%v", got, ok)
	}
	if _, ok := DecodeOverride(evidence.Event{Type: "check_failed"}); ok {
		t.Fatal("other events are not overrides")
	}
}

func mustJSONRoundTrip(t *testing.T, event evidence.Event) evidence.Event {
	t.Helper()
	data, err := json.Marshal(event.Data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	event.Data = decoded
	return event
}

// An advisory gate with a technical error is a warning, not a retry target:
// the retry would be refused (no decision is ever opened for it).
func TestResolveAdvisoryTechnicalErrorIsNotRetryable(t *testing.T) {
	refs := []workflow.GateRef{ref("a"), advisory("b")}
	attempts := []workflow.GateAttempt{
		attemptOf("a", "", 0, 1, workflow.GateExecutionError, ""),
		attemptOf("b", workflow.GateModeAdvisory, 0, 1, workflow.GateExecutionError, ""),
	}
	statuses, res := Resolve(refs, nil, hashV1, attempts, nil)
	if !statuses[0].Retryable || statuses[1].Retryable {
		t.Fatalf("only the blocking gate is retryable: %+v", statuses)
	}
	if len(res.Retryable) != 1 || res.Retryable[0] != "a" || len(res.Warnings) != 1 {
		t.Fatalf("resolution = %+v", res)
	}
}

// An override recorded before S06 (no version, no gates) is shown as legacy
// history: the wave of a task that really passed must not look blocked.
func TestResolveLegacyOverrideIsShownAsHistory(t *testing.T) {
	refs := []workflow.GateRef{ref("a")}
	attempts := []workflow.GateAttempt{attemptOf("a", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)}
	legacy, ok := DecodeOverride(evidence.Event{Type: EventCheckOverridden, Stage: "spec", AttemptID: "attempt-1",
		Data: map[string]any{"reason": "старый обход", "actor_id": "alice"}})
	if !ok || !legacy.Legacy || legacy.Authority != "" {
		t.Fatalf("legacy override = %+v", legacy)
	}
	statuses, res := Resolve(refs, nil, hashV1, attempts, &legacy)
	if res.Allowed || res.State != ResolutionLegacyOverride || len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "legacy") {
		t.Fatalf("resolution = %+v", res)
	}
	if statuses[0].Attempt.Verdict != workflow.GateVerdictFail {
		t.Fatal("the legacy override does not rewrite the attempt")
	}
}
