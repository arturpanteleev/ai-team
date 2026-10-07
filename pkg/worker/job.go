// Package worker определяет protocol и reference launcher disposable worker.
package worker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const SchemaVersion = 2
const LegacyQueueSchemaVersion = 1
const MaxJobBytes = 64 << 10
const ExecutionIDBytes = 32

type Operation string

const (
	OperationStart  Operation = "start"
	OperationResume Operation = "resume"
	// OperationRecover retries a durable start admission after a worker crash.
	// The worker inspects lifecycle state and chooses Start only if creation
	// never committed; otherwise it resumes the existing run.
	OperationRecover Operation = "recover"
	OperationCancel  Operation = "cancel"
)

type Job struct {
	SchemaVersion   int       `json:"schema_version"`
	Operation       Operation `json:"operation"`
	RunID           string    `json:"run_id"`
	TargetDir       string    `json:"target_dir"`
	Feature         string    `json:"feature,omitempty"`
	Task            string    `json:"task,omitempty"`
	ApproveGates    bool      `json:"approve_gates,omitempty"`
	ApprovePlanHash string    `json:"approve_plan_hash,omitempty"`
	ExecutionID     string    `json:"execution_id,omitempty"`
}

func DecodeJob(reader io.Reader, expectedTarget string) (Job, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxJobBytes+1))
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err := strictjson.Unmarshal(data, MaxJobBytes, &job); err != nil {
		return Job{}, fmt.Errorf("worker job: %w", err)
	}
	if err := job.Validate(expectedTarget); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (j Job) Validate(expectedTarget string) error {
	if j.SchemaVersion != SchemaVersion {
		return fmt.Errorf("worker job: неподдерживаемая schema_version %d", j.SchemaVersion)
	}
	if !validExecutionID(j.ExecutionID) {
		return errors.New("worker job: недопустимый execution_id")
	}
	return j.validateFields(expectedTarget)
}

// ValidateQueued accepts the current logical-job schema and the previous
// durable queue schema. Queued work has no process invocation identity until
// ProcessEngine spawns the child process.
func (j Job) ValidateQueued(expectedTarget string) error {
	if j.SchemaVersion != SchemaVersion && j.SchemaVersion != LegacyQueueSchemaVersion {
		return fmt.Errorf("worker queued job: неподдерживаемая schema_version %d", j.SchemaVersion)
	}
	if j.ExecutionID != "" {
		return errors.New("worker queued job: execution_id должен назначаться при запуске процесса")
	}
	return j.validateFields(expectedTarget)
}

func validExecutionID(value string) bool {
	if len(value) != ExecutionIDBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == ExecutionIDBytes
}

func (j Job) validateFields(expectedTarget string) error {
	if j.RunID == "" || filepath.Base(j.RunID) != j.RunID || strings.ContainsAny(j.RunID, `/\`) {
		return errors.New("worker job: недопустимый run_id")
	}
	target, err := filepath.Abs(j.TargetDir)
	if err != nil || !filepath.IsAbs(j.TargetDir) || filepath.Clean(j.TargetDir) != filepath.Clean(target) {
		return errors.New("worker job: target_dir должен быть absolute clean path")
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return errors.New("worker job: target_dir недоступен")
	}
	if expectedTarget != "" {
		expected, expectedErr := filepath.Abs(expectedTarget)
		if expectedErr == nil {
			expected, expectedErr = filepath.EvalSymlinks(expected)
		}
		if expectedErr != nil || filepath.Clean(canonicalTarget) != filepath.Clean(expected) {
			return errors.New("worker job: target_dir не совпадает с mounted target")
		}
	}
	switch j.Operation {
	case OperationStart:
		if !workflow.ValidFeature(j.Feature) || strings.TrimSpace(j.Task) == "" {
			return errors.New("worker start: feature и task обязательны")
		}
	case OperationResume:
		if j.Feature != "" || j.Task != "" {
			return errors.New("worker resume: feature/task загружаются из lifecycle")
		}
	case OperationRecover:
		if !workflow.ValidFeature(j.Feature) || strings.TrimSpace(j.Task) == "" {
			return errors.New("worker recover: исходные feature и task обязательны")
		}
	case OperationCancel:
		if j.Feature != "" || j.Task != "" || j.ApproveGates || j.ApprovePlanHash != "" {
			return errors.New("worker cancel: лишние execution параметры")
		}
	default:
		return fmt.Errorf("worker job: неизвестная operation %q", j.Operation)
	}
	return nil
}

func (j Job) RunConfig() pipeline.RunConfig {
	return pipeline.RunConfig{
		RunID: j.RunID, Feature: j.Feature, TaskDesc: j.Task, TargetDir: j.TargetDir,
		ApproveGates: j.ApproveGates, ApprovePlanHash: j.ApprovePlanHash,
	}
}
