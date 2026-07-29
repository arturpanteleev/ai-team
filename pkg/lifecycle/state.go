// Package lifecycle хранит изменяемый checkpoint исполнения отдельно от
// неизменяемого evidence конкретного run.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const SchemaVersion = 1

type Phase string

const (
	PhaseRunning   Phase = "running"
	PhaseWaiting   Phase = "waiting"
	PhaseResumable Phase = "resumable"
	PhaseTerminal  Phase = "terminal"
)

type State struct {
	SchemaVersion     int       `json:"schema_version"`
	RunID             string    `json:"run_id"`
	Feature           string    `json:"feature"`
	TargetDir         string    `json:"target_dir"`
	Task              string    `json:"task"`
	Phase             Phase     `json:"phase"`
	NextStage         string    `json:"next_stage,omitempty"`
	PendingApprovalID string    `json:"pending_approval_id,omitempty"`
	AttemptOrdinal    int       `json:"attempt_ordinal"`
	ConfigSHA256      string    `json:"config_sha256"`
	WorkflowSHA256    string    `json:"workflow_sha256"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Store struct {
	root string
}

func NewStore(target string) (*Store, error) {
	root, err := safeio.EnsureDir(target, ".ai-team", "state", "runs")
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Create(state State) error {
	path, err := s.path(state.RunID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("lifecycle state run %s уже существует", state.RunID)
	} else if !os.IsNotExist(err) {
		return err
	}
	state.SchemaVersion = SchemaVersion
	state.CreatedAt = state.CreatedAt.UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}
	state.UpdatedAt = state.CreatedAt
	return s.write(path, state)
}

func (s *Store) Load(runID string) (State, error) {
	path, err := s.path(runID)
	if err != nil {
		return State{}, err
	}
	data, err := safeio.ReadRegularFile(path, 1<<20)
	if err != nil {
		return State{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("lifecycle state %s: %w", runID, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return State{}, fmt.Errorf("lifecycle state %s содержит trailing JSON", runID)
	}
	if err := validate(state); err != nil {
		return State{}, err
	}
	if state.RunID != runID {
		return State{}, fmt.Errorf("lifecycle state identity mismatch: %s != %s", state.RunID, runID)
	}
	return state, nil
}

func (s *Store) Save(previous, next State) error {
	if previous.RunID != next.RunID || previous.Feature != next.Feature ||
		previous.TargetDir != next.TargetDir || previous.Task != next.Task ||
		previous.ConfigSHA256 != next.ConfigSHA256 || previous.WorkflowSHA256 != next.WorkflowSHA256 {
		return errors.New("immutable lifecycle identity изменена")
	}
	if previous.Phase == PhaseTerminal {
		return errors.New("terminal lifecycle state нельзя продолжить")
	}
	if !allowedTransition(previous.Phase, next.Phase) {
		return fmt.Errorf("недопустимый lifecycle transition %s → %s", previous.Phase, next.Phase)
	}
	next.SchemaVersion = SchemaVersion
	next.CreatedAt = previous.CreatedAt
	next.UpdatedAt = time.Now().UTC()
	if err := validate(next); err != nil {
		return err
	}
	path, err := s.path(next.RunID)
	if err != nil {
		return err
	}
	return s.write(path, next)
}

func allowedTransition(from, to Phase) bool {
	switch from {
	case PhaseRunning:
		return to == PhaseRunning || to == PhaseWaiting || to == PhaseResumable || to == PhaseTerminal
	case PhaseWaiting:
		return to == PhaseWaiting || to == PhaseRunning || to == PhaseTerminal
	case PhaseResumable:
		return to == PhaseRunning || to == PhaseTerminal
	default:
		return false
	}
}

func validate(state State) error {
	if state.SchemaVersion != SchemaVersion || state.RunID == "" || filepath.Base(state.RunID) != state.RunID ||
		state.Feature == "" || !filepath.IsAbs(state.TargetDir) || strings.TrimSpace(state.Task) == "" ||
		state.AttemptOrdinal < 0 || len(state.ConfigSHA256) != 64 || len(state.WorkflowSHA256) != 64 ||
		state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() {
		return errors.New("lifecycle state содержит недопустимые обязательные поля")
	}
	switch state.Phase {
	case PhaseRunning, PhaseResumable:
		if state.NextStage == "" {
			return errors.New("non-terminal lifecycle state требует next_stage")
		}
		if state.PendingApprovalID != "" {
			return errors.New("running/resumable lifecycle state не может иметь pending approval")
		}
	case PhaseWaiting:
		if state.NextStage == "" || state.PendingApprovalID == "" ||
			filepath.Base(state.PendingApprovalID) != state.PendingApprovalID {
			return errors.New("waiting lifecycle state требует next_stage и pending_approval_id")
		}
	case PhaseTerminal:
		if state.NextStage != "" || state.PendingApprovalID != "" {
			return errors.New("terminal lifecycle state не может иметь next_stage или pending approval")
		}
	default:
		return fmt.Errorf("неизвестная lifecycle phase %q", state.Phase)
	}
	return nil
}

func (s *Store) path(runID string) (string, error) {
	if runID == "" || filepath.Base(runID) != runID || runID == "." || runID == ".." {
		return "", fmt.Errorf("недопустимый run_id %q", runID)
	}
	return filepath.Join(s.root, runID+".json"), nil
}

func (s *Store) write(path string, state State) error {
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(s.root, ".state-*.tmp")
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
	directory, err := os.Open(s.root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
