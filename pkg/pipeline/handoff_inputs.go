package pipeline

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

const maxQuestionAnswerRecordBytes = maxAnswerBytes + 128

// QuestionAnswerInputProvider obtains the worker-visible, read-only projection
// of a durable clarification decision. Cloud workers send only run/approval
// identity to the controller; the answer content is never sent by the worker.
type QuestionAnswerInputProvider interface {
	MaterializeQuestionAnswer(runID, approvalID string) (runtime.Artifact, error)
}

// ControllerQuestionAnswerStore owns canonical clarification answers outside
// worker-visible paths. Prepare derives bytes from the durable approval passed
// by the controller API after it reloads that approval from the approval store.
type ControllerQuestionAnswerStore struct{ TargetDir string }

var controllerQuestionAnswerWriteMu sync.Mutex

type questionAnswerPayload struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
}

func (s ControllerQuestionAnswerStore) root(create bool) (string, error) {
	if s.TargetDir == "" || !filepath.IsAbs(s.TargetDir) || filepath.Clean(s.TargetDir) != s.TargetDir {
		return "", errors.New("question answer target must be absolute and clean")
	}
	if create {
		return safeio.EnsureDir(s.TargetDir, ".ai-team", "state", "handoff-inputs")
	}
	return safeio.ExistingDir(s.TargetDir, ".ai-team", "state", "handoff-inputs")
}

func QuestionAnswerCanonicalPath(targetDir, runID, approvalID string) (string, error) {
	if err := validateQuestionAnswerIdentity(runID, approvalID); err != nil {
		return "", err
	}
	if targetDir == "" || !filepath.IsAbs(targetDir) || filepath.Clean(targetDir) != targetDir {
		return "", errors.New("question answer target must be absolute and clean")
	}
	return filepath.Join(targetDir, ".ai-team", "state", "handoff-inputs", runID, approvalID, "answer.md"), nil
}

// QuestionAnswerMaterializationPath is the exact file bind-mounted read-only
// into the worker-visible run inputs directory. It is only a projection of the
// canonical controller record; it is not an authority store.
func QuestionAnswerMaterializationPath(targetDir, runID, approvalID string) (string, error) {
	if err := validateQuestionAnswerIdentity(runID, approvalID); err != nil {
		return "", err
	}
	if targetDir == "" || !filepath.IsAbs(targetDir) || filepath.Clean(targetDir) != targetDir {
		return "", errors.New("question answer target must be absolute and clean")
	}
	return filepath.Join(targetDir, ".ai-team", "runs", runID, "inputs", approvalID+"-answer.md"), nil
}

func validateQuestionAnswerIdentity(runID, approvalID string) error {
	if err := evidence.ValidateRunID(runID); err != nil {
		return fmt.Errorf("question answer run id: %w", err)
	}
	if approvalID == "" || len(approvalID) > 128 || approvalID == "." || approvalID == ".." {
		return errors.New("question answer approval id is invalid")
	}
	for _, r := range approvalID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return errors.New("question answer approval id is invalid")
		}
	}
	return nil
}

// CanonicalQuestionAnswerContent validates and derives the only bytes accepted
// for a clarification input. Callers in the controller must pass the approval
// they reloaded from the durable approval store, never worker-supplied bytes.
func CanonicalQuestionAnswerContent(value approval.PendingApproval) ([]byte, error) {
	if err := validateQuestionAnswerIdentity(value.RunID, value.ID); err != nil {
		return nil, err
	}
	if value.SchemaVersion != approval.SchemaVersion || value.Status != approval.StatusResolved || value.ResolvedAt.IsZero() ||
		value.CreatedAt.IsZero() || value.ResolvedAt.Before(value.CreatedAt) || value.Deferred || value.AttemptID == "" || value.SubjectHash == "" || value.FromStage == "" ||
		value.Trigger != "graph_outcome:blocked" || value.ResolvedAction != "answer_questions" ||
		value.ToStage != value.FromStage || !containsApprovalRole(value.Actions, value.ResolvedAction) || value.Targets[value.ResolvedAction] != value.FromStage ||
		(value.Quorum != approval.QuorumAny && value.Quorum != approval.QuorumAll) || len(value.RequiredRoles) == 0 ||
		len(value.ArtifactRevisions) != 0 || value.ArtifactRevisionBindingSHA256 != "" {
		return nil, errors.New("approval is not a resolved stage clarification")
	}
	if len(value.SubjectHash) != hex.EncodedLen(32) {
		return nil, errors.New("clarification approval subject hash is invalid")
	}
	if _, err := hex.DecodeString(value.SubjectHash); err != nil {
		return nil, errors.New("clarification approval subject hash is invalid")
	}
	if value.CandidateSHA256 != "" {
		if len(value.CandidateSHA256) != hex.EncodedLen(32) {
			return nil, errors.New("clarification approval candidate hash is invalid")
		}
		if _, err := hex.DecodeString(value.CandidateSHA256); err != nil {
			return nil, errors.New("clarification approval candidate hash is invalid")
		}
	}
	var payload questionAnswerPayload
	if len(value.Payload) == 0 || strictjson.Unmarshal(value.Payload, maxQuestionBytes+1024, &payload) != nil ||
		payload.Kind != "questions" || strings.TrimSpace(payload.Markdown) == "" || len(payload.Markdown) > maxQuestionBytes {
		return nil, errors.New("clarification approval payload is invalid")
	}
	var resolved *approval.Decision
	for index := range value.Decisions {
		decision := &value.Decisions[index]
		if decision.Action != value.ResolvedAction {
			continue
		}
		if decision.ApprovalID != value.ID || decision.ActorID == "" || decision.SubjectHash != value.SubjectHash ||
			decision.DecidedAt.IsZero() || !containsApprovalRole(value.RequiredRoles, decision.ActorRole) {
			return nil, errors.New("clarification approval contains an invalid durable decision")
		}
		if decision.DecidedAt.Equal(value.ResolvedAt) {
			if resolved != nil {
				return nil, errors.New("clarification approval has ambiguous resolving decisions")
			}
			resolved = decision
		}
	}
	if resolved == nil {
		return nil, errors.New("clarification approval has no resolving durable decision")
	}
	if len(resolved.ArtifactRevisions) != 0 {
		return nil, errors.New("clarification answer decision must not bind artifact revisions")
	}
	answer := strings.TrimSpace(resolved.Comment)
	if answer == "" || len(answer) > maxAnswerBytes {
		return nil, errors.New("clarification approval has no valid durable answer")
	}
	return []byte("# Ответ на вопросы\n\n" + answer + "\n"), nil
}

// QuestionAnswerProvenance returns the stage and resolving actor recorded by
// the approval store. It intentionally does not infer business roles from
// stage names or hard-code a particular organization chart.
func QuestionAnswerProvenance(value approval.PendingApproval) (ClarificationProvenance, error) {
	if _, err := CanonicalQuestionAnswerContent(value); err != nil {
		return ClarificationProvenance{}, err
	}
	for _, decision := range value.Decisions {
		if decision.Action == value.ResolvedAction && decision.DecidedAt.Equal(value.ResolvedAt) {
			return ClarificationProvenance{Stage: value.FromStage, ActorID: decision.ActorID, ActorRole: decision.ActorRole}, nil
		}
	}
	return ClarificationProvenance{}, errors.New("clarification approval has no resolving actor provenance")
}

func containsApprovalRole(roles []string, role string) bool {
	for _, candidate := range roles {
		if candidate == role {
			return true
		}
	}
	return false
}

// Prepare writes the immutable canonical answer. Repeating the same durable
// approval is idempotent; an existing different or damaged record fails closed.
func (s ControllerQuestionAnswerStore) Prepare(value approval.PendingApproval) (string, error) {
	content, err := CanonicalQuestionAnswerContent(value)
	if err != nil {
		return "", err
	}
	controllerQuestionAnswerWriteMu.Lock()
	defer controllerQuestionAnswerWriteMu.Unlock()
	root, err := s.root(true)
	if err != nil {
		return "", err
	}
	dir, err := safeio.EnsureDir(root, value.RunID, value.ID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "answer.md")
	if existing, readErr := safeio.ReadRegularFile(path, maxQuestionAnswerRecordBytes); readErr == nil {
		if !bytes.Equal(existing, content) {
			return "", errors.New("conflicting canonical clarification input rejected")
		}
		return path, nil
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("read canonical clarification input: %w", readErr)
	}
	if err := safeio.WriteRegularFileNoFollow(path, content, safeio.ReadOnlyFileMode); err != nil {
		if existing, readErr := safeio.ReadRegularFile(path, maxQuestionAnswerRecordBytes); readErr == nil && bytes.Equal(existing, content) {
			return path, nil
		}
		return "", err
	}
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return path, errors.Join(syncErr, closeErr)
}

func (s ControllerQuestionAnswerStore) Read(runID, approvalID string) ([]byte, error) {
	root, err := s.root(false)
	if err != nil {
		return nil, err
	}
	if _, err := safeio.ExistingDir(root, runID, approvalID); err != nil {
		return nil, err
	}
	path, err := QuestionAnswerCanonicalPath(s.TargetDir, runID, approvalID)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
		return nil, errors.New("canonical clarification input is not a read-only regular file")
	}
	return safeio.ReadRegularFile(path, maxQuestionAnswerRecordBytes)
}

// ValidateCanonicalQuestionAnswer verifies that the immutable canonical bytes
// still match a fresh derivation from the durable approval before a worker API
// path is returned.
func (s ControllerQuestionAnswerStore) ValidateCanonicalQuestionAnswer(value approval.PendingApproval) (string, error) {
	expected, err := CanonicalQuestionAnswerContent(value)
	if err != nil {
		return "", err
	}
	path, err := QuestionAnswerCanonicalPath(s.TargetDir, value.RunID, value.ID)
	if err != nil {
		return "", err
	}
	stored, err := s.Read(value.RunID, value.ID)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(stored, expected) {
		return "", errors.New("canonical clarification input disagrees with durable approval")
	}
	return path, nil
}
