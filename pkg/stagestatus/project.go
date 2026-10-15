package stagestatus

import (
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Event types the projection reads. Everything else in the journal is
// ignored, so newer event types never break an older projection and an old
// journal that lacks a type still projects.
const (
	eventRunStarted         = "run_started"
	eventRunFinished        = "run_finished"
	eventRunCanceled        = "run_canceled"
	eventAttemptStarted     = "attempt_started"
	eventAttemptFinished    = "attempt_finished"
	eventAttemptAbandoned   = "attempt_abandoned"
	eventAttemptsInvalid    = "attempts_invalidated"
	eventTransition         = "transition_selected"
	eventApprovalRequested  = "approval_requested"
	eventApprovalDecided    = "approval_decided"
	eventStageTaken         = "stage_taken"
	eventExecutorChanged    = "executor_changed"
	eventStageSkipped       = "stage_skipped"
	eventCheckOverridden    = "check_overridden"
	eventGateAttemptStarted = "gate_attempt_started"
	eventGateAttemptWaiting = "gate_attempt_waiting"
	eventGateAttemptDone    = "gate_attempt_finished"
)

type attemptRec struct {
	VisitAttempt
	finished bool
	visit    *visitRec
}

type visitRec struct {
	Visit
	stage         string
	open          bool
	attempts      []*attemptRec
	owner         string
	ownerApproval string
	override      string
	closedByRun   bool
}

type approvalRec struct {
	Approval
	at time.Time
}

type gateRec struct {
	visit     *visitRec
	name      string
	execution string
	verdict   string
	mode      string
	human     bool
	waiting   bool
	order     int
}

type builder struct {
	in        Input
	defs      map[string]StageDef
	index     map[string]int
	visits    map[string][]*visitRec
	open      *visitRec
	attempts  map[string]*attemptRec
	approvals map[string]*approvalRec
	gates     map[string]*gateRec
	gateOrder int
	finished  bool
	canceled  bool
	lastAt    time.Time
}

// Project derives the state of every stage from the durable facts. It is
// deterministic and has no side effects: calling it twice on the same input,
// or on the same journal after a restart, returns the same answer.
func Project(in Input) Projection {
	b := &builder{
		in: in, defs: map[string]StageDef{}, index: map[string]int{},
		visits: map[string][]*visitRec{}, attempts: map[string]*attemptRec{},
		approvals: map[string]*approvalRec{}, gates: map[string]*gateRec{},
	}
	for position, def := range in.Stages {
		b.defs[def.ID] = def
		b.index[def.ID] = position
	}
	events := append([]Event(nil), in.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Sequence != 0 && events[j].Sequence != 0 {
			return events[i].Sequence < events[j].Sequence
		}
		return false
	})
	for _, event := range events {
		if !event.Timestamp.IsZero() && event.Timestamp.After(b.lastAt) {
			b.lastAt = event.Timestamp
		}
		b.apply(event)
	}
	b.overlayApprovals()
	return b.project()
}

func (b *builder) entry() string {
	if b.in.Entry != "" {
		return b.in.Entry
	}
	if len(b.in.Stages) > 0 {
		return b.in.Stages[0].ID
	}
	return ""
}

func (b *builder) stageOf(event Event) string {
	for _, value := range []string{event.Stage, text(event.Data, "stage"), text(event.Data, "stage_id")} {
		if value != "" {
			return value
		}
	}
	if event.Type == eventAttemptStarted {
		if agent := text(event.Data, "agent"); agent != "" {
			return agent
		}
	}
	if event.AttemptID != "" {
		if attempt := b.attempts[event.AttemptID]; attempt != nil {
			return attempt.visit.stage
		}
	}
	return ""
}

// enter opens a new visit of the stage and closes whatever was open: the task
// is at one stage at a time.
func (b *builder) enter(stage string, at time.Time, via, from string) *visitRec {
	if b.open != nil {
		b.leave(b.open, at, stage, "", false)
	}
	v := &visitRec{stage: stage, open: true}
	v.Number = len(b.visits[stage]) + 1
	v.EnteredAt, v.EnteredVia, v.EnteredFrom = at, via, from
	b.visits[stage] = append(b.visits[stage], v)
	b.open = v
	return v
}

func (b *builder) leave(v *visitRec, at time.Time, to, action string, returned bool) {
	if !v.open {
		return
	}
	v.open = false
	leftAt := at
	v.LeftAt = &leftAt
	v.LeftTo, v.LeftAction, v.Returned = to, action, returned
	if b.open == v {
		b.open = nil
	}
}

// visitFor returns the open visit of the stage, opening one when the journal
// shows work on a stage no transition announced (older journals).
func (b *builder) visitFor(stage string, at time.Time) *visitRec {
	if b.open != nil && b.open.stage == stage {
		return b.open
	}
	from := ""
	if b.open != nil {
		from = b.open.stage
	}
	return b.enter(stage, at, EnteredInferred, from)
}

func (b *builder) apply(event Event) {
	at := event.Timestamp
	switch event.Type {
	case eventRunStarted:
		if entry := b.entry(); entry != "" && b.open == nil && len(b.visits[entry]) == 0 {
			b.enter(entry, at, EnteredStart, "")
		}
	case eventAttemptStarted:
		stage := b.stageOf(event)
		if stage == "" || event.AttemptID == "" || b.attempts[event.AttemptID] != nil {
			return
		}
		b.addAttempt(b.visitFor(stage, at), event, at)
	case eventAttemptFinished:
		stage := b.stageOf(event)
		attempt := b.attempts[event.AttemptID]
		if attempt == nil {
			if stage == "" || event.AttemptID == "" {
				return
			}
			attempt = b.addAttempt(b.visitFor(stage, at), event, at)
		}
		b.finishAttempt(attempt, event, at)
	case eventAttemptAbandoned:
		if attempt := b.attempts[event.AttemptID]; attempt != nil && !attempt.finished {
			attempt.finished = true
			finished := at
			attempt.FinishedAt = &finished
			attempt.Execution, attempt.Outcome = "canceled", "canceled"
		}
	case eventAttemptsInvalid:
		for _, id := range stringsOf(event.Data["attempt_ids"]) {
			if attempt := b.attempts[id]; attempt != nil {
				attempt.Superseded = true
			}
		}
	case eventTransition:
		b.transition(event, at)
	case eventApprovalRequested:
		id := text(event.Data, "approval_id")
		if id == "" {
			return
		}
		b.approvals[id] = &approvalRec{at: at, Approval: Approval{
			ID: id, Kind: text(event.Data, "kind"), Trigger: text(event.Data, "trigger"),
			FromStage: text(event.Data, "from_stage"), ToStage: text(event.Data, "to_stage"),
			Status: "pending", RequiredRoles: stringsOf(event.Data["required_roles"]), Actions: stringsOf(event.Data["actions"]),
		}}
	case eventApprovalDecided:
		delete(b.approvals, text(event.Data, "approval_id"))
	case eventStageTaken:
		stage := b.stageOf(event)
		if v := b.openOf(stage); v != nil {
			v.owner = firstText(event.Actor, text(event.Data, "actor_id"), text(event.Data, "actor"))
			v.ownerApproval = text(event.Data, "approval_id")
		}
	case eventExecutorChanged:
		if v := b.openOf(b.stageOf(event)); v != nil {
			v.override = text(event.Data, "to_executor")
		}
	case eventStageSkipped:
		if v := b.openOrLast(b.stageOf(event), event.AttemptID); v != nil {
			v.Skipped = true
		}
	case eventCheckOverridden:
		if v := b.openOrLast(b.stageOf(event), event.AttemptID); v != nil {
			v.Overridden = true
		}
	case eventGateAttemptStarted, eventGateAttemptWaiting, eventGateAttemptDone:
		b.gate(event)
	case eventRunCanceled:
		b.canceled = true
	case eventRunFinished:
		b.finished = true
		if b.open != nil {
			b.open.closedByRun = true
			b.leave(b.open, at, "", "", false)
		}
	}
}

func (b *builder) openOf(stage string) *visitRec {
	if stage != "" && b.open != nil && b.open.stage == stage {
		return b.open
	}
	return nil
}

// openOrLast finds the visit an event belongs to: the open visit of the stage,
// or the visit of its attempt, or the last visit of the stage.
func (b *builder) openOrLast(stage, attemptID string) *visitRec {
	if attempt := b.attempts[attemptID]; attempt != nil && attemptID != "" {
		return attempt.visit
	}
	if v := b.openOf(stage); v != nil {
		return v
	}
	if visits := b.visits[stage]; len(visits) > 0 {
		return visits[len(visits)-1]
	}
	return nil
}

func (b *builder) addAttempt(v *visitRec, event Event, at time.Time) *attemptRec {
	attempt := &attemptRec{visit: v}
	attempt.AttemptID, attempt.Outcome, attempt.StartedAt = event.AttemptID, "pending", at
	attempt.Executor = b.configuredExecutor(v)
	v.attempts = append(v.attempts, attempt)
	b.attempts[event.AttemptID] = attempt
	return attempt
}

func (b *builder) finishAttempt(attempt *attemptRec, event Event, at time.Time) {
	attempt.finished = true
	finished := at
	attempt.FinishedAt = &finished
	attempt.Execution = text(event.Data, "execution")
	attempt.Outcome = firstText(text(event.Data, "outcome"), text(event.Data, "status"), "pending")
	attempt.Verdict = text(event.Data, "verdict")
	executor := firstText(text(event.Data, "executor"), event.Executor)
	switch {
	case executor == "human":
		attempt.Executor = Executor{Kind: ExecutorHuman, Source: SourceAttempt,
			Actor: firstText(text(event.Data, "actor_id"), event.Actor), Agent: b.defs[attempt.visit.stage].Agent}
	case text(event.Data, "executor_kind") != "":
		kind := ExecutorInternalAgent
		if strings.HasPrefix(text(event.Data, "executor_kind"), "external") {
			kind = ExecutorExternalAgent
		}
		attempt.Executor = Executor{Kind: kind, Name: text(event.Data, "executor_name"), Source: SourceAttempt,
			Agent: b.defs[attempt.visit.stage].Agent}
	case executor == "agent":
		// An attempt recorded before executors were attested: the class is
		// known, the runtime or adapter is not.
		fallback := b.configuredExecutor(attempt.visit)
		if fallback.Kind == ExecutorHuman {
			fallback.Kind = ExecutorInternalAgent
		}
		fallback.Source = SourceConfigured
		attempt.Executor = fallback
	}
}

func (b *builder) transition(event Event, at time.Time) {
	from := firstText(text(event.Data, "from"), event.Stage)
	target := text(event.Data, "target")
	action := text(event.Data, "action")
	if from == "" || target == "" {
		return
	}
	v := b.openOf(from)
	if v == nil {
		return
	}
	if target == from {
		// The stage stays open for another attempt: not a new visit.
		return
	}
	backward := b.isBackward(from, target) || strings.HasPrefix(action, "return_to_")
	b.leave(v, at, target, action, backward)
	if workflow.IsTerminal(target) {
		return
	}
	via := EnteredFrom
	if backward {
		via = EnteredReturn
	}
	b.enter(target, at, via, from)
}

func (b *builder) isBackward(from, to string) bool {
	fromIndex, okFrom := b.index[from]
	toIndex, okTo := b.index[to]
	return okFrom && okTo && toIndex <= fromIndex
}

func (b *builder) gate(event Event) {
	id := text(event.Data, "gate_attempt_id")
	if id == "" {
		id = event.AttemptID
	}
	v := b.openOf(b.stageOf(event))
	if id == "" || v == nil {
		if existing := b.gates[id]; existing == nil {
			return
		}
	}
	record := b.gates[id]
	if record == nil {
		record = &gateRec{visit: v, order: b.gateOrder}
		b.gateOrder++
		b.gates[id] = record
	}
	record.name = firstText(text(event.Data, "gate_name"), record.name)
	record.execution = firstText(text(event.Data, "execution"), record.execution)
	record.verdict = text(event.Data, "verdict")
	record.mode = firstText(text(event.Data, "mode"), record.mode)
	record.waiting = event.Type == eventGateAttemptWaiting
	if attempt, ok := event.Data["attempt"].(map[string]any); ok {
		record.human = text(attempt, "executor") == string(workflow.GateKindHuman)
	}
	if event.Type == eventGateAttemptDone {
		if flagged, _ := event.Data["advisory_warning"].(bool); flagged && record.visit != nil {
			record.visit.Warnings++
		}
	}
}

// overlayApprovals applies the approval store: it knows a decision the journal
// only records when the run resumes.
func (b *builder) overlayApprovals() {
	if b.in.Approvals == nil {
		return
	}
	for _, value := range b.in.Approvals {
		if value.ID == "" {
			continue
		}
		if value.Status != "" && value.Status != "pending" {
			delete(b.approvals, value.ID)
			continue
		}
		existing := b.approvals[value.ID]
		if existing == nil {
			existing = &approvalRec{}
			b.approvals[value.ID] = existing
		}
		existing.Approval = value
		existing.Status = "pending"
	}
}

func (b *builder) configuredExecutor(v *visitRec) Executor {
	def := b.defs[v.stage]
	choice := v.override
	if choice == "" && b.in.Lifecycle != nil {
		if override, ok := b.in.Lifecycle.ExecutorOverrides[v.stage]; ok && v.open && override.ApprovalID == b.in.Lifecycle.PendingApprovalID {
			choice = override.Executor
		}
	}
	source := SourceConfigured
	kind := def.Executor
	if choice != "" {
		source, kind = SourceOverride, choice
	}
	if kind == "human" || (kind == "" && def.Agent == "") {
		return Executor{Kind: ExecutorHuman, Source: source, Agent: def.Agent}
	}
	if def.Adapter != "" {
		return Executor{Kind: ExecutorExternalAgent, Name: def.Adapter, Source: source, Agent: def.Agent}
	}
	return Executor{Kind: ExecutorInternalAgent, Source: source, Agent: def.Agent}
}

func (b *builder) terminalRun() bool {
	if b.finished || b.canceled {
		return true
	}
	switch b.in.RunStatus {
	case "completed", "completed_with_warnings", "failed", "canceled":
		return true
	}
	return b.in.Lifecycle != nil && b.in.Lifecycle.Phase == "terminal"
}

// liveRun: a worker is (or should be) executing; false for a run that is
// stopped, interrupted or blocked and waits for a person to resume it.
func (b *builder) liveRun() bool {
	switch b.in.RunStatus {
	case "stopped", "interrupted", "blocked":
		return false
	}
	return true
}

func (b *builder) project() Projection {
	terminal := b.terminalRun()
	if terminal && b.open != nil {
		b.open.closedByRun = true
		end := b.lastAt
		if end.IsZero() {
			end = b.open.EnteredAt
		}
		b.leave(b.open, end, "", "", false)
	}
	result := Projection{SchemaVersion: SchemaVersion, RunStatus: b.in.RunStatus, Terminal: terminal, Stages: make([]Stage, 0, len(b.in.Stages))}
	known := map[string]bool{}
	defs := append([]StageDef(nil), b.in.Stages...)
	for _, def := range defs {
		known[def.ID] = true
	}
	// A journal may name a stage the pinned template no longer lists; it is
	// still shown rather than silently dropped.
	var extra []string
	for stage := range b.visits {
		if !known[stage] {
			extra = append(extra, stage)
		}
	}
	sort.Strings(extra)
	for _, stage := range extra {
		defs = append(defs, StageDef{ID: stage})
		b.index[stage] = len(defs) - 1
		b.defs[stage] = StageDef{ID: stage}
	}
	for position, def := range defs {
		stage := b.stage(def, position, terminal)
		if stage.Current {
			result.CurrentStage = stage.StageID
		}
		result.Stages = append(result.Stages, stage)
	}
	return result
}

func (b *builder) stage(def StageDef, position int, terminal bool) Stage {
	stage := Stage{StageID: def.ID, Title: def.Title, Function: def.Function, Index: position + 1,
		Status: StatusNotReached, Readiness: ReadinessNotReached, Phase: PhaseNone, Outcome: "none",
		Executor: b.configuredExecutorFor(def), Visits: []Visit{}, AllowedActions: []string{}}
	records := b.visits[def.ID]
	if len(records) == 0 {
		return stage
	}
	stage.Visited, stage.VisitCount = true, len(records)
	for _, record := range records {
		b.finalize(record)
		visit := record.Visit
		visit.Active = record.open && !terminal
		visit.Attempts = make([]VisitAttempt, 0, len(record.attempts))
		for _, attempt := range record.attempts {
			visit.Attempts = append(visit.Attempts, attempt.VisitAttempt)
		}
		stage.Visits = append(stage.Visits, visit)
	}
	last := records[len(records)-1]
	stage.Executor, stage.Outcome = last.Executor, last.Outcome
	stage.Overridden, stage.Warnings = last.Overridden, last.Warnings
	if last.open && !terminal {
		stage.Current, stage.ActiveVisit = true, last.Number
		b.deriveCurrent(&stage, def, last)
		stage.AllowedActions = append([]string{}, b.in.AllowedActions...)
		if b.in.Lifecycle != nil && b.in.Lifecycle.NextStage != "" && b.in.Lifecycle.NextStage != def.ID {
			stage.ActionStageID = b.in.Lifecycle.NextStage
		}
		return stage
	}
	b.deriveLeft(&stage, last)
	return stage
}

func (b *builder) configuredExecutorFor(def StageDef) Executor {
	return b.configuredExecutor(&visitRec{stage: def.ID})
}

// finalize fixes the visit-level fields that depend on its attempts.
func (b *builder) finalize(v *visitRec) {
	v.Executor = b.configuredExecutor(v)
	if v.Executor.Kind == ExecutorHuman && v.owner != "" {
		v.Executor.Actor = v.owner
	}
	if len(v.attempts) == 0 {
		switch {
		case v.Skipped:
			v.Outcome = "skipped"
		case v.open:
			v.Outcome = "pending"
		case v.closedByRun && b.canceled:
			v.Outcome = "canceled"
		default:
			v.Outcome = "none"
		}
		return
	}
	last := v.attempts[len(v.attempts)-1]
	v.Executor = last.Executor
	v.Outcome = last.Outcome
	if !last.finished && !v.open {
		v.Outcome = "canceled"
	}
	v.Superseded = true
	for _, attempt := range v.attempts {
		if !attempt.Superseded {
			v.Superseded = false
		}
		if attempt.Outcome == "skipped" {
			v.Skipped = true
		}
	}
	if v.Skipped && last.Outcome == "skipped" {
		v.Outcome = "skipped"
	}
}

func (b *builder) deriveLeft(stage *Stage, v *visitRec) {
	stage.Readiness, stage.Phase = ReadinessLeft, PhaseNone
	if v.Superseded {
		// Its work was invalidated by a return: the stage has to be done
		// again. The history keeps what happened.
		stage.Status, stage.Readiness = StatusNotReached, ReadinessNotReached
		return
	}
	stage.Status = statusOfOutcome(v.Outcome)
	if stage.Status == StatusNotReached {
		stage.Readiness = ReadinessNotReached
	}
}

func statusOfOutcome(outcome string) Status {
	switch outcome {
	case "passed", "warning":
		return StatusPassed
	case "rejected":
		return StatusRejected
	case "failed":
		return StatusFailed
	case "blocked":
		return StatusBlocked
	case "skipped":
		return StatusSkipped
	case "canceled", "pending":
		return StatusCanceled
	default:
		return StatusNotReached
	}
}

// pendingFor lists the pending approvals of the stage raised in this visit.
func (b *builder) pendingFor(v *visitRec) []*approvalRec {
	var pending []*approvalRec
	for _, value := range b.approvals {
		if value.FromStage != v.stage || value.Status != "pending" {
			continue
		}
		if !value.at.IsZero() && !v.EnteredAt.IsZero() && value.at.Before(v.EnteredAt) {
			continue
		}
		pending = append(pending, value)
	}
	sort.Slice(pending, func(i, j int) bool {
		if !pending[i].at.Equal(pending[j].at) {
			return pending[i].at.Before(pending[j].at)
		}
		return pending[i].ID < pending[j].ID
	})
	return pending
}

func isHumanInput(value *approvalRec) bool {
	return value.Kind == "input" || value.Trigger == "human_input"
}

func (b *builder) deriveCurrent(stage *Stage, def StageDef, v *visitRec) {
	stage.Readiness = ReadinessEngaged
	var lastAttempt *attemptRec
	for _, attempt := range v.attempts {
		if !attempt.Superseded {
			lastAttempt = attempt
		}
	}
	stage.Outcome = "pending"
	if lastAttempt != nil {
		stage.Outcome = lastAttempt.Outcome
		stage.Executor = lastAttempt.Executor
	}
	pending := b.pendingFor(v)
	var input, decision *approvalRec
	for _, value := range pending {
		if isHumanInput(value) {
			if input == nil {
				input = value
			}
		} else if decision == nil {
			decision = value
		}
	}
	gatesRunning, gatesHuman, failed := b.gateState(v)

	switch {
	case lastAttempt != nil && !lastAttempt.finished:
		stage.Status, stage.Phase = StatusInProgress, PhaseWorking
		stage.Outcome = "pending"
		stage.Executor = lastAttempt.Executor
	case decision != nil:
		stage.Status, stage.Phase = StatusAwaitingHuman, PhaseAwaitingHuman
		stage.BlockedBy = blockOf(decision, failed)
	case gatesHuman:
		stage.Status, stage.Phase = StatusAwaitingHuman, PhaseAwaitingHuman
		stage.BlockedBy = &Block{Kind: BlockGate, Reason: "a human gate waits for a decision", Gates: gatesRunning}
	case gatesRunning != nil:
		stage.Status, stage.Phase = StatusChecking, PhaseChecking
		stage.BlockedBy = &Block{Kind: BlockGate, Reason: "gates are running", Gates: gatesRunning}
	case input != nil:
		b.deriveInput(stage, def, v, input)
	case lastAttempt != nil:
		b.deriveAfterAttempt(stage, lastAttempt)
	case b.in.RunStatus == "waiting_for_approval":
		stage.Status, stage.Phase = StatusAwaitingHuman, PhaseAwaitingHuman
		stage.BlockedBy = &Block{Kind: BlockApproval, Reason: "the stage waits for a decision"}
	default:
		stage.Status, stage.Readiness, stage.Phase = StatusReady, ReadinessReady, PhaseNone
		if !b.liveRun() {
			stage.BlockedBy = &Block{Kind: BlockRunState, Reason: "the task is " + b.in.RunStatus + " and waits to be resumed"}
		}
	}
}

func (b *builder) deriveInput(stage *Stage, def StageDef, v *visitRec, input *approvalRec) {
	executor := b.configuredExecutor(v)
	stage.Executor = executor
	if executor.Kind == ExecutorHuman && v.owner != "" && (v.ownerApproval == "" || v.ownerApproval == input.ID) {
		stage.Executor.Actor = v.owner
		stage.Status, stage.Phase = StatusInProgress, PhaseWorking
		return
	}
	stage.Status, stage.Readiness, stage.Phase = StatusReady, ReadinessReady, PhaseNone
	stage.BlockedBy = &Block{Kind: BlockHumanInput, ApprovalID: input.ID, Roles: input.RequiredRoles, Actions: input.Actions,
		Reason: "the stage waits for a person to take it and submit the result"}
	if executor.Kind != ExecutorHuman {
		stage.BlockedBy.Reason = "the stage waits for the agent to be started"
	}
}

func (b *builder) deriveAfterAttempt(stage *Stage, attempt *attemptRec) {
	switch attempt.Outcome {
	case "passed", "warning", "skipped", "pending", "":
		// The result is in; checks, gates and the transition follow. Without a
		// worker (stopped, interrupted) nothing is checking.
		if b.liveRun() && b.lifecycleRunning() {
			stage.Status, stage.Phase = StatusChecking, PhaseChecking
			return
		}
		stage.Status, stage.Readiness, stage.Phase = StatusReady, ReadinessReady, PhaseNone
		stage.BlockedBy = &Block{Kind: BlockRunState, Reason: "the task is " + firstText(b.in.RunStatus, "paused") + " and waits to be resumed"}
	default:
		stage.Status = statusOfOutcome(attempt.Outcome)
		stage.Phase = PhaseNone
		stage.BlockedBy = &Block{Kind: BlockRunState, Reason: "the last attempt ended as " + attempt.Outcome + "; the task waits to be resumed or returned"}
	}
}

// lifecycleRunning is false only when the checkpoint says the run is parked
// (waiting for a person or resumable); without a checkpoint the journal alone
// cannot tell, and a finished attempt without a decision is being checked.
func (b *builder) lifecycleRunning() bool {
	if b.in.Lifecycle == nil {
		return true
	}
	return b.in.Lifecycle.Phase == "running"
}

// gateState reports the gates of the open visit: the names still running, the
// running ones that wait for a person, and the required ones that failed.
func (b *builder) gateState(v *visitRec) (running []string, human bool, failed []string) {
	var records []*gateRec
	for _, record := range b.gates {
		if record.visit == v {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].order < records[j].order })
	latest := map[string]*gateRec{}
	for _, record := range records {
		latest[record.name] = record
	}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		record := latest[name]
		switch record.execution {
		case "pending", "running", "":
			running = append(running, name)
			if record.waiting && record.human {
				human = true
			}
		case "succeeded":
			if record.verdict == "fail" && record.mode != "advisory" {
				failed = append(failed, name)
			}
		case "error", "timeout":
			if record.mode != "advisory" {
				failed = append(failed, name)
			}
		}
	}
	return running, human, failed
}

func blockOf(value *approvalRec, failed []string) *Block {
	block := &Block{ApprovalID: value.ID, Roles: value.RequiredRoles, Actions: value.Actions}
	switch {
	case value.Kind == "check_decision" || value.Trigger == "stage_check":
		block.Kind = BlockCheckDecision
		block.Reason = "a check did not pass: a person decides to retry, return or override"
		block.Gates = failed
	case value.Kind == "grill_questions" || value.Trigger == "grill_questions":
		block.Kind = BlockGrill
		block.Reason = "the checker asks questions a person must answer"
	case value.Trigger == "delivery_plan":
		block.Kind = BlockDeliveryPlan
		block.Reason = "the delivery plan waits for approval"
	default:
		block.Kind = BlockApproval
		block.Reason = "the result waits for confirmation"
	}
	return block
}

func text(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func firstText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringsOf(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}
