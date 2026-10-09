package humanartifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

type approvalResultRecord struct {
	Kind        string    `json:"kind"`
	StageID     string    `json:"stage_id"`
	Action      string    `json:"action"`
	ActorID     string    `json:"actor_id"`
	ActorRole   string    `json:"actor_role"`
	Comment     string    `json:"comment,omitempty"`
	Description string    `json:"description,omitempty"`
	At          time.Time `json:"at"`
}

// ApprovalResultContent returns the exact immutable output for a human approve
// result. Controller manifest validation uses the same canonical encoding as
// the pipeline writer so the resolved decision and artifact cannot diverge.
func ApprovalResultContent(stageID string, decision approval.Decision) ([]byte, error) {
	data, err := json.Marshal(approvalResultRecord{
		Kind: "human_stage_result", StageID: stageID, Action: decision.Action,
		ActorID: decision.ActorID, ActorRole: decision.ActorRole, Comment: decision.Comment,
		Description: decision.Description, At: decision.DecidedAt,
	})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

type InputApprovalController interface {
	Approvals(runID string) ([]approval.PendingApproval, error)
	Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
}

// SubmissionCommand is the transport-neutral body for a human stage result.
// Content is markdown, a URL, or optional approve text according to Result.
type SubmissionCommand struct {
	StageID                 string
	Result                  string
	LinkKind                string
	Content                 string
	Note                    string
	Description             string
	Action                  string
	ActorID                 string
	ActorRole               string
	ControllerAuthenticated bool
}

type SubmittedResult struct {
	Approval approval.PendingApproval `json:"approval"`
	Revision Revision                 `json:"revision"`
}

// Submit resolves exactly the pending input for the requested stage and
// records each accepted result as an immutable stage-scoped revision.
func (s *Store) Submit(controller InputApprovalController, runID string, command SubmissionCommand) (SubmittedResult, error) {
	runID, command.StageID = strings.TrimSpace(runID), strings.TrimSpace(command.StageID)
	command.ActorID, command.ActorRole = strings.TrimSpace(command.ActorID), strings.TrimSpace(command.ActorRole)
	if controller == nil || !safeName(runID) || !safeName(command.StageID) || command.ActorID == "" || command.ActorRole == "" {
		return SubmittedResult{}, errors.New("run, stage, actor and role are required")
	}
	values, err := controller.Approvals(runID)
	if err != nil {
		return SubmittedResult{}, fmt.Errorf("read stage approvals: %w", err)
	}
	var selected *approval.PendingApproval
	for index := range values {
		value := &values[index]
		if value.Kind != approval.KindInput || value.Trigger != "human_input" || value.FromStage != command.StageID || value.ToStage != command.StageID {
			continue
		}
		if value.Status == approval.StatusPending {
			if selected != nil {
				return SubmittedResult{}, errors.New("more than one pending human input exists for this stage")
			}
			selected = value
		}
	}
	if selected == nil {
		// An exact retry after the stage has resumed remains idempotent. Select
		// the newest resolved human input for the stage; Decide verifies that
		// actor, action, description and bytes are identical.
		for index := range values {
			value := &values[index]
			if value.Kind == approval.KindInput && value.Trigger == "human_input" && value.FromStage == command.StageID &&
				value.ToStage == command.StageID && value.Status == approval.StatusResolved &&
				(selected == nil || value.CreatedAt.After(selected.CreatedAt)) {
				selected = value
			}
		}
	}
	if selected == nil {
		return SubmittedResult{}, fmt.Errorf("no pending human input for stage %q", command.StageID)
	}
	var payload approval.InputPayload
	if err := json.Unmarshal(selected.Payload, &payload); err != nil || payload.Kind != string(approval.KindInput) || payload.StageID != command.StageID {
		return SubmittedResult{}, errors.New("human input payload does not match requested stage")
	}
	if payload.Result != command.Result || payload.LinkKind != command.LinkKind {
		return SubmittedResult{}, errors.New("submission type or link kind does not match the configured stage")
	}
	if len(command.Description) > MaxCommentBytes || len(command.Note) > MaxCommentBytes {
		return SubmittedResult{}, fmt.Errorf("description and note are limited to %d bytes", MaxCommentBytes)
	}
	action := command.Action
	content := command.Content
	switch payload.Result {
	case "md", "link":
		if action == "" {
			action = "submit"
		}
		if action != "submit" {
			return SubmittedResult{}, errors.New("markdown and link results require the submit action")
		}
		if err := ValidateSubmission(payload.Result, payload.LinkKind, content); err != nil {
			return SubmittedResult{}, err
		}
	case "approve":
		if action == "" {
			action = "approve"
		}
		if action != "approve" {
			return SubmittedResult{}, errors.New("approve result requires the approve action")
		}
		content = command.Note
		if err := ValidateSubmission(payload.Result, payload.LinkKind, content); err != nil {
			return SubmittedResult{}, err
		}
	default:
		return SubmittedResult{}, fmt.Errorf("unsupported human result type %q", payload.Result)
	}
	if !containsRole(selected.RequiredRoles, command.ActorRole) {
		return SubmittedResult{}, errors.New("actor role is not assigned to this stage")
	}
	legacyRetry := false
	expectedRevision := 0
	if selected.Status == approval.StatusResolved {
		if len(selected.Decisions) == 0 {
			return SubmittedResult{}, errors.New("resolved human input has no decision")
		}
		previous := selected.Decisions[len(selected.Decisions)-1]
		if previous.ActorID != command.ActorID || previous.ActorRole != command.ActorRole ||
			previous.Action != action || previous.Comment != content || previous.Description != command.Description {
			return SubmittedResult{}, errors.New("submission conflict: resolved approval contains different actor or bytes")
		}
		legacyRetry = previous.SubmissionVersion == 0 && previous.ContentSHA256 == ""
		expectedRevision = previous.SubmissionVersion
		if !legacyRetry && (previous.ContentSHA256 != Digest([]byte(content)) || previous.SubmissionVersion < 1) {
			return SubmittedResult{}, errors.New("resolved approval submission metadata does not match content")
		}
	}
	revision, err := s.AppendSubmission(runID, payload.StageID, selected.ID, payload.Result, payload.LinkKind,
		content, command.Description, command.Note, command.ActorID)
	if err != nil {
		return SubmittedResult{}, err
	}
	if selected.Status == approval.StatusResolved && !legacyRetry && revision.Revision != expectedRevision {
		return SubmittedResult{}, errors.New("resolved approval revision does not match immutable submission history")
	}
	decision := approval.Decision{
		ActorID: command.ActorID, ActorRole: command.ActorRole, Action: action, Comment: content,
		Description: command.Description, SubmissionVersion: revision.Revision,
		ContentSHA256: revision.SHA256, SubjectHash: selected.SubjectHash,
		ControllerAuthenticated: command.ControllerAuthenticated,
	}
	// B-30 input decisions predate stage revisions. Preserve exact retries of
	// those records without trying to retrofit metadata into a resolved vote.
	if legacyRetry {
		return SubmittedResult{Approval: *selected, Revision: revision}, nil
	}
	resolved, err := controller.Decide(runID, selected.ID, decision)
	if err != nil {
		return SubmittedResult{}, err
	}
	return SubmittedResult{Approval: resolved, Revision: revision}, nil
}

func containsRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}
