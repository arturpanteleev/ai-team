// Package humandecision is the single service path of a human decision on a
// stage transition (the human_approval gate).
//
// Web, REST and the network MCP server do not decide approvals on their own:
// each authenticates the caller, builds an Actor and a Request and calls
// Service.Decide. The service applies, in one place and in one order:
//
//  1. the approval exists, is pending and is a transition decision;
//  2. the action is one of the approval's allowed actions;
//  3. the subject hash in the request equals the hash the approval is bound
//     to (the person confirms the exact result version, a stale view is
//     refused instead of silently deciding another version);
//  4. the actor holds one of the approval's required roles and has not voted
//     yet (quorum all needs distinct voters);
//  5. self-approval is forbidden when the pinned template says so;
//  6. the decision is stored and an audit record names the actor, the role,
//     the action, the hash and the channel.
//
// A redelivered request (same actor, action and hash, with every role of the
// actor already covered) is recognized: it does not create a second decision,
// and the audit records it as a replay. A person holding several required
// roles votes once per role: each call is counted in the next uncovered role
// (local mode: one owner closes a multi-role quorum all by calling once per
// role); once all the actor's roles are covered a different action gets
// ErrAlreadyVoted. Deciding is atomic by the approval store result: of two
// racing identical requests only the one the store applied writes AuditEvent.
package humandecision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

// AuditEvent is the type of the audit record written for every decision.
const AuditEvent = "human_gate_decided"

// ReplayAuditEvent is the audit record of a redelivered request that matched
// an earlier decision of the same person (no second decision is made).
const ReplayAuditEvent = "human_gate_decision_replayed"

// Channels name where a request came from; they are recorded, never trusted
// for authorization.
const (
	ChannelWeb  = "web"
	ChannelREST = "rest"
	ChannelMCP  = "mcp"
)

// Errors of the service. Each wraps a human-readable cause.
var (
	ErrNotFound         = errors.New("запрос решения не найден")
	ErrNotPending       = errors.New("запрос решения уже закрыт")
	ErrNotTransition    = errors.New("запрос не является решением о переходе этапа")
	ErrActionNotAllowed = errors.New("действие не разрешено для этого запроса")
	ErrHashRequired     = errors.New("требуется хеш предмета решения")
	ErrHashMismatch     = errors.New("хеш предмета решения не совпадает: результат изменился, обновите страницу")
	ErrRoleForbidden    = errors.New("роль не назначена на это решение")
	ErrAlreadyVoted     = errors.New("этот участник уже голосовал по запросу")
	ErrSelfApproval     = errors.New("самоподтверждение запрещено")
	ErrAuditUnavailable = errors.New("решение сохранено, но аудит не записан")
)

// Ports are the controller services the decision needs.
type Ports interface {
	// Approvals lists the approvals of a run.
	Approvals(runID string) ([]approval.PendingApproval, error)
	// Decide stores one decision (the approval store enforces quorum, exact
	// hash and reasons).
	Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
	// SelfApprovalViolation returns an error when actorID worked on the stage
	// of pending and the policy forbids confirming one's own work. An error
	// that is not a violation means the check could not be made and fails the
	// decision closed.
	SelfApprovalViolation(runID string, pending approval.PendingApproval, actorID string) error
	// PinRevisions returns the immutable artifact revisions the decision pins
	// (nil when the stage has none). The first vote pins them for later voters.
	PinRevisions(runID string, pending approval.PendingApproval) (map[string]string, error)
	// Audit durably records the decision. It must fail when the record cannot
	// be stored.
	Audit(runID, eventType string, at time.Time, data map[string]any) error
}

// Actor is the authenticated person who decides.
type Actor struct {
	ID string
	// HasRole reports whether the person holds the role (a required role of the
	// approval, in its stored spelling).
	HasRole func(role string) bool
	// Authenticated marks a decision made through the authenticated
	// controller (Web, REST, MCP) as opposed to a local operator.
	Authenticated bool
}

// Request is one decision.
type Request struct {
	RunID       string
	ApprovalID  string
	Action      string
	SubjectHash string
	Comment     string
	// Channel is web, rest or mcp.
	Channel string
	// RequestKey is the client's idempotency key, recorded in the audit.
	RequestKey string
	// ArtifactRevisions pins the reviewed revisions explicitly; empty means
	// the service pins what Ports.PinRevisions returns.
	ArtifactRevisions map[string]string
	// HashFromServer marks a request whose hash was filled in by the server
	// because an older client sent none; the audit records that the person did
	// not confirm the hash explicitly.
	HashFromServer bool
	Actor          Actor
}

// Result is the outcome of a decision.
type Result struct {
	Approval approval.PendingApproval
	// Role is the role the decision was made in.
	Role string
	// Resolved reports that the quorum is complete and the run may resume.
	Resolved bool
	// Replayed reports that an identical earlier decision of the same actor
	// was found: no second decision was stored; the redelivery is audited as
	// ReplayAuditEvent (not AuditEvent).
	Replayed bool
}

// Service decides approvals.
type Service struct {
	ports Ports
	now   func() time.Time
}

// New returns a Service over the ports.
func New(ports Ports) *Service {
	return &Service{ports: ports, now: func() time.Time { return time.Now().UTC() }}
}

// IsTransition reports whether the approval is a stage transition decision
// (as opposed to human input, a check decision, grill questions or the
// delivery plan).
func IsTransition(value approval.PendingApproval) bool {
	return (value.Kind == "" || value.Kind == approval.KindApprove) &&
		value.Trigger != "delivery_plan" && value.FromStage != "" && !value.Deferred
}

// Decide applies the decision. See the package comment for the order of checks.
func (s *Service) Decide(_ context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Actor.ID) == "" {
		return Result{}, errors.New("решение требует личности автора")
	}
	values, err := s.ports.Approvals(req.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("состояние запросов решения недоступно: %w", err)
	}
	var pending *approval.PendingApproval
	for index := range values {
		if values[index].ID == req.ApprovalID {
			pending = &values[index]
			break
		}
	}
	if pending == nil {
		return Result{}, ErrNotFound
	}
	if !IsTransition(*pending) {
		return Result{}, ErrNotTransition
	}
	if !contains(pending.Actions, req.Action) {
		return Result{}, fmt.Errorf("%w: %q, доступно %s", ErrActionNotAllowed, req.Action, strings.Join(pending.Actions, ", "))
	}
	hash := strings.ToLower(strings.TrimSpace(req.SubjectHash))
	if hash == "" {
		return Result{}, ErrHashRequired
	}
	if hash != pending.SubjectHash {
		return Result{}, ErrHashMismatch
	}
	if pending.Status != approval.StatusPending {
		// A closed approval still recognizes a redelivery of the same decision.
		if prior, ok := priorDecision(pending.Decisions, req.Actor.ID, req.Action, hash); ok {
			return s.replay(req, *pending, prior, hash)
		}
		return Result{}, ErrNotPending
	}
	// A person who holds several required roles votes once per role, each
	// time in the next role that is not covered yet (local mode: one owner
	// holds every role and closes a quorum all of several roles by calling
	// the decision once per role; the approval store enforces the same action
	// for quorum all). A role that is already covered is never voted again.
	role := ""
	if req.Actor.HasRole != nil {
		for _, required := range pending.RequiredRoles {
			if req.Actor.HasRole(required) && !roleVoted(pending.Decisions, required) {
				role = required
				break
			}
		}
	}
	if role == "" {
		// Every role of this actor is covered: the same decision again is a
		// redelivery (replay); a different one is an explicit refusal.
		if prior, ok := priorDecision(pending.Decisions, req.Actor.ID, req.Action, hash); ok {
			return s.replay(req, *pending, prior, hash)
		}
		for _, prior := range pending.Decisions {
			if prior.ActorID == req.Actor.ID {
				return Result{}, ErrAlreadyVoted
			}
		}
		return Result{}, ErrRoleForbidden
	}
	if req.Action == "approve" || req.Action == "approve_spec" {
		if err := s.ports.SelfApprovalViolation(req.RunID, *pending, req.Actor.ID); err != nil {
			// A violation and an unverifiable policy both refuse the decision.
			return Result{}, fmt.Errorf("%w: %v", ErrSelfApproval, err)
		}
	}
	revisions := req.ArtifactRevisions
	if len(revisions) == 0 {
		if len(pending.Decisions) > 0 {
			revisions = pending.Decisions[0].ArtifactRevisions
		} else if revisions, err = s.ports.PinRevisions(req.RunID, *pending); err != nil {
			return Result{}, fmt.Errorf("не удалось закрепить текущие версии артефактов: %w", err)
		}
	}
	decidedAt := s.now()
	resolved, err := s.ports.Decide(req.RunID, req.ApprovalID, approval.Decision{
		ActorID: req.Actor.ID, ActorRole: role, Action: req.Action, Comment: req.Comment, DecidedAt: decidedAt,
		SubjectHash: hash, ArtifactRevisions: copyMap(revisions),
		ControllerAuthenticated: req.Actor.Authenticated,
	})
	if err != nil {
		return Result{}, err
	}
	// The store treats an identical decision of the same actor and role as
	// idempotent and returns the stored one. Whether this call stored a new
	// decision is decided by the store result (our unique timestamp is in it),
	// not by the earlier read, so two racing identical requests audit once.
	if !decisionStored(resolved.Decisions, req.Actor.ID, role, decidedAt) {
		prior, _ := priorDecision(resolved.Decisions, req.Actor.ID, req.Action, hash)
		if prior.ActorRole == "" {
			prior.ActorRole = role
		}
		return s.replay(req, resolved, prior, hash)
	}
	result := Result{Approval: resolved, Role: role, Resolved: resolved.Status == approval.StatusResolved}
	data := map[string]any{
		"approval_id": resolved.ID, "stage": resolved.FromStage, "to_stage": resolved.ToStage,
		"trigger": resolved.Trigger, "action": req.Action, "target": resolved.Targets[req.Action],
		"actor_id": req.Actor.ID, "actor_role": role, "subject_hash": hash,
		"channel": req.Channel, "resolved": result.Resolved,
		"hash_confirmed_by_actor": !req.HashFromServer,
	}
	if req.RequestKey != "" {
		data["request_key"] = req.RequestKey
	}
	if resolved.ResolvedAction != "" {
		data["resolved_action"] = resolved.ResolvedAction
	}
	if err := s.ports.Audit(req.RunID, AuditEvent, s.now(), data); err != nil {
		return result, fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
	}
	return result, nil
}

// replay answers a redelivered request: nothing is stored again and the audit
// records the redelivery under ReplayAuditEvent.
func (s *Service) replay(req Request, pending approval.PendingApproval, prior approval.Decision, hash string) (Result, error) {
	result := Result{Approval: pending, Role: prior.ActorRole, Resolved: pending.Status == approval.StatusResolved, Replayed: true}
	data := map[string]any{
		"approval_id": pending.ID, "stage": pending.FromStage, "action": req.Action,
		"actor_id": req.Actor.ID, "actor_role": prior.ActorRole, "subject_hash": hash, "channel": req.Channel,
	}
	if req.RequestKey != "" {
		data["request_key"] = req.RequestKey
	}
	if err := s.ports.Audit(req.RunID, ReplayAuditEvent, s.now(), data); err != nil {
		return result, fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
	}
	return result, nil
}

func priorDecision(decisions []approval.Decision, actorID, action, hash string) (approval.Decision, bool) {
	for _, prior := range decisions {
		if prior.ActorID == actorID && prior.Action == action && prior.SubjectHash == hash {
			return prior, true
		}
	}
	return approval.Decision{}, false
}

func decisionStored(decisions []approval.Decision, actorID, role string, at time.Time) bool {
	for _, decision := range decisions {
		if decision.ActorID == actorID && decision.ActorRole == role && decision.DecidedAt.Equal(at) {
			return true
		}
	}
	return false
}

func roleVoted(decisions []approval.Decision, role string) bool {
	for _, decision := range decisions {
		if decision.ActorRole == role {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
