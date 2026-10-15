// Package stagestatus is the one server-side projection of where every stage
// of a task stands (S11). It is a pure function of the durable facts of a run:
// the append-only event journal, the approvals, the lifecycle checkpoint and
// the pinned template. Nothing in it is stored or editable: the same journal
// always yields the same projection, so a reload or a restart cannot change it,
// and no API accepts a status.
//
// The projection keeps five questions apart, because collapsing them is what
// made the old "running" label lie:
//
//   - readiness: has the stage been reached, is it open for work, was it left;
//   - phase: what is happening in the open visit (working, checking, a person
//     is awaited);
//   - executor: who does the work (a person, an internal agent, an external
//     agent) and who actually did it, as the attempt evidence records it;
//   - outcome: how the visit ended (passed, rejected, blocked, skipped, ...);
//   - visits: the history of times the stage was entered. The current stage
//     and the history are different data: leaving a stage makes its visit
//     inactive, returning starts a new visit, and the earlier attempts stay.
//
// Status is the single human label derived from the first four.
package stagestatus

import "time"

// SchemaVersion is the version of the JSON shape below.
const SchemaVersion = 1

// Status is the one label of a stage.
type Status string

const (
	// StatusNotReached: the stage has not been reached (or its earlier visit
	// was invalidated by a return and it has to be done again).
	StatusNotReached Status = "not_reached"
	// StatusReady: the stage is current and open for work; nobody started yet.
	StatusReady Status = "ready"
	// StatusInProgress: a person, an internal agent or an external agent works.
	StatusInProgress Status = "in_progress"
	// StatusChecking: the result is being checked (stage checks, gates). It is
	// never reported as plain "running".
	StatusChecking Status = "checking"
	// StatusAwaitingHuman: the work waits for a person's decision, answer or
	// confirmation. It is not "running" and not "ready".
	StatusAwaitingHuman Status = "awaiting_human"
	// StatusPassed: the stage passed (possibly with a recorded override).
	StatusPassed Status = "passed"
	// StatusRejected: the result was rejected (negative verdict).
	StatusRejected Status = "rejected"
	// StatusFailed: the attempt failed technically.
	StatusFailed Status = "failed"
	// StatusBlocked: the stage reported a blocker.
	StatusBlocked Status = "blocked"
	// StatusSkipped: the stage was skipped by an explicit, reasoned decision.
	StatusSkipped Status = "skipped"
	// StatusCanceled: the run was canceled while the stage was open.
	StatusCanceled Status = "canceled"
)

// Readiness is whether the stage is open for work.
type Readiness string

const (
	ReadinessNotReached Readiness = "not_reached"
	ReadinessReady      Readiness = "ready"
	ReadinessEngaged    Readiness = "engaged"
	ReadinessLeft       Readiness = "left"
)

// Phase is what is happening in the open visit.
type Phase string

const (
	PhaseNone          Phase = "none"
	PhaseWorking       Phase = "working"
	PhaseChecking      Phase = "checking"
	PhaseAwaitingHuman Phase = "awaiting_human"
)

// ExecutorKind says who does the work.
type ExecutorKind string

const (
	ExecutorHuman         ExecutorKind = "human"
	ExecutorInternalAgent ExecutorKind = "internal_agent"
	ExecutorExternalAgent ExecutorKind = "external_agent"
	ExecutorUnknown       ExecutorKind = "unknown"
)

// Executor sources: Source says how sure the kind is.
const (
	// SourceAttempt: recorded by the attempt evidence (the actual executor).
	SourceAttempt = "attempt"
	// SourceConfigured: the template says so; nothing has run yet.
	SourceConfigured = "configured"
	// SourceOverride: a person switched the executor of this visit.
	SourceOverride = "override"
)

// Executor is who does or did the work.
type Executor struct {
	Kind ExecutorKind `json:"kind"`
	// Name is the runtime type of an internal agent or the adapter of an
	// external one; empty for a person.
	Name string `json:"name,omitempty"`
	// Agent is the template agent of the stage.
	Agent string `json:"agent,omitempty"`
	// Actor is the person who took or did the stage.
	Actor  string `json:"actor,omitempty"`
	Source string `json:"source"`
}

// Block kinds name the source of a block.
const (
	BlockHumanInput    = "human_input"
	BlockApproval      = "approval"
	BlockCheckDecision = "check_decision"
	BlockGrill         = "grill_questions"
	BlockDeliveryPlan  = "delivery_plan"
	BlockGate          = "gate"
	BlockRunState      = "run_state"
)

// Block says what the current stage waits for.
type Block struct {
	Kind       string   `json:"kind"`
	Reason     string   `json:"reason,omitempty"`
	ApprovalID string   `json:"approval_id,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	Gates      []string `json:"gates,omitempty"`
	Actions    []string `json:"actions,omitempty"`
}

// VisitAttempt is one stage attempt inside a visit. It is never dropped: an
// invalidated attempt stays with Superseded set.
type VisitAttempt struct {
	AttemptID  string     `json:"attempt_id"`
	Executor   Executor   `json:"executor"`
	Execution  string     `json:"execution,omitempty"`
	Outcome    string     `json:"outcome"`
	Verdict    string     `json:"verdict,omitempty"`
	Superseded bool       `json:"superseded,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Visit entry kinds.
const (
	EnteredStart    = "start"
	EnteredFrom     = "transition"
	EnteredReturn   = "return"
	EnteredInferred = "inferred"
)

// Visit is one time the stage was entered. Number is 1-based per stage.
type Visit struct {
	Number int `json:"number"`
	// Active is true only for the open visit of the current stage. A left
	// visit is inactive for good; a return starts a new visit.
	Active      bool       `json:"active"`
	EnteredAt   time.Time  `json:"entered_at"`
	EnteredFrom string     `json:"entered_from,omitempty"`
	EnteredVia  string     `json:"entered_via"`
	LeftAt      *time.Time `json:"left_at,omitempty"`
	LeftTo      string     `json:"left_to,omitempty"`
	LeftAction  string     `json:"left_action,omitempty"`
	// Returned is true when the visit was left by moving back in the route.
	Returned bool `json:"returned,omitempty"`
	// Outcome is how the visit ended (the outcome of its last attempt as the
	// evidence recorded it); pending while it is open.
	Outcome string `json:"outcome"`
	// Superseded: every attempt of the visit was invalidated by a later
	// return. The outcome above is still what it was when the visit ended.
	Superseded bool           `json:"superseded,omitempty"`
	Overridden bool           `json:"overridden,omitempty"`
	Skipped    bool           `json:"skipped,omitempty"`
	Warnings   int            `json:"warnings,omitempty"`
	Executor   Executor       `json:"executor"`
	Attempts   []VisitAttempt `json:"attempts"`
}

// Stage is the state of one template stage.
type Stage struct {
	StageID  string `json:"stage_id"`
	Title    string `json:"title,omitempty"`
	Function string `json:"function,omitempty"`
	Index    int    `json:"index"`

	Status    Status    `json:"status"`
	Readiness Readiness `json:"readiness"`
	Phase     Phase     `json:"phase"`
	// Outcome is the outcome of the open visit (pending) or of the latest one.
	Outcome  string   `json:"outcome"`
	Executor Executor `json:"executor"`
	// Current is true for the stage the task is at right now.
	Current bool `json:"current"`
	// Visited is true when the stage was ever active, also when it is not now.
	Visited     bool    `json:"visited"`
	VisitCount  int     `json:"visit_count"`
	ActiveVisit int     `json:"active_visit"`
	Visits      []Visit `json:"visits"`
	// Overridden is true when a failed check of the latest visit was bypassed.
	Overridden bool `json:"overridden,omitempty"`
	Warnings   int  `json:"warnings,omitempty"`
	// BlockedBy and AllowedActions are only set on the current stage.
	BlockedBy      *Block   `json:"blocked_by,omitempty"`
	AllowedActions []string `json:"allowed_actions"`
	// ActionStageID names the stage the action routes address when it is not
	// this one. A confirmation of a stage result is a decision on the move to
	// the next stage, so the task checkpoint (and the action routes) already
	// name that next stage while this stage is the one that awaits a person.
	ActionStageID string `json:"action_stage_id,omitempty"`
}

// Projection is the state of all stages of one task.
type Projection struct {
	SchemaVersion int    `json:"schema_version"`
	RunStatus     string `json:"run_status,omitempty"`
	// Terminal is true when the task has ended: no stage is current.
	Terminal     bool    `json:"terminal"`
	CurrentStage string  `json:"current_stage,omitempty"`
	Stages       []Stage `json:"stages"`
}

// RouteItem is the compact form of a stage for lists and cards.
type RouteItem struct {
	StageID    string    `json:"stage_id"`
	Status     Status    `json:"status"`
	Visited    bool      `json:"visited"`
	VisitCount int       `json:"visit_count"`
	Current    bool      `json:"current,omitempty"`
	Readiness  Readiness `json:"readiness"`
}

// Summary is the compact projection of a task for list cards: the current
// stage in full and every other stage as a route item.
type Summary struct {
	SchemaVersion int       `json:"schema_version"`
	Terminal      bool      `json:"terminal"`
	CurrentStage  string    `json:"current_stage,omitempty"`
	Status        Status    `json:"status,omitempty"`
	Readiness     Readiness `json:"readiness,omitempty"`
	Phase         Phase     `json:"phase,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	Executor      *Executor `json:"executor,omitempty"`
	Visit         int       `json:"visit,omitempty"`
	VisitCount    int       `json:"visit_count,omitempty"`
	// Returned is true when the current stage was entered by a return.
	Returned  bool        `json:"returned,omitempty"`
	BlockedBy *Block      `json:"blocked_by,omitempty"`
	Route     []RouteItem `json:"route"`
}

// Summary compacts the projection for a list card.
func (p Projection) Summary() Summary {
	summary := Summary{SchemaVersion: p.SchemaVersion, Terminal: p.Terminal, CurrentStage: p.CurrentStage, Route: make([]RouteItem, 0, len(p.Stages))}
	for _, stage := range p.Stages {
		summary.Route = append(summary.Route, RouteItem{
			StageID: stage.StageID, Status: stage.Status, Visited: stage.Visited, VisitCount: stage.VisitCount,
			Current: stage.Current, Readiness: stage.Readiness,
		})
		if !stage.Current {
			continue
		}
		executor := stage.Executor
		summary.Status, summary.Readiness, summary.Phase, summary.Outcome = stage.Status, stage.Readiness, stage.Phase, stage.Outcome
		summary.Executor, summary.Visit, summary.VisitCount, summary.BlockedBy = &executor, stage.ActiveVisit, stage.VisitCount, stage.BlockedBy
		if stage.ActiveVisit > 0 && stage.ActiveVisit <= len(stage.Visits) {
			summary.Returned = stage.Visits[stage.ActiveVisit-1].EnteredVia == EnteredReturn
		}
	}
	return summary
}

// StageDef is the part of a template stage the projection needs.
type StageDef struct {
	ID       string
	Title    string
	Function string
	// Executor is the configured executor class: human or agent.
	Executor string
	Agent    string
	// Adapter names an external executor of an agent stage.
	Adapter string
}

// Event is one entry of the durable event journal.
type Event struct {
	Sequence  int64
	Type      string
	Stage     string
	AttemptID string
	Actor     string
	Executor  string
	Timestamp time.Time
	Data      map[string]any
}

// Approval is the durable state of one approval as the approval store has it.
type Approval struct {
	ID            string
	Kind          string
	Trigger       string
	FromStage     string
	ToStage       string
	Status        string
	RequiredRoles []string
	Actions       []string
}

// Lifecycle is the mutable checkpoint of the run.
type Lifecycle struct {
	Phase             string
	NextStage         string
	PendingApprovalID string
	// ExecutorOverrides maps a stage to the executor a person chose for the
	// visit bound by the approval ID.
	ExecutorOverrides map[string]LifecycleOverride
}

// LifecycleOverride is a person's executor choice for one stage visit.
type LifecycleOverride struct {
	Executor   string
	ApprovalID string
}

// Input is everything the projection reads.
type Input struct {
	// Entry is the first stage of the route; the first stage when empty.
	Entry  string
	Stages []StageDef
	Events []Event
	// Approvals, when not nil, is authoritative for the approvals it lists
	// (the journal can lag behind a decision that waits for a resume).
	Approvals []Approval
	Lifecycle *Lifecycle
	// RunStatus is the status of the run as the run record has it.
	RunStatus string
	// AllowedActions are the actions open on the current stage; the projection
	// only carries them next to the status.
	AllowedActions []string
}
