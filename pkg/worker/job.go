// Package worker определяет protocol и reference launcher disposable worker.
package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const SchemaVersion = 1
const MaxJobBytes = 64 << 10

type Operation string

const (
	OperationStart  Operation = "start"
	OperationResume Operation = "resume"
	OperationCancel Operation = "cancel"
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
}

func DecodeJob(reader io.Reader, expectedTarget string) (Job, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxJobBytes+1))
	if err != nil {
		return Job{}, err
	}
	if len(data) == 0 || len(data) > MaxJobBytes {
		return Job{}, errors.New("worker job пуст или превышает limit")
	}
	var job Job
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&job); err != nil {
		return Job{}, fmt.Errorf("worker job JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Job{}, errors.New("worker job должен содержать один JSON document")
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
