package stagestatus

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func defs() []StageDef {
	return []StageDef{
		{ID: "spec", Title: "Spec", Function: "po", Executor: "human"},
		{ID: "design", Title: "Design", Function: "architect", Executor: "agent", Agent: "architect"},
		{ID: "impl", Title: "Implementation", Function: "developer", Executor: "agent", Agent: "coder"},
		{ID: "review", Title: "Review", Function: "reviewer", Executor: "agent", Agent: "reviewer", Adapter: "container"},
		{ID: "qa", Title: "QA", Function: "qa", Executor: "human"},
	}
}

// journal builds an ordered event list with growing timestamps.
type journal struct {
	events []Event
}

func (j *journal) add(eventType, stage, attempt string, data map[string]any) *journal {
	seq := int64(len(j.events) + 1)
	j.events = append(j.events, Event{
		Sequence: seq, Type: eventType, Stage: stage, AttemptID: attempt,
		Timestamp: epoch.Add(time.Duration(seq) * time.Minute), Data: data,
	})
	return j
}

func (j *journal) started() *journal { return j.add("run_started", "", "", nil) }

func (j *journal) begin(stage, attempt string) *journal {
	return j.add("attempt_started", "", attempt, map[string]any{"agent": stage, "stage_index": 1})
}

func (j *journal) finish(stage, attempt, outcome string, extra map[string]any) *journal {
	data := map[string]any{"stage": stage, "outcome": outcome, "status": outcome, "execution": "succeeded", "executor": "agent",
		"executor_kind": "internal_agent", "executor_name": "agentcli"}
	for key, value := range extra {
		data[key] = value
	}
	return j.add("attempt_finished", "", attempt, data)
}

func (j *journal) move(from, target, action string) *journal {
	return j.add("transition_selected", "", "", map[string]any{"from": from, "target": target, "action": action, "outcome": "passed", "edge_target": target})
}

func (j *journal) request(id, kind, trigger, from, to string, actions ...string) *journal {
	list := make([]any, 0, len(actions))
	for _, action := range actions {
		list = append(list, action)
	}
	return j.add("approval_requested", "", "a-"+id, map[string]any{"approval_id": id, "kind": kind, "trigger": trigger,
		"from_stage": from, "to_stage": to, "actions": list, "required_roles": []any{"reviewer"}})
}

func (j *journal) decide(id string) *journal {
	return j.add("approval_decided", "", "a-"+id, map[string]any{"approval_id": id, "resolved_action": "approve"})
}

func project(j *journal, mutate ...func(*Input)) Projection {
	in := Input{Stages: defs(), Events: j.events, RunStatus: "running"}
	for _, apply := range mutate {
		apply(&in)
	}
	return Project(in)
}

func stageOf(t *testing.T, p Projection, id string) Stage {
	t.Helper()
	for _, stage := range p.Stages {
		if stage.StageID == id {
			return stage
		}
	}
	t.Fatalf("no stage %s in %+v", id, p)
	return Stage{}
}

func statuses(p Projection) map[string]Status {
	result := map[string]Status{}
	for _, stage := range p.Stages {
		result[stage.StageID] = stage.Status
	}
	return result
}

func lifecycleIn(phase, next string) func(*Input) {
	return func(in *Input) { in.Lifecycle = &Lifecycle{Phase: phase, NextStage: next} }
}

func TestNothingHappenedYetEverythingIsNotReached(t *testing.T) {
	p := project(&journal{}, func(in *Input) { in.RunStatus = "queued" })
	if p.CurrentStage != "" || p.Terminal {
		t.Fatalf("a queued task has no current stage: %+v", p)
	}
	for id, status := range statuses(p) {
		if status != StatusNotReached {
			t.Fatalf("%s = %s", id, status)
		}
	}
	if stageOf(t, p, "design").Executor.Kind != ExecutorInternalAgent || stageOf(t, p, "review").Executor.Kind != ExecutorExternalAgent ||
		stageOf(t, p, "qa").Executor.Kind != ExecutorHuman {
		t.Fatal("an unreached stage names its configured executor kind")
	}
	if p.SchemaVersion != SchemaVersion {
		t.Fatal("schema version")
	}
}

func TestEntryStageIsReadyAfterStart(t *testing.T) {
	p := project((&journal{}).started())
	spec := stageOf(t, p, "spec")
	if spec.Status != StatusReady || spec.Readiness != ReadinessReady || !spec.Current || spec.ActiveVisit != 1 || spec.Visits[0].EnteredVia != EnteredStart {
		t.Fatalf("%+v", spec)
	}
	custom := project((&journal{}).started(), func(in *Input) { in.Entry = "impl" })
	if custom.CurrentStage != "impl" {
		t.Fatalf("the entry of the graph wins: %+v", custom)
	}
}

func TestHumanStageReadyThenTakenThenDone(t *testing.T) {
	j := (&journal{}).started().request("in1", "input", "human_input", "spec", "spec", "submit")
	ready := stageOf(t, project(j), "spec")
	if ready.Status != StatusReady || ready.BlockedBy == nil || ready.BlockedBy.Kind != BlockHumanInput || ready.BlockedBy.ApprovalID != "in1" ||
		len(ready.BlockedBy.Roles) != 1 {
		t.Fatalf("ready for a person: %+v", ready)
	}
	j.add("stage_taken", "spec", "", map[string]any{"approval_id": "in1", "actor_id": "alice"})
	j.events[len(j.events)-1].Actor = "alice"
	taken := stageOf(t, project(j), "spec")
	if taken.Status != StatusInProgress || taken.Phase != PhaseWorking || taken.Executor.Kind != ExecutorHuman || taken.Executor.Actor != "alice" {
		t.Fatalf("taken: %+v", taken)
	}
	// A take of an older approval does not count for a new input request.
	j.decide("in1").request("in2", "input", "human_input", "spec", "spec", "submit")
	if again := stageOf(t, project(j), "spec"); again.Status != StatusReady {
		t.Fatalf("a new input request is not taken yet: %+v", again)
	}
}

func TestAgentWorkIsInProgressAndExternalNamesTheAdapter(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1")
	p := project(j)
	design := stageOf(t, p, "design")
	if design.Status != StatusInProgress || design.Phase != PhaseWorking || design.Executor.Kind != ExecutorInternalAgent || design.Executor.Agent != "architect" ||
		design.Executor.Source != SourceConfigured || design.Outcome != "pending" {
		t.Fatalf("%+v", design)
	}
	j2 := (&journal{}).started().move("spec", "review", "").begin("review", "r1")
	review := stageOf(t, project(j2), "review")
	if review.Executor.Kind != ExecutorExternalAgent || review.Executor.Name != "container" {
		t.Fatalf("%+v", review)
	}
}

func TestFinishedAttemptIsCheckedUntilTheCheckpointParks(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil)
	if got := stageOf(t, project(j), "design"); got.Status != StatusChecking || got.Phase != PhaseChecking {
		t.Fatalf("without a checkpoint a finished attempt is being checked: %+v", got)
	}
	if got := stageOf(t, project(j, lifecycleIn("running", "design")), "design"); got.Status != StatusChecking {
		t.Fatalf("running checkpoint: %+v", got)
	}
	parked := stageOf(t, project(j, lifecycleIn("waiting", "design")), "design")
	if parked.Status == StatusChecking {
		t.Fatalf("a parked task checks nothing: %+v", parked)
	}
	// The executor of a finished attempt is the actual one, not the configured one.
	if checked := stageOf(t, project(j), "design"); checked.Executor.Source != SourceAttempt || checked.Executor.Name != "agentcli" {
		t.Fatalf("actual executor: %+v", checked.Executor)
	}
}

func TestConfirmationIsAwaitingHumanWithAllowedActions(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
		request("c1", "approve", "graph_outcome:passed", "design", "impl", "approve", "reject")
	p := project(j, lifecycleIn("waiting", "impl"), func(in *Input) { in.AllowedActions = []string{"confirm"} })
	design := stageOf(t, p, "design")
	if design.Status != StatusAwaitingHuman || design.Phase != PhaseAwaitingHuman || design.BlockedBy == nil || design.BlockedBy.Kind != BlockApproval ||
		!reflect.DeepEqual(design.BlockedBy.Actions, []string{"approve", "reject"}) || !reflect.DeepEqual(design.AllowedActions, []string{"confirm"}) ||
		design.ActionStageID != "impl" || design.Readiness != ReadinessEngaged {
		t.Fatalf("%+v", design)
	}
	if got := stageOf(t, p, "impl"); got.Status != StatusNotReached || len(got.AllowedActions) != 0 {
		t.Fatalf("the next stage is not reached: %+v", got)
	}
	// The approval store knows the decision before the journal does.
	decided := project(j, lifecycleIn("waiting", "impl"), func(in *Input) {
		in.Approvals = []Approval{{ID: "c1", Status: "resolved"}}
	})
	if got := stageOf(t, decided, "design"); got.Status == StatusAwaitingHuman {
		t.Fatalf("a resolved approval no longer awaits a person: %+v", got)
	}
	// Without a journal event the store alone raises the wait.
	storeOnly := project((&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil),
		func(in *Input) {
			in.Approvals = []Approval{{ID: "s1", Kind: "approve", FromStage: "design", Status: "pending", Actions: []string{"approve"}}}
		})
	if got := stageOf(t, storeOnly, "design"); got.Status != StatusAwaitingHuman {
		t.Fatalf("store-only approval: %+v", got)
	}
}

func TestApprovalKindsMapToBlockSources(t *testing.T) {
	cases := map[string]struct {
		kind, trigger string
		want          string
	}{
		"check":    {"check_decision", "stage_check", BlockCheckDecision},
		"grill":    {"grill_questions", "grill_questions", BlockGrill},
		"delivery": {"approve", "delivery_plan", BlockDeliveryPlan},
		"plain":    {"approve", "graph_outcome:passed", BlockApproval},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
				request("x1", tc.kind, tc.trigger, "design", "design", "approve")
			got := stageOf(t, project(j, lifecycleIn("waiting", "design")), "design")
			if got.Status != StatusAwaitingHuman || got.BlockedBy.Kind != tc.want {
				t.Fatalf("%+v", got.BlockedBy)
			}
		})
	}
}

func gateEvent(eventType, stage, id, name, execution, verdict, mode string, human bool) (string, string, string, map[string]any) {
	attempt := map[string]any{"executor": "builtin"}
	if human {
		attempt["executor"] = "human"
	}
	data := map[string]any{"gate_attempt_id": id, "gate_name": name, "execution": execution, "mode": mode, "attempt": attempt}
	if verdict != "" {
		data["verdict"] = verdict
	}
	return eventType, stage, id, data
}

func TestGatesAreCheckingAndAHumanGateAwaitsAPerson(t *testing.T) {
	base := func() *journal {
		return (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil)
	}
	running := base()
	running.add(gateEvent("gate_attempt_started", "design", "g1", "sections", "running", "", "required", false))
	if got := stageOf(t, project(running, lifecycleIn("running", "design")), "design"); got.Status != StatusChecking || got.BlockedBy == nil ||
		got.BlockedBy.Kind != BlockGate || !reflect.DeepEqual(got.BlockedBy.Gates, []string{"sections"}) {
		t.Fatalf("a running gate is checking: %+v", got)
	}
	human := base()
	human.add(gateEvent("gate_attempt_started", "design", "g2", "signoff", "running", "", "required", true))
	human.add(gateEvent("gate_attempt_waiting", "design", "g2", "signoff", "running", "", "required", true))
	if got := stageOf(t, project(human), "design"); got.Status != StatusAwaitingHuman || got.BlockedBy.Kind != BlockGate {
		t.Fatalf("a human gate waits for a person: %+v", got)
	}
	failed := base()
	failed.add(gateEvent("gate_attempt_started", "design", "g3", "sections", "running", "", "required", false))
	failed.add(gateEvent("gate_attempt_finished", "design", "g3", "sections", "succeeded", "fail", "required", false))
	failed.add(gateEvent("gate_attempt_finished", "design", "g4", "tone", "succeeded", "fail", "advisory", false))
	failed.request("cd1", "check_decision", "stage_check", "design", "design", "retry_gate", "override")
	got := stageOf(t, project(failed, lifecycleIn("waiting", "design")), "design")
	if got.Status != StatusAwaitingHuman || got.BlockedBy.Kind != BlockCheckDecision || !reflect.DeepEqual(got.BlockedBy.Gates, []string{"sections"}) {
		t.Fatalf("a failed required gate awaits a decision and names the gate, not the advisory one: %+v", got.BlockedBy)
	}
	// A technical failure of a required gate also counts; a retry that
	// succeeds clears it.
	retry := base()
	retry.add(gateEvent("gate_attempt_finished", "design", "g5", "sections", "error", "", "required", false))
	retry.request("cd2", "check_decision", "stage_check", "design", "design", "retry_gate")
	if got := stageOf(t, project(retry, lifecycleIn("waiting", "design")), "design"); !reflect.DeepEqual(got.BlockedBy.Gates, []string{"sections"}) {
		t.Fatalf("%+v", got.BlockedBy)
	}
	retry.add(gateEvent("gate_attempt_finished", "design", "g6", "sections", "succeeded", "pass", "required", false))
	if got := stageOf(t, project(retry, lifecycleIn("waiting", "design")), "design"); len(got.BlockedBy.Gates) != 0 {
		t.Fatalf("a later passing attempt of the same gate clears it: %+v", got.BlockedBy)
	}
}

func TestAdvisoryGateWarningsAreCountedOnTheVisit(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil)
	_, _, _, data := gateEvent("gate_attempt_finished", "design", "g1", "tone", "succeeded", "fail", "advisory", false)
	data["advisory_warning"] = true
	j.add("gate_attempt_finished", "design", "g1", data)
	got := stageOf(t, project(j), "design")
	if got.Warnings != 1 || got.Visits[0].Warnings != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestReturnStartsANewVisitAndKeepsTheOldAttempt(t *testing.T) {
	j := (&journal{}).started().
		move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
		move("design", "impl", "").begin("impl", "i1").finish("impl", "i1", "passed", nil).
		move("impl", "review", "").begin("review", "r1").finish("review", "r1", "rejected",
		map[string]any{"executor_kind": "external", "executor_name": "container"}).
		move("review", "impl", "return_to_impl").
		add("attempts_invalidated", "", "", map[string]any{"attempt_ids": []any{"r1"}})
	p := project(j, lifecycleIn("running", "impl"))
	impl := stageOf(t, p, "impl")
	if impl.Status != StatusReady || impl.VisitCount != 2 || impl.ActiveVisit != 2 || !impl.Current || impl.Outcome != "pending" {
		t.Fatalf("the second visit has not started work: %+v", impl)
	}
	first, second := impl.Visits[0], impl.Visits[1]
	if first.Active || first.Outcome != "passed" || first.LeftTo != "review" || len(first.Attempts) != 1 || first.Attempts[0].AttemptID != "i1" ||
		first.Attempts[0].Outcome != "passed" {
		t.Fatalf("first visit: %+v", first)
	}
	if !second.Active || second.EnteredVia != EnteredReturn || second.EnteredFrom != "review" || len(second.Attempts) != 0 {
		t.Fatalf("second visit: %+v", second)
	}
	review := stageOf(t, p, "review")
	if review.Status != StatusNotReached || !review.Visited || review.Current || len(review.Visits) != 1 || !review.Visits[0].Returned ||
		!review.Visits[0].Superseded || review.Visits[0].Outcome != "rejected" || review.Visits[0].LeftAction != "return_to_impl" ||
		review.Visits[0].Attempts[0].Executor.Kind != ExecutorExternalAgent || !review.Visits[0].Attempts[0].Superseded {
		t.Fatalf("the review it came from: %+v", review)
	}
	// The second pass: the first visit stays inactive, a second review visit begins.
	j.begin("impl", "i2").finish("impl", "i2", "passed", nil).move("impl", "review", "").begin("review", "r2")
	p = project(j)
	if got := stageOf(t, p, "impl"); got.Status != StatusNotReached && got.Status != StatusPassed {
		t.Fatalf("%+v", got)
	}
	if got := stageOf(t, p, "impl"); len(got.Visits) != 2 || got.Visits[1].Active || got.Visits[1].Outcome != "passed" || len(got.Visits[1].Attempts) != 1 ||
		got.Status != StatusPassed {
		t.Fatalf("both implementation visits are history now: %+v", got)
	}
	if got := stageOf(t, p, "review"); got.Status != StatusInProgress || got.VisitCount != 2 || got.ActiveVisit != 2 || got.Visits[0].Active {
		t.Fatalf("the review is back for a second visit: %+v", got)
	}
}

func TestStayDoesNotOpenANewVisit(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "rejected", nil).
		move("design", "design", "reject").begin("design", "d2")
	got := stageOf(t, project(j), "design")
	if got.VisitCount != 1 || len(got.Visits[0].Attempts) != 2 || got.Status != StatusInProgress {
		t.Fatalf("a stay is another attempt of the same visit: %+v", got)
	}
}

func TestSupersededAttemptsOfAnOpenVisitAreIgnoredForTheStatus(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
		add("attempts_invalidated", "", "", map[string]any{"attempt_ids": []any{"d1"}})
	got := stageOf(t, project(j, lifecycleIn("resumable", "design")), "design")
	if got.Status != StatusReady || got.Outcome != "pending" {
		t.Fatalf("an invalidated attempt does not make the stage checking: %+v", got)
	}
}

func TestTerminalRunClosesTheOpenVisit(t *testing.T) {
	done := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
		move("design", "$complete", "").add("run_finished", "", "", map[string]any{"status": "completed"})
	p := project(done, func(in *Input) { in.RunStatus = "completed" })
	if !p.Terminal || p.CurrentStage != "" {
		t.Fatalf("%+v", p)
	}
	design := stageOf(t, p, "design")
	if design.Status != StatusPassed || design.Readiness != ReadinessLeft || design.Current || design.Visits[0].Active || design.Visits[0].LeftAt == nil ||
		design.Visits[0].LeftTo != "$complete" {
		t.Fatalf("%+v", design)
	}

	canceled := (&journal{}).started().move("spec", "design", "").begin("design", "d1").
		add("run_canceled", "", "", nil).add("run_finished", "", "", map[string]any{"status": "canceled"})
	got := stageOf(t, project(canceled, func(in *Input) { in.RunStatus = "canceled" }), "design")
	if got.Status != StatusCanceled || got.Visits[0].Active || got.Outcome != "canceled" {
		t.Fatalf("a canceled run leaves its working stage canceled: %+v", got)
	}

	// The run record alone (an old journal without run_finished) also ends it.
	byStatus := project((&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "failed", nil),
		func(in *Input) { in.RunStatus = "failed" })
	if got := stageOf(t, byStatus, "design"); !byStatus.Terminal || got.Status != StatusFailed {
		t.Fatalf("%+v", got)
	}
	byCheckpoint := project((&journal{}).started(), lifecycleIn("terminal", ""))
	if !byCheckpoint.Terminal {
		t.Fatal("a terminal checkpoint ends the task")
	}
}

func TestPausedRunShowsTheOutcomeAndWhyNothingMoves(t *testing.T) {
	cases := map[string]Status{"failed": StatusFailed, "blocked": StatusBlocked, "rejected": StatusRejected, "canceled": StatusCanceled}
	for outcome, want := range cases {
		t.Run(outcome, func(t *testing.T) {
			j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", outcome, nil)
			p := project(j, func(in *Input) { in.RunStatus = "stopped" })
			got := stageOf(t, p, "design")
			if got.Status != want || got.BlockedBy == nil || got.BlockedBy.Kind != BlockRunState || !got.Current {
				t.Fatalf("%+v", got)
			}
		})
	}
	interrupted := project((&journal{}).started().move("spec", "design", ""), func(in *Input) { in.RunStatus = "interrupted" })
	got := stageOf(t, interrupted, "design")
	if got.Status != StatusReady || got.BlockedBy == nil || got.BlockedBy.Kind != BlockRunState {
		t.Fatalf("an interrupted run waits to be resumed: %+v", got)
	}
	done := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil)
	idle := stageOf(t, project(done, func(in *Input) { in.RunStatus = "stopped" }), "design")
	if idle.Status != StatusReady || idle.BlockedBy == nil {
		t.Fatalf("without a worker nothing is being checked: %+v", idle)
	}
}

func TestSkipOverrideAndWarningMarks(t *testing.T) {
	skipped := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "skipped", nil).
		add("stage_skipped", "design", "d1", map[string]any{"reason": "not needed"}).move("design", "impl", "")
	if got := stageOf(t, project(skipped), "design"); got.Status != StatusSkipped || !got.Visits[0].Skipped || got.Outcome != "skipped" {
		t.Fatalf("%+v", got)
	}
	overridden := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
		add("check_overridden", "design", "d1", map[string]any{"reason": "risk accepted"}).move("design", "impl", "")
	got := stageOf(t, project(overridden), "design")
	if got.Status != StatusPassed || !got.Overridden || !got.Visits[0].Overridden {
		t.Fatalf("a passed stage keeps the mark of its override: %+v", got)
	}
	warning := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "warning", nil).move("design", "impl", "")
	if got := stageOf(t, project(warning), "design"); got.Status != StatusPassed || got.Outcome != "warning" {
		t.Fatalf("a waived attempt passes with its outcome kept: %+v", got)
	}
	abandoned := (&journal{}).started().move("spec", "design", "").begin("design", "d1").
		add("attempt_abandoned", "", "d1", map[string]any{"reason": "restart"}).begin("design", "d2")
	if got := stageOf(t, project(abandoned), "design"); got.Visits[0].Attempts[0].Outcome != "canceled" || got.Status != StatusInProgress {
		t.Fatalf("%+v", got)
	}
}

func TestExecutorOverrideAndActualExecutors(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").
		request("in1", "input", "human_input", "design", "design", "submit", "run_agent")
	asHuman := stageOf(t, project(j, func(in *Input) {
		in.Lifecycle = &Lifecycle{Phase: "waiting", NextStage: "design", PendingApprovalID: "in1",
			ExecutorOverrides: map[string]LifecycleOverride{"design": {Executor: "human", ApprovalID: "in1"}}}
	}), "design")
	if asHuman.Executor.Kind != ExecutorHuman || asHuman.Executor.Source != SourceOverride || asHuman.Status != StatusReady {
		t.Fatalf("a person chose to do it: %+v", asHuman)
	}
	stale := stageOf(t, project(j, func(in *Input) {
		in.Lifecycle = &Lifecycle{Phase: "waiting", NextStage: "design", PendingApprovalID: "in1",
			ExecutorOverrides: map[string]LifecycleOverride{"design": {Executor: "human", ApprovalID: "older"}}}
	}), "design")
	if stale.Executor.Kind != ExecutorInternalAgent || stale.Executor.Source != SourceConfigured {
		t.Fatalf("an override of another visit does not apply: %+v", stale)
	}
	j.add("executor_changed", "design", "", map[string]any{"to_executor": "human", "from_executor": "agent"})
	if got := stageOf(t, project(j), "design"); got.Executor.Kind != ExecutorHuman || got.Executor.Source != SourceOverride {
		t.Fatalf("the recorded change applies to its visit: %+v", got)
	}
	// An attempt without attested executor info falls back to the configured one.
	legacy := (&journal{}).started().move("spec", "design", "").begin("design", "d1").
		add("attempt_finished", "", "d1", map[string]any{"stage": "design", "outcome": "passed", "status": "passed", "executor": "agent"})
	if got := stageOf(t, project(legacy), "design"); got.Executor.Kind != ExecutorInternalAgent || got.Executor.Source != SourceConfigured {
		t.Fatalf("%+v", got.Executor)
	}
	humanAttempt := (&journal{}).started().begin("spec", "s1").
		add("attempt_finished", "", "s1", map[string]any{"stage": "spec", "outcome": "passed", "status": "passed", "executor": "human", "actor_id": "bob"})
	if got := stageOf(t, project(humanAttempt), "spec"); got.Executor.Kind != ExecutorHuman || got.Executor.Actor != "bob" || got.Executor.Source != SourceAttempt {
		t.Fatalf("%+v", got.Executor)
	}
}

func TestOldJournalsWithoutTransitionsStillProject(t *testing.T) {
	// No run_started, no transition_selected: only attempts. Visits are
	// inferred and at most one is open.
	j := (&journal{}).begin("spec", "s1").finish("spec", "s1", "passed", map[string]any{"executor": "human"}).
		begin("design", "d1").finish("design", "d1", "passed", nil).begin("impl", "i1")
	p := project(j)
	if p.CurrentStage != "impl" {
		t.Fatalf("%+v", p)
	}
	spec := stageOf(t, p, "spec")
	if spec.Status != StatusPassed || spec.Visits[0].EnteredVia != EnteredStart && spec.Visits[0].EnteredVia != EnteredInferred || spec.Visits[0].Active {
		t.Fatalf("%+v", spec)
	}
	if stageOf(t, p, "impl").Visits[0].EnteredFrom != "design" {
		t.Fatalf("inferred entry: %+v", stageOf(t, p, "impl").Visits)
	}
}

func TestUnknownStagesAndEventsAreKept(t *testing.T) {
	j := (&journal{}).started().move("spec", "retired", "").begin("retired", "x1").add("some_future_event", "retired", "", nil).
		add("attempt_started", "", "", map[string]any{"agent": ""}) // no stage, no attempt: ignored
	p := project(j)
	if len(p.Stages) != 6 || p.Stages[5].StageID != "retired" || p.Stages[5].Status != StatusInProgress {
		t.Fatalf("a stage the template no longer lists is still shown: %+v", p.Stages)
	}
}

func TestEventsOutOfOrderAreSortedBySequence(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1")
	reversed := append([]Event(nil), j.events...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	a := Project(Input{Stages: defs(), Events: j.events, RunStatus: "running"})
	b := Project(Input{Stages: defs(), Events: reversed, RunStatus: "running"})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the journal order is its sequence, not the order of delivery")
	}
}

func TestProjectionIsPureAndSerializesStably(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil)
	before := append([]Event(nil), j.events...)
	first, second := project(j), project(j)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("the same journal must project to the same state")
	}
	if !reflect.DeepEqual(before, j.events) {
		t.Fatal("the projection must not change its input")
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("stable JSON")
	}
	// The JSON carries lists, never null, so a client can iterate.
	var raw map[string]any
	if err := json.Unmarshal(a, &raw); err != nil {
		t.Fatal(err)
	}
	for _, stage := range raw["stages"].([]any) {
		fields := stage.(map[string]any)
		if fields["visits"] == nil || fields["allowed_actions"] == nil {
			t.Fatalf("lists must not be null: %v", fields)
		}
	}
}

// Replaying every prefix of a realistic journal keeps the invariants: one
// current stage, an active visit only on it, and visits numbered from one.
func TestEveryPrefixOfAJournalKeepsTheInvariants(t *testing.T) {
	j := (&journal{}).started().
		request("in1", "input", "human_input", "spec", "spec", "submit").
		add("stage_taken", "spec", "", map[string]any{"approval_id": "in1", "actor_id": "alice"}).
		begin("spec", "s1").finish("spec", "s1", "passed", map[string]any{"executor": "human"}).decide("in1").move("spec", "design", "").
		begin("design", "d1").finish("design", "d1", "passed", nil).
		request("c1", "approve", "graph_outcome:passed", "design", "impl", "approve").decide("c1").move("design", "impl", "").
		begin("impl", "i1").finish("impl", "i1", "passed", nil).move("impl", "review", "").
		begin("review", "r1").finish("review", "r1", "rejected", nil).
		request("c2", "approve", "graph_outcome:rejected", "review", "impl", "return_to_impl").decide("c2").
		move("review", "impl", "return_to_impl").add("attempts_invalidated", "", "", map[string]any{"attempt_ids": []any{"r1", "i1"}}).
		begin("impl", "i2").finish("impl", "i2", "passed", nil).move("impl", "review", "").
		begin("review", "r2").finish("review", "r2", "passed", nil).move("review", "qa", "").
		add("run_finished", "", "", map[string]any{"status": "completed"})
	for cut := 0; cut <= len(j.events); cut++ {
		p := Project(Input{Stages: defs(), Events: j.events[:cut], RunStatus: "running"})
		current := 0
		for _, stage := range p.Stages {
			if stage.Current {
				current++
			}
			for index, visit := range stage.Visits {
				if visit.Number != index+1 {
					t.Fatalf("cut %d: visit numbers of %s: %+v", cut, stage.StageID, stage.Visits)
				}
				if visit.Active && !stage.Current {
					t.Fatalf("cut %d: an active visit on a stage that is not current: %s", cut, stage.StageID)
				}
				if visit.Active && index != len(stage.Visits)-1 {
					t.Fatalf("cut %d: only the last visit can be active: %s", cut, stage.StageID)
				}
			}
			if stage.Status == "" || stage.Readiness == "" || stage.Phase == "" {
				t.Fatalf("cut %d: incomplete %+v", cut, stage)
			}
		}
		if current > 1 || (p.Terminal && current != 0) {
			t.Fatalf("cut %d: %d current stages (terminal=%v)", cut, current, p.Terminal)
		}
	}
}

func TestSummaryCompactsTheCurrentStageAndTheRoute(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").finish("design", "d1", "passed", nil).
		move("design", "impl", "").begin("impl", "i1").finish("impl", "i1", "passed", nil).move("impl", "review", "").
		begin("review", "r1").finish("review", "r1", "rejected", nil).move("review", "impl", "return_to_impl").
		add("attempts_invalidated", "", "", map[string]any{"attempt_ids": []any{"r1"}})
	summary := project(j, func(in *Input) { in.AllowedActions = []string{"run-agent"} }).Summary()
	if summary.CurrentStage != "impl" || summary.Status != StatusReady || summary.Visit != 2 || summary.VisitCount != 2 || !summary.Returned ||
		summary.Executor == nil || summary.Executor.Kind != ExecutorInternalAgent || len(summary.Route) != 5 {
		t.Fatalf("%+v", summary)
	}
	if summary.Route[3].StageID != "review" || summary.Route[3].Status != StatusNotReached || !summary.Route[3].Visited {
		t.Fatalf("the route keeps the stage that was active before: %+v", summary.Route[3])
	}
	finished := project((&journal{}).started(), lifecycleIn("terminal", "")).Summary()
	if finished.CurrentStage != "" || finished.Executor != nil || !finished.Terminal {
		t.Fatalf("%+v", finished)
	}
}

func TestInterruptedMidAttemptIsReadyNotInProgress(t *testing.T) {
	feed := (&journal{}).started().move("spec", "design", "").begin("design", "d1")
	// The evidence journal also says the attempt was abandoned.
	evidence := (&journal{}).started().move("spec", "design", "").begin("design", "d1").
		add("attempt_abandoned", "", "d1", map[string]any{"reason": "interrupted"})
	for _, status := range []string{"interrupted", "stopped"} {
		t.Run(status, func(t *testing.T) {
			byFeed := project(feed, func(in *Input) { in.RunStatus = status })
			got := stageOf(t, byFeed, "design")
			if got.Status != StatusReady || got.Phase != PhaseNone || got.Readiness != ReadinessReady || got.Outcome != "pending" ||
				got.BlockedBy == nil || got.BlockedBy.Kind != BlockRunState || !got.Current {
				t.Fatalf("nobody works on an interrupted attempt: %+v", got)
			}
			if got.Visits[0].Outcome != "pending" || got.Visits[0].Attempts[0].FinishedAt != nil {
				t.Fatalf("the attempt has no end: %+v", got.Visits[0])
			}
			byEvidence := project(evidence, func(in *Input) { in.RunStatus = status })
			if !reflect.DeepEqual(byFeed, byEvidence) {
				t.Fatalf("the feed and the evidence journal disagree:\n%+v\n%+v", byFeed, byEvidence)
			}
		})
	}
	// While the run is alive the same journal is work in progress.
	if got := stageOf(t, project(feed), "design"); got.Status != StatusInProgress || got.BlockedBy != nil {
		t.Fatalf("%+v", got)
	}
}

func TestFailedRunDoesNotCancelTheOpenAttempt(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").begin("design", "d1").move("design", "$fail", "")
	got := stageOf(t, project(j, func(in *Input) { in.RunStatus = "failed" }), "design")
	if got.Status != StatusFailed || got.Outcome != "failed" {
		t.Fatalf("a task that failed did not cancel anything: %+v", got)
	}
}

func TestVisitedStageWithoutAttemptsIsNeverNotReached(t *testing.T) {
	j := (&journal{}).started().move("spec", "design", "").move("design", "impl", "").move("impl", "design", "return_to_design")
	p := project(j)
	spec := stageOf(t, p, "spec")
	if !spec.Visited || spec.Status != StatusPassed || spec.Visits[0].Outcome != "passed" {
		t.Fatalf("moved on without attempts: passed by the transition: %+v", spec)
	}
	impl := stageOf(t, p, "impl")
	if !impl.Visited || impl.Status != StatusRejected {
		t.Fatalf("sent back without attempts: %+v", impl)
	}
	for _, stage := range p.Stages {
		if stage.Visited && stage.Status == StatusNotReached && !stage.Current {
			t.Fatalf("visited yet not reached: %+v", stage)
		}
	}
}
