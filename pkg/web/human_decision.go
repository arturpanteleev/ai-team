package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/humandecision"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

// human_decision.go — the Web/REST face of the single human-decision service
// (pkg/humandecision). Every decision on a stage transition passes through
// it, so Web, REST and the MCP server give the same decision and the same
// audit record.

// webDecisionPorts adapts the server to humandecision.Ports.
type webDecisionPorts struct{ s *Server }

func (p webDecisionPorts) Approvals(runID string) ([]approval.PendingApproval, error) {
	if p.s.controller == nil {
		return nil, errors.New("task controller unavailable")
	}
	return p.s.controller.Approvals(runID)
}

func (p webDecisionPorts) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	return p.s.controller.Decide(runID, approvalID, decision)
}

func (p webDecisionPorts) SelfApprovalViolation(runID string, pending approval.PendingApproval, actorID string) error {
	return p.s.selfApprovalViolation(runID, pending, actorID)
}

func (p webDecisionPorts) PinRevisions(runID string, pending approval.PendingApproval) (map[string]string, error) {
	return p.s.latestAttemptRevisions(runID, pending.AttemptID)
}

func (p webDecisionPorts) Audit(runID, eventType string, at time.Time, data map[string]any) error {
	return p.s.appendRunEventRequired(runID, eventType, at, data)
}

// decodeOptionalCommand decodes a JSON command whose body may be empty.
func decodeOptionalCommand(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	return strictjson.Decode(bytes.NewReader(data), destination)
}

// decisionOrigin names the door a decision came through and the request key
// the client attached: MCP when the request was dispatched from the MCP
// server (the channel is derived from the transport, never from the request),
// otherwise the given browser or REST channel.
func decisionOrigin(r *http.Request, channel string) (string, string) {
	if meta, ok := mcpCallMetaFromRequest(r); ok {
		return humandecision.ChannelMCP, meta.Key
	}
	return channel, r.Header.Get("Idempotency-Key")
}

// decideTransition runs the service for an authenticated actor and writes the
// HTTP error itself when the decision is refused. The returned bool is false
// after an error response was written.
func (s *Server) decideTransition(w http.ResponseWriter, request humandecision.Request) (humandecision.Result, bool) {
	result, err := humandecision.New(webDecisionPorts{s}).Decide(context.Background(), request)
	if err == nil {
		return result, true
	}
	status := http.StatusConflict
	switch {
	case errors.Is(err, humandecision.ErrHashRequired):
		status = http.StatusBadRequest
	case errors.Is(err, humandecision.ErrRoleForbidden), errors.Is(err, humandecision.ErrSelfApproval):
		status = http.StatusForbidden
	case errors.Is(err, humandecision.ErrAuditUnavailable):
		// The decision itself is stored; repeating the request is safe and is
		// recorded as a replay.
		status = http.StatusServiceUnavailable
	}
	code := "decision_refused"
	switch {
	case errors.Is(err, humandecision.ErrSelfApproval):
		code = "self_approval_forbidden"
	case errors.Is(err, humandecision.ErrHashMismatch):
		code = "subject_hash_mismatch"
	case errors.Is(err, humandecision.ErrHashRequired):
		code = "subject_hash_required"
	case errors.Is(err, humandecision.ErrRoleForbidden):
		code = "forbidden"
	case errors.Is(err, humandecision.ErrAuditUnavailable):
		code = "audit_unavailable"
	}
	writeJSONAPIError(w, status, err.Error(), code)
	return humandecision.Result{}, false
}
