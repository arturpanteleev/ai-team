package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const maxDiagnostics = 64 << 10

type ProcessEngine struct {
	argv   []string
	target string
	dbPath string
}

type ProcessError struct {
	ExitCode    int
	Diagnostics string
	Result      *Result
	Err         error
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("disposable worker: %v: %s", e.Err, e.Diagnostics)
}

func (e *ProcessError) Unwrap() error { return e.Err }

func NewProcessEngine(argv []string, target, dbPath string) (*ProcessEngine, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("worker command обязателен")
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	absoluteTarget, err = filepath.EvalSymlinks(absoluteTarget)
	if err != nil {
		return nil, err
	}
	absoluteDB, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	return &ProcessEngine{
		argv: append([]string(nil), argv...), target: filepath.Clean(absoluteTarget), dbPath: filepath.Clean(absoluteDB),
	}, nil
}

func (e *ProcessEngine) Start(ctx context.Context, config pipeline.RunConfig) (pipeline.RunResult, error) {
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationStart,
		RunID: config.RunID, TargetDir: config.TargetDir, Feature: config.Feature, Task: config.TaskDesc,
		ApproveGates: config.ApproveGates, ApprovePlanHash: config.ApprovePlanHash,
	}
	return e.execute(ctx, job)
}

func (e *ProcessEngine) Resume(ctx context.Context, config pipeline.ResumeConfig) (pipeline.RunResult, error) {
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationResume,
		RunID: config.RunID, TargetDir: config.TargetDir,
		ApproveGates: config.ApproveGates, ApprovePlanHash: config.ApprovePlanHash,
	}
	return e.execute(ctx, job)
}

func (e *ProcessEngine) Cancel(config pipeline.CancelConfig) (pipeline.RunResult, error) {
	job := Job{
		SchemaVersion: SchemaVersion, Operation: OperationCancel,
		RunID: config.RunID, TargetDir: config.TargetDir,
	}
	return e.execute(context.Background(), job)
}

func (e *ProcessEngine) Execute(ctx context.Context, job Job) (pipeline.RunResult, error) {
	return e.execute(ctx, job)
}

func (e *ProcessEngine) execute(ctx context.Context, job Job) (pipeline.RunResult, error) {
	if err := job.Validate(e.target); err != nil {
		return pipeline.RunResult{}, err
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return pipeline.RunResult{}, err
	}
	args := append(append([]string(nil), e.argv[1:]...), "worker", "--target", e.target, "--db", e.dbPath)
	command := exec.CommandContext(ctx, e.argv[0], args...)
	command.Stdin = bytes.NewReader(payload)
	output := &limitedOutput{limit: maxDiagnostics}
	command.Stdout = output
	command.Stderr = output
	err = command.Run()
	result := pipeline.RunResult{RunID: job.RunID}
	if err == nil {
		// Успешный exit без строки результата — чужой binary: не считаем
		// это контролируемым завершением job.
		parsed, parseErr := ParseResult(output.String())
		if parseErr != nil {
			return result, &ProcessError{ExitCode: 0, Diagnostics: output.String(), Err: parseErr}
		}
		result.Outcome = workflow.RunOutcome(parsed.Outcome)
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	exitCode := -1
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
	}
	processErr := &ProcessError{ExitCode: exitCode, Diagnostics: output.String(), Err: err}
	if parsed, parseErr := ParseResult(output.String()); parseErr == nil {
		parsedResult := parsed
		processErr.Result = &parsedResult
		result.Outcome = workflow.RunOutcome(parsed.Outcome)
	}
	return result, processErr
}

type limitedOutput struct {
	mu        sync.Mutex
	value     []byte
	limit     int
	truncated bool
}

func (w *limitedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - len(w.value)
	if remaining > 0 {
		count := len(value)
		if count > remaining {
			count = remaining
		}
		w.value = append(w.value, value[:count]...)
	}
	if len(value) > remaining {
		w.truncated = true
	}
	return len(value), nil
}

func (w *limitedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	value := string(w.value)
	if w.truncated {
		value += "\n[diagnostics truncated]"
	}
	return value
}
