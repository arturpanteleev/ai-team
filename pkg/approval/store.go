// Package approval хранит обязательные человеческие решения о переходах
// пайплайна отдельно от неизменяемого журнала evidence.
package approval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
)

const SchemaVersion = 1

type Status string

const (
	StatusPending  Status = "pending"
	StatusResolved Status = "resolved"
	QuorumAny             = "any"
	QuorumAll             = "all"
)

type Decision struct {
	ApprovalID  string    `json:"approval_id"`
	ActorID     string    `json:"actor_id"`
	ActorRole   string    `json:"actor_role"`
	Action      string    `json:"action"`
	Comment     string    `json:"comment,omitempty"`
	SubjectHash string    `json:"subject_hash"`
	DecidedAt   time.Time `json:"decided_at"`
}

type PendingApproval struct {
	SchemaVersion   int               `json:"schema_version"`
	ID              string            `json:"id"`
	RunID           string            `json:"run_id"`
	AttemptID       string            `json:"attempt_id"`
	FromStage       string            `json:"from_stage"`
	ToStage         string            `json:"to_stage"`
	Trigger         string            `json:"trigger"`
	SubjectHash     string            `json:"subject_hash"`
	CandidateSHA256 string            `json:"candidate_sha256,omitempty"`
	RequiredRoles   []string          `json:"required_roles"`
	Quorum          string            `json:"quorum"`
	Actions         []string          `json:"actions"`
	Targets         map[string]string `json:"targets"`
	Status          Status            `json:"status"`
	Decisions       []Decision        `json:"decisions,omitempty"`
	ResolvedAction  string            `json:"resolved_action,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	ResolvedAt      time.Time         `json:"resolved_at,omitempty"`
	// Payload содержит машиночитаемое представление subject (например,
	// canonical JSON delivery plan) для осознанного решения без доступа к
	// filesystem. Не является частью identity: subject уже зафиксирован hash.
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Store struct {
	root string
	// mu сериализует read-modify-write циклы внутри одного процесса; между
	// процессами сериализует lockRun (flock на run-каталоге).
	mu sync.Mutex
}

func NewStore(target string) (*Store, error) {
	root, err := safeio.EnsureDir(target, ".ai-team", "state", "approvals")
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// NewID возвращает стабильный идентификатор конкретного subject перехода.
func NewID(runID, attemptID, fromStage, toStage, trigger, subjectHash string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		runID, attemptID, fromStage, toStage, trigger, subjectHash,
	}, "\x00")))
	return "approval-" + hex.EncodeToString(sum[:12])
}

func (s *Store) Create(value PendingApproval) (PendingApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockRun(value.RunID)
	if err != nil {
		return PendingApproval{}, err
	}
	defer unlock()
	value.SchemaVersion = SchemaVersion
	value.Status = StatusPending
	value.ResolvedAction = ""
	value.ResolvedAt = time.Time{}
	value.Decisions = nil
	if value.Quorum == "" {
		value.Quorum = QuorumAny
	}
	if value.ID == "" {
		value.ID = NewID(value.RunID, value.AttemptID, value.FromStage, value.ToStage, value.Trigger, value.SubjectHash)
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	} else {
		value.CreatedAt = value.CreatedAt.UTC()
	}
	normalize(&value)
	if err := validate(value); err != nil {
		return PendingApproval{}, err
	}
	path, err := s.path(value.RunID, value.ID)
	if err != nil {
		return PendingApproval{}, err
	}
	if existing, loadErr := s.Load(value.RunID, value.ID); loadErr == nil {
		if sameRequest(existing, value) {
			return existing, nil
		}
		return PendingApproval{}, fmt.Errorf("approval %s уже существует с другим subject", value.ID)
	} else if !os.IsNotExist(loadErr) {
		return PendingApproval{}, loadErr
	}
	if err := s.write(path, value); err != nil {
		return PendingApproval{}, err
	}
	return value, nil
}

func (s *Store) Load(runID, approvalID string) (PendingApproval, error) {
	path, err := s.path(runID, approvalID)
	if err != nil {
		return PendingApproval{}, err
	}
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return PendingApproval{}, err
	}
	var value PendingApproval
	if err := strictjson.Unmarshal(data, 1<<20, &value); err != nil {
		return PendingApproval{}, fmt.Errorf("approval %s: %w", approvalID, err)
	}
	if err := validate(value); err != nil {
		return PendingApproval{}, err
	}
	if value.RunID != runID || value.ID != approvalID {
		return PendingApproval{}, errors.New("approval identity mismatch")
	}
	return value, nil
}

func (s *Store) List(runID string) ([]PendingApproval, error) {
	if !safeName(runID) {
		return nil, errors.New("недопустимый run_id")
	}
	directory := filepath.Join(s.root, runID)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []PendingApproval{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := make([]PendingApproval, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		value, err := s.Load(runID, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values, nil
}

func (s *Store) Decide(runID, approvalID string, decision Decision) (PendingApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockRun(runID)
	if err != nil {
		return PendingApproval{}, err
	}
	defer unlock()
	value, err := s.Load(runID, approvalID)
	if err != nil {
		return PendingApproval{}, err
	}
	decision.ApprovalID = approvalID
	decision.ActorID = strings.TrimSpace(decision.ActorID)
	decision.ActorRole = strings.TrimSpace(decision.ActorRole)
	decision.Action = strings.TrimSpace(decision.Action)
	decision.SubjectHash = strings.ToLower(strings.TrimSpace(decision.SubjectHash))
	decision.Comment = strings.TrimSpace(decision.Comment)
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	} else {
		decision.DecidedAt = decision.DecidedAt.UTC()
	}
	if decision.SubjectHash != value.SubjectHash {
		return PendingApproval{}, errors.New("subject hash решения не совпадает с ожидаемым")
	}
	if decision.ActorID == "" || !contains(value.RequiredRoles, decision.ActorRole) || !contains(value.Actions, decision.Action) {
		return PendingApproval{}, errors.New("решение содержит недопустимого actor, role или action")
	}
	for _, previous := range value.Decisions {
		if previous.ActorID == decision.ActorID && previous.ActorRole == decision.ActorRole {
			if previous.Action == decision.Action && previous.SubjectHash == decision.SubjectHash {
				return value, nil
			}
			return PendingApproval{}, errors.New("actor уже записал конфликтующее решение")
		}
	}
	if value.Status == StatusResolved {
		return PendingApproval{}, errors.New("approval уже разрешён")
	}
	if value.Quorum == QuorumAll {
		for _, previous := range value.Decisions {
			if previous.Action != decision.Action {
				return PendingApproval{}, errors.New("для quorum all решения должны иметь одинаковый action")
			}
		}
	}
	value.Decisions = append(value.Decisions, decision)
	if value.Quorum == QuorumAny || coversAllRoles(value.RequiredRoles, value.Decisions) {
		value.Status = StatusResolved
		value.ResolvedAction = decision.Action
		value.ResolvedAt = decision.DecidedAt
	}
	if err := validate(value); err != nil {
		return PendingApproval{}, err
	}
	path, err := s.path(runID, approvalID)
	if err != nil {
		return PendingApproval{}, err
	}
	if err := s.write(path, value); err != nil {
		return PendingApproval{}, err
	}
	return value, nil
}

func normalize(value *PendingApproval) {
	value.SubjectHash = strings.ToLower(strings.TrimSpace(value.SubjectHash))
	for index := range value.RequiredRoles {
		value.RequiredRoles[index] = strings.TrimSpace(value.RequiredRoles[index])
	}
	for index := range value.Actions {
		value.Actions[index] = strings.TrimSpace(value.Actions[index])
	}
	sort.Strings(value.RequiredRoles)
	sort.Strings(value.Actions)
}

func validate(value PendingApproval) error {
	if value.SchemaVersion != SchemaVersion || !safeName(value.ID) || !safeName(value.RunID) ||
		value.AttemptID == "" || value.FromStage == "" || value.ToStage == "" || value.Trigger == "" ||
		!validSHA256(value.SubjectHash) || value.CreatedAt.IsZero() {
		return errors.New("approval содержит недопустимые обязательные поля")
	}
	if value.CandidateSHA256 != "" && !validSHA256(value.CandidateSHA256) {
		return errors.New("approval содержит недопустимый candidate hash")
	}
	if len(value.Payload) > 0 {
		var payload any
		if json.Unmarshal(value.Payload, &payload) != nil {
			return errors.New("approval payload должен быть валидным JSON")
		}
	}
	if value.Quorum != QuorumAny && value.Quorum != QuorumAll {
		return fmt.Errorf("неподдерживаемый approval quorum %q", value.Quorum)
	}
	if !uniqueNonEmpty(value.RequiredRoles) || !uniqueNonEmpty(value.Actions) {
		return errors.New("approval roles/actions должны быть непустыми и уникальными")
	}
	for _, action := range value.Actions {
		if strings.TrimSpace(value.Targets[action]) == "" {
			return fmt.Errorf("approval action %s не имеет target", action)
		}
	}
	for _, decision := range value.Decisions {
		if decision.ApprovalID != value.ID || decision.ActorID == "" ||
			!contains(value.RequiredRoles, decision.ActorRole) || !contains(value.Actions, decision.Action) ||
			decision.SubjectHash != value.SubjectHash || decision.DecidedAt.IsZero() {
			return errors.New("approval содержит недопустимое решение")
		}
	}
	switch value.Status {
	case StatusPending:
		if value.ResolvedAction != "" || !value.ResolvedAt.IsZero() {
			return errors.New("pending approval не может содержать resolved outcome")
		}
	case StatusResolved:
		if !contains(value.Actions, value.ResolvedAction) || value.ResolvedAt.IsZero() || len(value.Decisions) == 0 {
			return errors.New("resolved approval не содержит итогового решения")
		}
		if value.Quorum == QuorumAll && !coversAllRoles(value.RequiredRoles, value.Decisions) {
			return errors.New("resolved approval не достиг quorum all")
		}
	default:
		return fmt.Errorf("неизвестный approval status %q", value.Status)
	}
	return nil
}

func sameRequest(left, right PendingApproval) bool {
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	return left.ID == right.ID && left.RunID == right.RunID && left.AttemptID == right.AttemptID &&
		left.FromStage == right.FromStage && left.ToStage == right.ToStage &&
		left.Trigger == right.Trigger && left.SubjectHash == right.SubjectHash &&
		left.CandidateSHA256 == right.CandidateSHA256 &&
		left.Quorum == right.Quorum && fmt.Sprint(left.RequiredRoles) == fmt.Sprint(right.RequiredRoles) &&
		fmt.Sprint(left.Actions) == fmt.Sprint(right.Actions) && fmt.Sprint(left.Targets) == fmt.Sprint(right.Targets) &&
		bytes.Equal(left.Payload, right.Payload)
}

func coversAllRoles(roles []string, decisions []Decision) bool {
	covered := make(map[string]bool, len(decisions))
	for _, decision := range decisions {
		covered[decision.ActorRole] = true
	}
	for _, role := range roles {
		if !covered[role] {
			return false
		}
	}
	return true
}

func uniqueNonEmpty(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func safeName(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value
}

func (s *Store) path(runID, approvalID string) (string, error) {
	if !safeName(runID) || !safeName(approvalID) {
		return "", errors.New("недопустимый approval path identity")
	}
	directory, err := safeio.EnsureDir(s.root, runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, approvalID+".json"), nil
}

func (s *Store) write(path string, value PendingApproval) error {
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".approval-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
