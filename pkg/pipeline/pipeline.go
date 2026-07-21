package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/process"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/scope"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

const (
	maxArtifactFileBytes   = 8 << 20
	maxArtifactTreeBytes   = 32 << 20
	maxArtifactTreeFiles   = 1000
	maxArtifactTreeDepth   = 20
	maxCandidatePatchBytes = 2 << 20
	maxCandidateGitStderr  = 64 << 10
)

// Recorder получает жизненный цикл запуска (запись в SQLite для web-UI).
// Реализации не должны ронять пайплайн: ошибки обрабатываются внутри.
type Recorder interface {
	ReconcileInterrupted(at time.Time)
	RunStarted(runID, feature, configSnapshot string, startedAt time.Time)
	StageStarted(runID, attemptID, agentName string, index int, startedAt time.Time)
	StageFinished(stage notifier.StageResult)
	AttemptsInvalidated(runID string, attemptIDs []string, at time.Time)
	RunFinished(runID, status string, completedAt time.Time)
}

type Pipeline struct {
	cfg        *config.Config
	reg        *agent.Registry
	notifier   notifier.Notifier
	prompter   Prompter
	newRuntime runtime.Factory
	recorder   Recorder
	delivery   delivery.Service
	reportsDir string
}

type Option func(*Pipeline)

func WithNotifier(n notifier.Notifier) Option {
	return func(p *Pipeline) { p.notifier = n }
}

func WithReportsDir(dir string) Option {
	return func(p *Pipeline) { p.reportsDir = dir }
}

// WithPrompter подменяет интерактив (тесты, будущий web-режим).
func WithPrompter(pr Prompter) Option {
	return func(p *Pipeline) { p.prompter = pr }
}

// WithRuntimeFactory подменяет создание runtime (тесты).
func WithRuntimeFactory(f runtime.Factory) Option {
	return func(p *Pipeline) { p.newRuntime = f }
}

func WithRecorder(r Recorder) Option {
	return func(p *Pipeline) { p.recorder = r }
}

func WithDeliveryService(service delivery.Service) Option {
	return func(p *Pipeline) { p.delivery = service }
}

func New(cfg *config.Config, reg *agent.Registry, opts ...Option) *Pipeline {
	if cfg == nil {
		cfg = config.Default()
	}
	p := &Pipeline{cfg: cfg, reg: reg}
	for _, opt := range opts {
		opt(p)
	}
	if p.notifier == nil {
		p.notifier = notifier.NewConsoleNotifier()
	}
	if p.prompter == nil {
		p.prompter = NewConsolePrompter()
	}
	if p.newRuntime == nil {
		p.newRuntime = runtime.NewRuntime
	}
	if p.delivery == nil {
		p.delivery = delivery.NewController()
	}
	return p
}

type RunConfig struct {
	RunID           string
	Feature         string
	TaskDesc        string
	TargetDir       string
	RetryFrom       string
	ApproveGates    bool
	ApprovePlanHash string
}

type RunResult struct {
	RunID   string
	Outcome workflow.RunOutcome
}

func (p *Pipeline) Agents() []string {
	return p.cfg.AgentNames()
}

// runState — состояние одного запуска (Pipeline не мутируется, можно
// переиспользовать для нескольких запусков).
type runState struct {
	p                *Pipeline
	runCfg           RunConfig
	task             *runtime.Task
	reportsDir       string
	names            []string
	results          []notifier.StageResult
	extraInputs      map[string][]runtime.Artifact // loopback: выходы вердикт-агента → входы цели
	retryCounts      map[string]int
	ps               *ui.PipelineStatus
	startTime        time.Time
	approvedPlanHash string
	runID            string
	evidence         *evidence.Store
	attemptOrdinal   int
	userOwnedPaths   map[string]bool
}

func (p *Pipeline) Run(ctx context.Context, runCfg RunConfig) error {
	_, err := p.RunWithResult(ctx, runCfg)
	return err
}

func (p *Pipeline) RunWithResult(ctx context.Context, runCfg RunConfig) (RunResult, error) {
	if err := p.cfg.Validate(p.reg); err != nil {
		return RunResult{}, err
	}
	if !workflow.ValidFeature(runCfg.Feature) {
		return RunResult{}, fmt.Errorf("недопустимое имя feature %q", runCfg.Feature)
	}
	targetDir, err := filepath.Abs(runCfg.TargetDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("нормализация target: %w", err)
	}
	targetDir, err = filepath.EvalSymlinks(filepath.Clean(targetDir))
	if err != nil {
		return RunResult{}, fmt.Errorf("нормализация target symlinks: %w", err)
	}
	targetInfo, err := os.Stat(targetDir)
	if err != nil || !targetInfo.IsDir() {
		return RunResult{}, fmt.Errorf("target %s не является доступным каталогом", targetDir)
	}
	runCfg.TargetDir = targetDir
	if _, err := safeio.EnsureDir(runCfg.TargetDir, ".ai-team"); err != nil {
		return RunResult{}, fmt.Errorf("controller root: %w", err)
	}
	workspaceLock, err := evidence.AcquireWorkspaceLock(runCfg.TargetDir)
	if err != nil {
		return RunResult{}, err
	}
	defer func() {
		if closeErr := workspaceLock.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "  %s workspace unlock error: %v\n", ui.Colorize("⚠", ui.ColorYellow), closeErr)
		}
	}()

	// task.md is a workflow input and therefore must be created/read while the
	// workspace lock is held. Otherwise a rejected concurrent run could overwrite
	// the task consumed by the active run before failing to acquire the lock.
	runCfg.TaskDesc, err = prepareTaskArtifact(runCfg)
	if err != nil {
		return RunResult{}, err
	}

	runStartedAt := time.Now().UTC()
	runID := runCfg.RunID
	if runID == "" {
		runID, err = evidence.NewRunID(runStartedAt)
		if err != nil {
			return RunResult{}, err
		}
	}
	configSnapshot, workflowSnapshot, err := p.resolvedEvidenceSnapshots()
	if err != nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resolved workflow evidence: %w", err)
	}
	evidenceStore, err := evidence.Start(filepath.Join(runCfg.TargetDir, ".ai-team", "runs"), evidence.RunManifest{
		RunID: runID, Feature: runCfg.Feature, TargetDir: runCfg.TargetDir, StartedAt: runStartedAt,
		ConfigSnapshot: configSnapshot, WorkflowSnapshot: workflowSnapshot,
	})
	if err != nil {
		return RunResult{}, fmt.Errorf("создание evidence run: %w", err)
	}
	if err := evidenceStore.Append(evidence.Event{Type: "run_started", Timestamp: runStartedAt}); err != nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("запись run_started: %w", err)
	}

	reportsDir := p.reportsDir
	if reportsDir == "" {
		reportsDir = filepath.Join(runCfg.TargetDir, ".ai-team", "reports")
		if _, err := safeio.EnsureDir(runCfg.TargetDir, ".ai-team", "reports"); err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("reports root: %w", err)
		}
		featureReports := filepath.Join(reportsDir, runCfg.Feature)
		if info, statErr := os.Lstat(featureReports); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("report path %s должен быть каталогом без symlink", featureReports)
			}
			// Live reports are a replaceable projection; previous versions remain
			// available in their immutable run directories.
			if err := os.RemoveAll(featureReports); err != nil {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("clear live report projection: %w", err)
			}
		} else if !os.IsNotExist(statErr) {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, statErr
		}
	}

	rs := &runState{
		p:      p,
		runCfg: runCfg,
		task: &runtime.Task{
			Feature:      runCfg.Feature,
			TaskDesc:     runCfg.TaskDesc,
			TargetDir:    runCfg.TargetDir,
			ArtifactRoot: filepath.Join(runCfg.TargetDir, ".ai-team", "artifacts"),
			LogDir:       evidenceStore.LogDir(),
		},
		reportsDir:       reportsDir,
		names:            p.cfg.AgentNames(),
		extraInputs:      make(map[string][]runtime.Artifact),
		retryCounts:      make(map[string]int),
		startTime:        runStartedAt,
		approvedPlanHash: runCfg.ApprovePlanHash,
		runID:            runID,
		evidence:         evidenceStore,
	}
	rs.ps = ui.NewPipelineStatus(filepath.Base(runCfg.TargetDir), runCfg.Feature, len(rs.names))
	rs.task.ConsoleOut = rs.ps.StatusWriter()
	if p.recorder != nil {
		snapshot, _ := yaml.Marshal(p.cfg)
		p.recorder.ReconcileInterrupted(rs.startTime)
		p.recorder.RunStarted(runID, runCfg.Feature, string(snapshot), rs.startTime)
	}
	if err := rs.initializeWorkspaceOwnership(); err != nil {
		outcome, finalErr := rs.finalize(err)
		return RunResult{RunID: runID, Outcome: outcome}, finalErr
	}

	runErr := rs.execute(ctx)
	outcome, finalErr := rs.finalize(runErr)
	return RunResult{RunID: runID, Outcome: outcome}, finalErr
}

func (rs *runState) initializeWorkspaceOwnership() error {
	rs.userOwnedPaths = make(map[string]bool)
	workspace, err := captureWorkspaceSnapshot(rs.runCfg.TargetDir)
	if err != nil {
		return fmt.Errorf("workspace ownership baseline: %w", err)
	}
	gitState, available, err := captureGitMetadataSnapshot(rs.runCfg.TargetDir)
	if err != nil {
		return fmt.Errorf("git ownership baseline: %w", err)
	}
	if !available {
		return nil
	}
	if rs.runCfg.RetryFrom == "" && len(gitState.Dirty) > 0 {
		paths := make([]string, 0, len(gitState.Dirty))
		for path := range gitState.Dirty {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		return fmt.Errorf("новый run требует clean git workspace; сохраните или удалите пользовательские изменения: %s", strings.Join(paths, ", "))
	}
	for path := range gitState.Dirty {
		rs.userOwnedPaths[path] = true
	}
	// Pre-existing ignored/untracked files are user-owned even though porcelain
	// status may hide them. Agents may create new paths, but never overwrite
	// such ambient data or caches in the canonical workspace.
	for path := range workspace.Files {
		if !gitState.Tracked[path] {
			rs.userOwnedPaths[path] = true
		}
	}
	return nil
}

func (p *Pipeline) resolvedEvidenceSnapshots() (json.RawMessage, json.RawMessage, error) {
	configSnapshot, err := json.MarshalIndent(p.cfg, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	type resolvedStage struct {
		Index      int                 `json:"index"`
		Name       string              `json:"name"`
		Definition *agent.Agent        `json:"definition"`
		Effective  *config.AgentConfig `json:"effective_config"`
	}
	type resolvedWorkflow struct {
		SchemaVersion int             `json:"schema_version"`
		Stages        []resolvedStage `json:"stages"`
	}
	resolved := resolvedWorkflow{SchemaVersion: 1}
	for index, name := range p.cfg.AgentNames() {
		definition, loadErr := p.reg.Load(name)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		resolved.Stages = append(resolved.Stages, resolvedStage{
			Index: index + 1, Name: name, Definition: definition, Effective: p.cfg.AgentConfig(name),
		})
	}
	workflowSnapshot, err := json.MarshalIndent(resolved, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return configSnapshot, workflowSnapshot, nil
}

func prepareTaskArtifact(runCfg RunConfig) (string, error) {
	taskPath := filepath.Join(runCfg.TargetDir, ".ai-team", "artifacts", "tasks", runCfg.Feature, "task.md")
	if _, err := safeio.EnsureDir(runCfg.TargetDir, ".ai-team", "artifacts", "tasks", runCfg.Feature); err != nil {
		return "", fmt.Errorf("безопасный каталог task.md: %w", err)
	}
	if runCfg.RetryFrom != "" {
		data, err := safeio.ReadRegularFile(taskPath, maxArtifactFileBytes)
		if err != nil {
			return "", fmt.Errorf("retry-from: task.md не найден (%s): %w", taskPath, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("retry-from: task.md пуст (%s)", taskPath)
		}
		return string(data), nil
	}
	if strings.TrimSpace(runCfg.TaskDesc) == "" {
		return "", fmt.Errorf("описание задачи обязательно для нового run")
	}
	temporary, err := os.CreateTemp(filepath.Dir(taskPath), ".task-*.tmp")
	if err != nil {
		return "", fmt.Errorf("временный task.md: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		cleanup()
		return "", fmt.Errorf("права task.md: %w", err)
	}
	if _, err := temporary.WriteString(runCfg.TaskDesc); err != nil {
		_ = temporary.Close()
		cleanup()
		return "", fmt.Errorf("запись task.md: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return "", fmt.Errorf("sync task.md: %w", err)
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("закрытие task.md: %w", err)
	}
	if err := os.Rename(temporaryPath, taskPath); err != nil {
		cleanup()
		return "", fmt.Errorf("публикация task.md: %w", err)
	}
	return runCfg.TaskDesc, nil
}

func (rs *runState) execute(ctx context.Context) error {
	startIdx := 0
	if rs.runCfg.RetryFrom != "" {
		idx, err := rs.prepareRetryFrom()
		if err != nil {
			return err
		}
		startIdx = idx
		if err := rs.invalidateRetryOutputs(startIdx); err != nil {
			return err
		}
	}

	for i := startIdx; i < len(rs.names); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		name := rs.names[i]
		if err := rs.prepareControllerStageEvidence(ctx, name); err != nil {
			return fmt.Errorf("controller evidence for %s: %w", name, err)
		}
		if err := rs.authorizeStage(name); err != nil {
			return err
		}
		r := rs.runStage(ctx, i, name)
		rs.results = append(rs.results, r)

		if err := rs.p.notifier.Notify(ctx, r); err != nil {
			fmt.Fprintf(os.Stderr, "  %s notifier error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
		}
		if err := report.GenerateStageReport(rs.reportsDir, rs.runCfg.Feature, r.AttemptID, r, rs.task.ArtifactRoot); err != nil {
			fmt.Fprintf(os.Stderr, "  %s report error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
		}
		if rs.p.recorder != nil {
			rs.p.recorder.StageFinished(r)
		}

		if r.Status == notifier.StatusBlocked {
			fmt.Printf("\n%s %s\n", ui.Colorize("⊘ Блокер:", ui.ColorBold+ui.ColorYellow), r.Blocker)
			fmt.Printf("  Для исправления уточните задачу и запустите: ai-team run --feature %s --retry-from %s\n",
				rs.runCfg.Feature, name)
			return &BlockedError{Agent: name, Reason: r.Blocker}
		}
		if r.Err != nil {
			return r.Err
		}

		if r.Verdict.IsNegative() {
			loopbackIdx, err := rs.enforce(i, name, r)
			if err != nil {
				return err
			}
			if loopbackIdx >= 0 {
				if err := rs.invalidateAttempts(loopbackIdx + 1); err != nil {
					return err
				}
				// История результатов сохраняется (повторные этапы видны в отчёте).
				i = loopbackIdx - 1
				continue
			}
		}

		if err := rs.checkpoints(i, name, r); err != nil {
			return err
		}

		rs.ps.DoneAgent(name)
	}

	return nil
}

type candidateEvidence struct {
	SchemaVersion        int                `json:"schema_version"`
	RunID                string             `json:"run_id"`
	Purpose              string             `json:"purpose"`
	BaselineHead         string             `json:"baseline_head,omitempty"`
	WorkspaceSHA256      string             `json:"workspace_sha256"`
	ChangedFiles         []candidateFile    `json:"changed_files"`
	TrackedPatchSHA256   string             `json:"tracked_patch_sha256,omitempty"`
	TrackedPatchBytes    int64              `json:"tracked_patch_bytes,omitempty"`
	TrackedPatchIncluded bool               `json:"tracked_patch_included"`
	TrackedPatch         string             `json:"tracked_patch,omitempty"`
	Checks               []candidateCheck   `json:"checks"`
	Attempts             []candidateAttempt `json:"attempts"`
}

type candidateFile struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Mode        string `json:"mode"`
}

type candidateCheck struct {
	AttemptID             string   `json:"attempt_id"`
	Name                  string   `json:"name"`
	Class                 string   `json:"class"`
	Adapter               string   `json:"adapter"`
	Command               []string `json:"command"`
	ToolPath              string   `json:"tool_path,omitempty"`
	ToolVersion           string   `json:"tool_version,omitempty"`
	Status                string   `json:"status"`
	ExitCode              int      `json:"exit_code"`
	DiscoveredTests       int      `json:"discovered_tests,omitempty"`
	PassedTests           int      `json:"passed_tests,omitempty"`
	EvidenceDigest        string   `json:"evidence_digest"`
	WorkspaceDigestBefore string   `json:"workspace_digest_before"`
	WorkspaceDigestAfter  string   `json:"workspace_digest_after"`
	StructuredSHA256      string   `json:"structured_output_sha256,omitempty"`
}

type candidateAttempt struct {
	AttemptID string `json:"attempt_id"`
	Stage     string `json:"stage"`
	Outcome   string `json:"outcome"`
	Verdict   string `json:"verdict,omitempty"`
}

func (rs *runState) prepareControllerStageEvidence(ctx context.Context, stage string) error {
	definition, err := rs.p.reg.Load(stage)
	if err != nil {
		return err
	}
	for inputName, declaredPath := range definition.Inputs {
		resolved := filepath.ToSlash(runtime.ReplaceVars(declaredPath, rs.runCfg.Feature))
		switch {
		case inputName == "candidate" && strings.HasSuffix(resolved, "/.control/review-candidate.json"):
			return rs.writeCandidateEvidence(ctx, "review-candidate.json", "semantic_code_review")
		case inputName == "reviewed-candidate" && strings.HasSuffix(resolved, "/.control/review-candidate.json"):
			return rs.verifyCandidateEvidence("review-candidate.json", "semantic_code_review")
		case inputName == "candidate" && strings.HasSuffix(resolved, "/.control/verification-candidate.json"):
			return rs.writeCandidateEvidence(ctx, "verification-candidate.json", "final_verification")
		}
	}
	return nil
}

func (rs *runState) writeCandidateEvidence(ctx context.Context, name, purpose string) error {
	workspaceDigest, err := checks.WorkspaceDigest(rs.runCfg.TargetDir)
	if err != nil {
		return err
	}
	snapshot, err := captureWorkspaceSnapshot(rs.runCfg.TargetDir)
	if err != nil {
		return err
	}
	gitState, gitAvailable, err := captureGitMetadataSnapshot(rs.runCfg.TargetDir)
	if err != nil {
		return err
	}
	changedSet := make(map[string]bool)
	if gitAvailable {
		for changed := range gitState.Dirty {
			changedSet[changed] = true
		}
	} else {
		for _, changed := range rs.attributedDeliveryFiles() {
			changedSet[changed] = true
		}
	}
	changed := make([]string, 0, len(changedSet))
	for path := range changedSet {
		changed = append(changed, path)
	}
	sort.Strings(changed)
	evidenceDocument := candidateEvidence{
		SchemaVersion: 1, RunID: rs.runID, Purpose: purpose,
		WorkspaceSHA256: workspaceDigest, ChangedFiles: make([]candidateFile, 0, len(changed)),
		Checks: make([]candidateCheck, 0), Attempts: make([]candidateAttempt, 0),
	}
	if gitAvailable {
		evidenceDocument.BaselineHead = gitState.Head
		patch, patchErr := collectTrackedPatch(ctx, rs.runCfg.TargetDir)
		if patchErr != nil {
			return fmt.Errorf("tracked patch: %w", patchErr)
		}
		evidenceDocument.TrackedPatchSHA256 = patch.Digest()
		evidenceDocument.TrackedPatchBytes = patch.Total()
		if !patch.Truncated() {
			evidenceDocument.TrackedPatchIncluded = true
			evidenceDocument.TrackedPatch = patch.String()
		}
	}
	for _, changedPath := range changed {
		fingerprint := snapshot.Files[changedPath]
		mode := "deleted"
		if info, statErr := os.Lstat(filepath.Join(rs.runCfg.TargetDir, filepath.FromSlash(changedPath))); statErr == nil {
			mode = info.Mode().String()
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		if fingerprint == "" {
			fingerprint = "deleted"
		}
		evidenceDocument.ChangedFiles = append(evidenceDocument.ChangedFiles, candidateFile{
			Path: changedPath, Fingerprint: fingerprint, Mode: mode,
		})
	}
	for _, result := range rs.results {
		if result.Superseded {
			continue
		}
		evidenceDocument.Attempts = append(evidenceDocument.Attempts, candidateAttempt{
			AttemptID: result.AttemptID, Stage: result.Name, Outcome: string(result.State.Outcome), Verdict: string(result.Verdict),
		})
		for _, check := range result.Checks {
			evidenceDocument.Checks = append(evidenceDocument.Checks, candidateCheck{
				AttemptID: result.AttemptID, Name: check.Name, Class: check.Class, Adapter: check.Adapter,
				Command: append([]string(nil), check.Command...), ToolPath: check.ToolPath, ToolVersion: check.ToolVersion,
				Status: check.Status, ExitCode: check.ExitCode, DiscoveredTests: check.DiscoveredTests, PassedTests: check.PassedTests,
				EvidenceDigest: check.EvidenceDigest, WorkspaceDigestBefore: check.WorkspaceDigestBefore,
				WorkspaceDigestAfter: check.WorkspaceDigestAfter, StructuredSHA256: check.StructuredOutputSHA256,
			})
		}
	}
	directory, err := safeio.EnsureDir(rs.task.ArtifactRoot, rs.runCfg.Feature, ".control")
	if err != nil {
		return err
	}
	return writeControllerJSON(filepath.Join(directory, name), evidenceDocument)
}

func (rs *runState) verifyCandidateEvidence(name, purpose string) error {
	path := filepath.Join(rs.task.ArtifactRoot, rs.runCfg.Feature, ".control", name)
	data, err := safeio.ReadRegularFile(path, maxArtifactFileBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var candidate candidateEvidence
	if err := decoder.Decode(&candidate); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("candidate evidence has trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	current, err := checks.WorkspaceDigest(rs.runCfg.TargetDir)
	if err != nil {
		return err
	}
	if candidate.SchemaVersion != 1 || candidate.RunID != rs.runID || candidate.Purpose != purpose || candidate.WorkspaceSHA256 != current {
		return fmt.Errorf("reviewed candidate identity changed before test authoring")
	}
	return nil
}

// digestCapture hashes the complete stream while retaining only a bounded
// prefix. This keeps candidate identity exact without allowing a large diff to
// consume unbounded controller memory.
type digestCapture struct {
	buffer    bytes.Buffer
	hash      hash.Hash
	total     int64
	limit     int
	truncated bool
}

func newDigestCapture(limit int) *digestCapture {
	return &digestCapture{hash: sha256.New(), limit: limit}
}

func (capture *digestCapture) Write(data []byte) (int, error) {
	original := len(data)
	capture.total += int64(original)
	_, _ = capture.hash.Write(data)
	remaining := capture.limit - capture.buffer.Len()
	if remaining <= 0 {
		capture.truncated = capture.truncated || original > 0
		return original, nil
	}
	if original > remaining {
		capture.truncated = true
		data = data[:remaining]
	}
	_, _ = capture.buffer.Write(data)
	return original, nil
}

func (capture *digestCapture) Digest() string  { return fmt.Sprintf("%x", capture.hash.Sum(nil)) }
func (capture *digestCapture) String() string  { return capture.buffer.String() }
func (capture *digestCapture) Total() int64    { return capture.total }
func (capture *digestCapture) Truncated() bool { return capture.truncated }

func collectTrackedPatch(ctx context.Context, dir string) (*digestCapture, error) {
	stdout := newDigestCapture(maxCandidatePatchBytes)
	stderr := newDigestCapture(maxCandidateGitStderr)
	command := exec.Command("git", "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	command.Dir = dir
	command.Stdout = stdout
	command.Stderr = stderr
	if err := process.Run(ctx, command); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%w: %s", err, message)
		}
		return nil, err
	}
	return stdout, nil
}

func writeControllerJSON(path string, value any) error {
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxArtifactFileBytes {
		return fmt.Errorf("controller evidence %s exceeds %d bytes", path, maxArtifactFileBytes)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".control-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (rs *runState) invalidateRetryOutputs(startIdx int) error {
	var invalidatedStages []string
	for i := startIdx; i < len(rs.names); i++ {
		name := rs.names[i]
		a, err := rs.p.reg.Load(name)
		if err != nil {
			return fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
		}
		if err := rs.clearStageEphemeral(name, a); err != nil {
			return fmt.Errorf("invalidate retry outputs %s: %w", name, err)
		}
		invalidatedStages = append(invalidatedStages, name)
	}
	return rs.evidence.Append(evidence.Event{
		Type: "retry_outputs_invalidated", Timestamp: time.Now().UTC(),
		Data: map[string]any{"retry_from": rs.names[startIdx], "stages": invalidatedStages},
	})
}

func (rs *runState) invalidateAttempts(fromStageIndex int) error {
	var invalidated []string
	for i := range rs.results {
		result := &rs.results[i]
		if result.StageIndex < fromStageIndex || result.Superseded {
			continue
		}
		result.Superseded = true
		result.State = workflow.Invalidate(result.State)
		result.Status = result.State.LegacyStatus()
		invalidated = append(invalidated, result.AttemptID)
	}
	if len(invalidated) == 0 {
		return nil
	}
	at := time.Now().UTC()
	if rs.p.recorder != nil {
		rs.p.recorder.AttemptsInvalidated(rs.runID, invalidated, at)
	}
	return rs.evidence.Append(evidence.Event{
		Type: "attempts_invalidated", Timestamp: at,
		Data: map[string]any{"from_stage_index": fromStageIndex, "attempt_ids": invalidated, "reason": "loopback"},
	})
}

// runStage выполняет один этап: загрузка агента, входы, execute (с таймаутом),
// blocked-check, проверка выходов, git guard, парсинг вердикта.
func (rs *runState) runStage(ctx context.Context, i int, name string) (r notifier.StageResult) {
	stageStart := time.Now()
	rs.attemptOrdinal++
	attemptID := rs.evidence.NewAttemptID(name, rs.attemptOrdinal)
	r = notifier.StageResult{
		RunID:       rs.runID,
		AttemptID:   attemptID,
		Name:        name,
		StageIndex:  i + 1,
		TotalStages: len(rs.names),
		StartedAt:   stageStart.UTC(),
	}
	var evidenceInputs []evidence.Artifact
	cleanupEvidenceInputs := func() {}
	fail := func(err error) notifier.StageResult {
		r.Err = err
		r.Status = notifier.StatusFailed
		r.Duration = time.Since(stageStart)
		return r
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "attempt_started", Stage: name, AttemptID: attemptID, Timestamp: stageStart.UTC(),
		Data: map[string]any{"stage_index": i + 1},
	}); err != nil {
		return fail(fmt.Errorf("агент %s: запись attempt_started: %w", name, err))
	}
	defer func() {
		r.FinishedAt = time.Now().UTC()
		r.Duration = r.FinishedAt.Sub(r.StartedAt)
		r.Summary = report.ReadStageSummary(rs.task.ArtifactRoot, rs.runCfg.Feature, name)
		rs.deriveStageState(&r)
		manifest := evidence.AttemptManifest{
			AttemptID: attemptID, Stage: name, StageIndex: i + 1,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			Status: r.Status, Verdict: string(r.Verdict), Blocker: r.Blocker,
			Execution: string(r.State.Execution), Decision: string(r.State.Decision), Outcome: string(r.State.Outcome),
			Checks:    append([]checks.Result(nil), r.Checks...),
			Mutations: append([]string(nil), r.Mutations...), Delivery: r.Delivery,
		}
		if r.Err != nil {
			manifest.Error = r.Err.Error()
		}
		manifestPublished := false
		if err := rs.evidence.PublishAttempt(manifest, rs.task.ArtifactRoot, evidenceInputs, toEvidenceArtifacts(r.Outputs)); err != nil {
			r.Err = errors.Join(r.Err, fmt.Errorf("публикация evidence attempt %s: %w", attemptID, err))
			rs.deriveStageState(&r)
		} else {
			manifestPublished = true
		}
		cleanupEvidenceInputs()
		data := map[string]any{
			"status": r.Status, "execution": r.State.Execution, "decision": r.State.Decision,
			"outcome": r.State.Outcome, "verdict": r.Verdict,
		}
		if manifestPublished {
			manifestPath := filepath.Join(rs.evidence.RunDir(), "attempts", attemptID, "manifest.json")
			_, _, digest, digestErr := evidence.ArtifactDigest(manifestPath)
			if digestErr != nil {
				r.Err = errors.Join(r.Err, fmt.Errorf("attempt manifest digest %s: %w", attemptID, digestErr))
				rs.deriveStageState(&r)
				data["status"], data["execution"], data["decision"], data["outcome"] = r.Status, r.State.Execution, r.State.Decision, r.State.Outcome
			} else {
				data["manifest_sha256"] = digest
			}
		}
		if r.Blocker != "" {
			data["blocker"] = r.Blocker
		}
		if r.Err != nil {
			data["error"] = r.Err.Error()
		}
		if err := rs.evidence.Append(evidence.Event{
			Type: "attempt_finished", Stage: name, AttemptID: attemptID, Timestamp: r.FinishedAt, Data: data,
		}); err != nil {
			r.Err = errors.Join(r.Err, fmt.Errorf("запись attempt_finished %s: %w", attemptID, err))
			rs.deriveStageState(&r)
		}
	}()

	rs.ps.StartAgent(i+1, name)
	if rs.p.recorder != nil {
		rs.p.recorder.StageStarted(rs.runID, attemptID, name, i+1, stageStart.UTC())
	}
	fmt.Printf("\n%s %s\n",
		ui.Colorize("▶", ui.ColorCyan),
		ui.Colorize(name, ui.ColorBold+ui.ColorYellow))

	a, err := rs.p.reg.Load(name)
	if err != nil {
		return fail(fmt.Errorf("ошибка загрузки агента %s: %w", name, err))
	}
	agentCfg := rs.p.cfg.AgentConfig(name)
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Name: name}
	}

	inputs, inputArtifacts, err := rs.collectInputs(a, name)
	r.Inputs = inputArtifacts
	if err != nil {
		return fail(err)
	}
	evidenceInputs, cleanupEvidenceInputs, err = rs.evidence.SnapshotInputs(attemptID, toEvidenceArtifacts(inputArtifacts))
	if err != nil {
		return fail(fmt.Errorf("агент %s: immutable input snapshot: %w", name, err))
	}
	inputs = toRuntimeArtifacts(evidenceInputs)
	preconditions, err := validateSnapshotPreconditions(name, a, inputs)
	if err != nil {
		return fail(err)
	}
	if a.Kind == "delivery" {
		if err := rs.validateDeliveryChecks(); err != nil {
			return fail(fmt.Errorf("агент %s: %w", name, err))
		}
	}

	var stageRuntime runtime.Runtime
	var runtimeAgent *runtime.Agent
	if a.Kind != "delivery" {
		stageRuntime, err = rs.p.newRuntime(a.RuntimeType)
		if err != nil {
			return fail(fmt.Errorf("ошибка создания runtime для %s: %w", name, err))
		}

		runtimeAgent = &runtime.Agent{
			Name:         a.Name,
			AttemptID:    attemptID,
			RuntimeType:  a.RuntimeType,
			CLI:          a.CLI,
			Prompt:       a.Prompt,
			Inputs:       a.Inputs,
			Outputs:      a.Outputs,
			Verdict:      a.Verdict,
			Kind:         a.Kind,
			Mutation:     a.Mutation,
			AllowedPaths: append([]string(nil), a.AllowedPaths...),
			RequireDiff:  a.RequireDiff,
		}
		if agentCfg.CLI != "" {
			runtimeAgent.CLI = agentCfg.CLI
		}
		runtimeAgent.Model = agentCfg.Model
		runtimeAgent.Effort = agentCfg.Effort
	}

	stageCtx := ctx
	timeout, terr := agentCfg.StageTimeoutFor()
	if terr != nil {
		return fail(fmt.Errorf("невалидный timeout агента %s: %w", name, terr))
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		stageCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if err := rs.clearStageEphemeral(name, a); err != nil {
		return fail(fmt.Errorf("агент %s: очистка stale control artifacts: %w", name, err))
	}

	var workspaceBefore filesystemSnapshot
	var gitBefore gitMetadataSnapshot
	var gitAvailable bool
	guardWorkspace := a.Kind != "delivery"
	if guardWorkspace {
		workspaceBefore, err = captureWorkspaceSnapshot(rs.runCfg.TargetDir)
		if err != nil {
			return fail(fmt.Errorf("агент %s: не удалось снять workspace baseline: %w", name, err))
		}
		gitBefore, gitAvailable, err = captureGitMetadataSnapshot(rs.runCfg.TargetDir)
		if err != nil {
			return fail(fmt.Errorf("агент %s: не удалось снять git metadata baseline: %w", name, err))
		}
	}
	artifactBefore, err := captureArtifactSnapshot(rs.task.ArtifactRoot)
	if err != nil {
		return fail(fmt.Errorf("агент %s: не удалось снять artifact baseline: %w", name, err))
	}
	defer func() {
		if guardErr := rs.enforceMutationGuard(a, name, workspaceBefore, gitBefore, gitAvailable, guardWorkspace, artifactBefore, &r); guardErr != nil {
			r.ValidationFailed = true
			r.Err = errors.Join(r.Err, guardErr)
			r.Status = notifier.StatusFailed
		}
	}()

	var execErr error
	if a.Kind == "delivery" {
		execErr = rs.writeDeliveryPlan(stageCtx, a, preconditions)
	} else {
		execErr = stageRuntime.Execute(stageCtx, runtimeAgent, rs.task, inputs)
	}
	// BLOCKED имеет приоритет над ошибкой выполнения и проверкой выходов:
	// заблокированный агент по контракту не создаёт обычных артефактов.
	if blocked, reason := verdict.ReadBlocked(rs.task.ArtifactRoot, rs.runCfg.Feature, name); blocked {
		for outputName, outputPath := range a.Outputs {
			fullPath := filepath.Join(rs.task.ArtifactRoot, runtime.ReplaceVars(outputPath, rs.runCfg.Feature))
			if _, statErr := os.Lstat(fullPath); statErr == nil {
				r.ValidationFailed = true
				return fail(fmt.Errorf("агент %s создал normal output %s одновременно с BLOCKED signal", name, outputName))
			} else if !os.IsNotExist(statErr) {
				return fail(fmt.Errorf("агент %s: проверка output при BLOCKED: %w", name, statErr))
			}
		}
		statusPath := verdict.StatusFilePath(rs.task.ArtifactRoot, rs.runCfg.Feature, name)
		if statusInfo, statErr := os.Stat(statusPath); statErr == nil {
			r.Outputs = []runtime.Artifact{{Name: "blocked-status", Path: statusPath, Size: statusInfo.Size(), ModTime: statusInfo.ModTime()}}
		}
		r.Status = notifier.StatusBlocked
		r.Blocker = reason
		r.Duration = time.Since(stageStart)
		return r
	}

	if execErr != nil {
		if stageCtx.Err() == context.DeadlineExceeded {
			return fail(fmt.Errorf("этап %s превысил таймаут %s: %w", name, timeout, context.DeadlineExceeded))
		}
		return fail(execErr)
	}

	outputs, err := rs.collectOutputs(a, name)
	r.Outputs = outputs
	if err != nil {
		return fail(err)
	}
	outputIdentities, err := captureArtifactIdentities(outputs)
	if err != nil {
		return fail(fmt.Errorf("агент %s: фиксация output identity: %w", name, err))
	}

	var outputPaths []string
	for _, out := range outputs {
		outputPaths = append(outputPaths, out.Path)
	}
	r.Verdict, err = verdict.FromOutputsContract(outputPaths, a.Verdict)
	if err != nil {
		return fail(fmt.Errorf("агент %s: %w", name, err))
	}

	if r.Verdict.IsNegative() {
		if err := verifyArtifactIdentities(outputs, outputIdentities); err != nil {
			r.ValidationFailed = true
			return fail(fmt.Errorf("агент %s: output изменён после verdict parse: %w", name, err))
		}
		r.Status = notifier.StatusRejected
		r.Duration = time.Since(stageStart)
		return r
	}

	if a.Kind == "delivery" {
		plan, planErr := deliveryPlanFromOutputs(outputs)
		if planErr != nil {
			r.ValidationFailed = true
			return fail(fmt.Errorf("агент %s: %w", name, planErr))
		}
		if _, prepareErr := delivery.Prepare(rs.runCfg.TargetDir, rs.runCfg.Feature, plan); prepareErr != nil {
			return fail(fmt.Errorf("агент %s: подготовка delivery state: %w", name, prepareErr))
		}
		if approvalErr := rs.authorizeDelivery(name, plan); approvalErr != nil {
			r.ControlStopped = true
			return fail(approvalErr)
		}
		deliveryResult, deliveryErr := rs.p.delivery.Execute(stageCtx, delivery.Request{
			TargetDir: rs.runCfg.TargetDir, Feature: rs.runCfg.Feature, Plan: plan,
		})
		r.Delivery = &deliveryResult
		if deliveryErr != nil {
			return fail(fmt.Errorf("агент %s: delivery execution: %w", name, deliveryErr))
		}
	}

	definitions := mergeChecks(a.Checks, agentCfg.Checks)
	if len(definitions) > 0 {
		r.Checks, err = (checks.Runner{TargetDir: rs.runCfg.TargetDir}).RunAll(stageCtx, definitions)
		if err != nil {
			r.ValidationFailed = true
			return fail(fmt.Errorf("агент %s: детерминированные проверки: %w", name, err))
		}
		if stageCtx.Err() != nil {
			return fail(stageCtx.Err())
		}
	}
	if err := verifyArtifactIdentities(outputs, outputIdentities); err != nil {
		r.ValidationFailed = true
		return fail(fmt.Errorf("агент %s: output изменён после controller checks: %w", name, err))
	}
	finalVerdict, err := verdict.FromOutputsContract(outputPaths, a.Verdict)
	if err != nil || finalVerdict != r.Verdict {
		r.ValidationFailed = true
		if err == nil {
			err = fmt.Errorf("verdict изменился с %s на %s", r.Verdict, finalVerdict)
		}
		return fail(fmt.Errorf("агент %s: повторная валидация verdict: %w", name, err))
	}
	r.Status = notifier.StatusPassed
	r.Duration = time.Since(stageStart)
	return r
}

func captureArtifactIdentities(artifacts []runtime.Artifact) (map[string]string, error) {
	identities := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		artifactType, size, digest, err := evidence.ArtifactDigest(artifact.Path)
		if err != nil {
			return nil, err
		}
		identities[artifact.Path] = fmt.Sprintf("%s:%d:%s", artifactType, size, digest)
	}
	return identities, nil
}

func verifyArtifactIdentities(artifacts []runtime.Artifact, expected map[string]string) error {
	actual, err := captureArtifactIdentities(artifacts)
	if err != nil {
		return err
	}
	for path, identity := range expected {
		if actual[path] != identity {
			return fmt.Errorf("%s identity mismatch: expected=%s actual=%s", path, identity, actual[path])
		}
	}
	return nil
}

func mergeChecks(base, overrides []checks.Definition) []checks.Definition {
	merged := append([]checks.Definition(nil), base...)
	positions := make(map[string]int, len(merged))
	for i, definition := range merged {
		positions[definition.Name] = i
	}
	for _, override := range overrides {
		if index, exists := positions[override.Name]; exists {
			merged[index] = override
			continue
		}
		positions[override.Name] = len(merged)
		merged = append(merged, override)
	}
	return merged
}

func (rs *runState) enforceMutationGuard(
	a *agent.Agent,
	name string,
	workspaceBefore filesystemSnapshot,
	gitBefore gitMetadataSnapshot,
	gitAvailable, guardWorkspace bool,
	artifactBefore filesystemSnapshot,
	result *notifier.StageResult,
) error {
	var guardErrors []error
	if guardWorkspace {
		workspaceAfter, err := captureWorkspaceSnapshot(rs.runCfg.TargetDir)
		if err != nil {
			guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить workspace state: %w", name, err))
		} else {
			changedPaths := changedSnapshotPaths(workspaceBefore, workspaceAfter)
			result.Mutations = append([]string(nil), changedPaths...)
			if a.Mutation == "none" && workspaceBefore.Fingerprint != workspaceAfter.Fingerprint {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s нарушил mutation policy: read-only этап изменил проект", name))
			}
			if a.Mutation == "source" || a.Mutation == "tests" {
				var denied []string
				for _, changedPath := range changedPaths {
					if !scope.MatchAny(a.AllowedPaths, changedPath) {
						denied = append(denied, changedPath)
					}
				}
				if len(denied) > 0 {
					guardErrors = append(guardErrors, fmt.Errorf("агент %s нарушил mutation policy: пути вне allowed_paths: %s", name, strings.Join(denied, ", ")))
				}
				var dirtyTouched []string
				for _, changedPath := range changedPaths {
					if rs.userOwnedPaths[changedPath] {
						dirtyTouched = append(dirtyTouched, changedPath)
					}
				}
				if len(dirtyTouched) > 0 {
					guardErrors = append(guardErrors, fmt.Errorf("агент %s изменил user-owned файлы, существовавшие до run: %s", name, strings.Join(dirtyTouched, ", ")))
				}
			}
			if a.RequireDiff && len(changedPaths) == 0 {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s не создал изменений в коде", name))
			}
		}
		if gitAvailable {
			gitAfter, stillAvailable, err := captureGitMetadataSnapshot(rs.runCfg.TargetDir)
			if err != nil || !stillAvailable {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить git metadata state: %w", name, err))
			} else if gitBefore.Fingerprint != gitAfter.Fingerprint {
				guardErrors = append(guardErrors, fmt.Errorf("агент %s нарушил mutation policy: изменил HEAD, branch или git index", name))
			}
		}
	}

	artifactAfter, err := captureArtifactSnapshot(rs.task.ArtifactRoot)
	if err != nil {
		guardErrors = append(guardErrors, fmt.Errorf("агент %s: не удалось проверить artifact state: %w", name, err))
	} else {
		var denied []string
		for _, changedPath := range changedSnapshotPaths(artifactBefore, artifactAfter) {
			fullPath := filepath.Join(rs.task.ArtifactRoot, filepath.FromSlash(changedPath))
			info, statErr := os.Lstat(fullPath)
			unsafeLink := statErr == nil && info.Mode()&os.ModeSymlink != 0
			if unsafeLink || !rs.artifactMutationAllowed(a, name, changedPath) {
				denied = append(denied, changedPath)
			}
		}
		if len(denied) > 0 {
			guardErrors = append(guardErrors, fmt.Errorf("агент %s изменил undeclared artifacts: %s", name, strings.Join(denied, ", ")))
		}
	}
	return errors.Join(guardErrors...)
}

func (rs *runState) artifactMutationAllowed(a *agent.Agent, name, relative string) bool {
	relative = filepath.ToSlash(relative)
	allowedFiles := []string{
		filepath.ToSlash(filepath.Join(rs.runCfg.Feature, "status", name+".md")),
		filepath.ToSlash(filepath.Join(rs.runCfg.Feature, ".stage-summary", name+".md")),
	}
	for _, allowed := range allowedFiles {
		if relative == allowed {
			return true
		}
	}
	for _, outputPath := range a.Outputs {
		output := filepath.ToSlash(filepath.Clean(filepath.FromSlash(runtime.ReplaceVars(outputPath, rs.runCfg.Feature))))
		if relative == output {
			return true
		}
		fullOutput := filepath.Join(rs.task.ArtifactRoot, filepath.FromSlash(output))
		if info, err := os.Lstat(fullOutput); err == nil && info.IsDir() && strings.HasPrefix(relative, output+"/") {
			return true
		}
	}
	return false
}

func deliveryPlanFromOutputs(outputs []runtime.Artifact) (delivery.Plan, error) {
	for _, output := range outputs {
		if output.Name != "plan" {
			continue
		}
		data, err := os.ReadFile(output.Path)
		if err != nil {
			return delivery.Plan{}, err
		}
		return delivery.Parse(data)
	}
	return delivery.Plan{}, fmt.Errorf("delivery output plan отсутствует")
}

func toEvidenceArtifacts(artifacts []runtime.Artifact) []evidence.Artifact {
	result := make([]evidence.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		result = append(result, evidence.Artifact{Name: artifact.Name, Path: artifact.Path})
	}
	return result
}

func toRuntimeArtifacts(artifacts []evidence.Artifact) []runtime.Artifact {
	result := make([]runtime.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		result = append(result, runtime.Artifact{Name: artifact.Name, Path: artifact.Path, Source: artifact.SourcePath})
	}
	return result
}

func (rs *runState) deriveStageState(result *notifier.StageResult) {
	execution := workflow.ExecutionSucceeded
	switch {
	case errors.Is(result.Err, context.Canceled):
		execution = workflow.ExecutionCanceled
	case errors.Is(result.Err, context.DeadlineExceeded):
		execution = workflow.ExecutionTimedOut
	case result.Err != nil && !result.ValidationFailed && !result.ControlStopped:
		execution = workflow.ExecutionInfraFailed
	}
	checkWarning := false
	for _, check := range result.Checks {
		if check.Policy == checks.PolicyOptional && check.Status != checks.StatusPassed {
			checkWarning = true
			break
		}
	}
	state, err := workflow.DeriveAttempt(workflow.AttemptFacts{
		Execution: execution, Verdict: result.Verdict,
		Blocked: result.Blocker != "", Waived: checkWarning,
		ValidationFailed: result.ValidationFailed, Skipped: result.ControlStopped, Superseded: result.Superseded,
	})
	if err != nil {
		result.Err = errors.Join(result.Err, err)
		state, _ = workflow.DeriveAttempt(workflow.AttemptFacts{Execution: workflow.ExecutionInfraFailed})
	}
	result.State = state
	result.Status = state.LegacyStatus()
}

func (rs *runState) clearStageEphemeral(name string, a *agent.Agent) error {
	paths := []string{
		verdict.StatusFilePath(rs.task.ArtifactRoot, rs.runCfg.Feature, name),
		filepath.Join(rs.task.ArtifactRoot, rs.runCfg.Feature, ".stage-summary", name+".md"),
	}
	for _, outputPath := range a.Outputs {
		fullPath, err := confinedArtifactPath(rs.task.ArtifactRoot, runtime.ReplaceVars(outputPath, rs.runCfg.Feature))
		if err != nil {
			return err
		}
		paths = append(paths, fullPath)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, path := range paths {
		if err := validateRemovalPath(rs.task.ArtifactRoot, path); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func validateRemovalPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("отказ удаления пути вне artifact root: %s", target)
	}
	current := rootAbs
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("отказ удаления через symbolic link: %s", current)
		}
	}
	return nil
}

func confinedArtifactPath(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	if full == rootAbs || !strings.HasPrefix(full, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q выходит за пределы %s", relative, rootAbs)
	}
	return full, nil
}

func validateExistingArtifactPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("artifact path %s находится вне root %s", target, root)
	}
	current := rootAbs
	components := append([]string{""}, strings.Split(relative, string(filepath.Separator))...)
	for index := range components {
		if index > 0 {
			current = filepath.Join(current, components[index])
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact path %s проходит через symlink %s", target, current)
		}
	}
	info, err := os.Lstat(targetAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact path %s должен быть regular file или directory", target)
		}
		if info.Size() > maxArtifactFileBytes {
			return fmt.Errorf("artifact file %s слишком велик: %d > %d bytes", target, info.Size(), maxArtifactFileBytes)
		}
		return nil
	}
	var totalSize int64
	fileCount := 0
	return filepath.WalkDir(targetAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact directory %s содержит symlink %s", target, path)
		}
		relative, relErr := filepath.Rel(targetAbs, path)
		if relErr != nil {
			return relErr
		}
		if relative != "." && len(strings.Split(filepath.ToSlash(relative), "/")) > maxArtifactTreeDepth {
			return fmt.Errorf("artifact directory %s превышает max depth %d", target, maxArtifactTreeDepth)
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact directory %s содержит special file %s", target, path)
		}
		fileCount++
		totalSize += info.Size()
		if fileCount > maxArtifactTreeFiles || totalSize > maxArtifactTreeBytes {
			return fmt.Errorf("artifact directory %s превышает лимит files/bytes (%d/%d)", target, fileCount, totalSize)
		}
		return nil
	})
}

// collectInputs проверяет декларированные входы агента (stat) и добавляет
// loopback-входы; возвращает входы для промпта и полный список для отчёта.
func (rs *runState) collectInputs(a *agent.Agent, name string) ([]runtime.Artifact, []runtime.Artifact, error) {
	var promptInputs, all []runtime.Artifact
	for _, inName := range sortedStringMapKeys(a.Inputs) {
		inPath := a.Inputs[inName]
		replaced := runtime.ReplaceVars(inPath, rs.runCfg.Feature)
		fullPath := filepath.Join(rs.task.ArtifactRoot, replaced)

		if err := validateExistingArtifactPath(rs.task.ArtifactRoot, fullPath); err != nil {
			return nil, all, fmt.Errorf("агент %s: вход %s (%s) небезопасен: %w", name, inName, fullPath, err)
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, all, fmt.Errorf("агент %s: вход %s (%s) не найден: %w", name, inName, fullPath, err)
		}

		fmt.Printf("  %s %s %s(%s, %d байт)\n",
			ui.Colorize("→", ui.ColorBlue),
			inName,
			ui.Colorize(fullPath, ui.ColorBlue),
			info.ModTime().Format(time.RFC3339),
			info.Size(),
		)

		art := runtime.Artifact{Name: inName, Path: fullPath, Size: info.Size(), ModTime: info.ModTime()}
		if !info.IsDir() {
			promptInputs = append(promptInputs, art)
		}
		all = append(all, art)
	}

	for _, extra := range rs.extraInputs[name] {
		if err := validateExistingArtifactPath(rs.task.ArtifactRoot, extra.Path); err != nil {
			return nil, all, fmt.Errorf("агент %s: loopback input %s небезопасен: %w", name, extra.Name, err)
		}
		fmt.Printf("  %s %s %s(loopback)\n",
			ui.Colorize("→", ui.ColorYellow), extra.Name, ui.Colorize(extra.Path, ui.ColorBlue))
		promptInputs = append(promptInputs, extra)
		all = append(all, extra)
	}

	return promptInputs, all, nil
}

func (rs *runState) collectOutputs(a *agent.Agent, name string) ([]runtime.Artifact, error) {
	var outputs []runtime.Artifact
	for _, outName := range sortedStringMapKeys(a.Outputs) {
		outPath := a.Outputs[outName]
		replaced := runtime.ReplaceVars(outPath, rs.runCfg.Feature)
		fullPath := filepath.Join(rs.task.ArtifactRoot, replaced)
		if _, statErr := os.Lstat(fullPath); os.IsNotExist(statErr) {
			return outputs, fmt.Errorf("агент %s: выход %s (%s) не создан: %w", name, outName, fullPath, statErr)
		} else if statErr != nil {
			return outputs, fmt.Errorf("агент %s: выход %s (%s) недоступен: %w", name, outName, fullPath, statErr)
		}

		if err := validateExistingArtifactPath(rs.task.ArtifactRoot, fullPath); err != nil {
			return outputs, fmt.Errorf("агент %s: выход %s (%s) небезопасен: %w", name, outName, fullPath, err)
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			return outputs, fmt.Errorf("агент %s: выход %s (%s) не создан: %w", name, outName, fullPath, err)
		}
		if !info.IsDir() && info.Size() == 0 {
			return outputs, fmt.Errorf("агент %s: выход %s (%s) пуст", name, outName, fullPath)
		}
		if filepath.Ext(fullPath) != "" && !info.Mode().IsRegular() {
			return outputs, fmt.Errorf("агент %s: выход %s (%s) должен быть обычным файлом", name, outName, fullPath)
		}

		art := runtime.Artifact{Name: outName, Path: fullPath, Size: info.Size(), ModTime: info.ModTime()}
		outputs = append(outputs, art)
		fmt.Printf("  %s %s %s(%s, %d байт)\n",
			ui.Colorize("✓", ui.ColorGreen),
			ui.Colorize(outName, ui.ColorBold),
			ui.Colorize(fullPath, ui.ColorBlue),
			info.ModTime().Format(time.RFC3339),
			info.Size(),
		)
	}
	return outputs, nil
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// authorizeStage validates controller-owned prerequisites before planning.
func (rs *runState) authorizeStage(name string) error {
	_, err := rs.p.reg.Load(name)
	if err != nil {
		return fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
	}
	return nil
}

func (rs *runState) validateDeliveryChecks() error {
	_, err := rs.currentDeliveryVerification()
	return err
}

func (rs *runState) currentDeliveryVerification() (delivery.Verification, error) {
	workspaceDigest, err := checks.WorkspaceDigest(rs.runCfg.TargetDir)
	if err != nil {
		return delivery.Verification{}, fmt.Errorf("delivery workspace digest: %w", err)
	}
	for resultIndex := len(rs.results) - 1; resultIndex >= 0; resultIndex-- {
		result := rs.results[resultIndex]
		if result.Superseded || result.Err != nil {
			continue
		}
		for checkIndex := len(result.Checks) - 1; checkIndex >= 0; checkIndex-- {
			check := result.Checks[checkIndex]
			if checks.IsTestEvidence(check) &&
				check.WorkspaceDigestBefore == workspaceDigest && check.WorkspaceDigestAfter == workspaceDigest && check.EvidenceDigest != "" {
				return delivery.Verification{
					SourceRunID: rs.runID, WorkspaceDigest: workspaceDigest, CheckEvidenceDigest: check.EvidenceDigest,
				}, nil
			}
		}
	}
	if plan, prepared, loadErr := delivery.LoadPreparedPlan(rs.runCfg.TargetDir, rs.runCfg.Feature); loadErr != nil {
		return delivery.Verification{}, fmt.Errorf("проверка prepared delivery plan: %w", loadErr)
	} else if prepared {
		if plan.VerifiedWorkspaceDigest != workspaceDigest {
			return delivery.Verification{}, fmt.Errorf("prepared delivery plan проверял workspace %s, текущее состояние %s", plan.VerifiedWorkspaceDigest, workspaceDigest)
		}
		if verifyErr := evidence.VerifyCheckEvidence(filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs"), plan.SourceRunID, plan.CheckEvidenceDigest, workspaceDigest); verifyErr != nil {
			return delivery.Verification{}, fmt.Errorf("prepared delivery provenance: %w", verifyErr)
		}
		return delivery.Verification{
			SourceRunID: plan.SourceRunID, WorkspaceDigest: workspaceDigest, CheckEvidenceDigest: plan.CheckEvidenceDigest,
		}, nil
	}
	return delivery.Verification{}, fmt.Errorf("delivery запрещён: нет успешно выполненного required check класса unit/integration/e2e для точного текущего workspace digest %s", workspaceDigest)
}

func (rs *runState) authorizeDelivery(name string, plan delivery.Plan) error {
	planHash, err := plan.Hash()
	if err != nil {
		return err
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return err
	}
	showPipelineSummary(rs.results)
	fmt.Printf("\n%s\n%s\nPlan SHA-256: %s\n", ui.Colorize("Canonical delivery plan:", ui.ColorBold), canonical, planHash)
	approve := func(mode string) error {
		rs.approvedPlanHash = planHash
		return rs.evidence.Append(evidence.Event{Type: "delivery_plan_approved", Timestamp: time.Now().UTC(), Data: map[string]any{
			"plan_hash": planHash, "mode": mode, "approver": "local-user",
		}})
	}
	if rs.approvedPlanHash != "" {
		if rs.approvedPlanHash != planHash {
			return fmt.Errorf("delivery approval hash mismatch: approved=%s actual=%s", rs.approvedPlanHash, planHash)
		}
		return approve("hash_flag")
	}
	if !rs.p.prompter.Interactive() {
		fmt.Printf("Для продолжения: ai-team run --feature %s --retry-from %s --approve-plan %s\n", rs.runCfg.Feature, name, planHash)
		return &ApprovalRequiredError{Checkpoint: "delivery перед " + name}
	}
	ans := rs.p.prompter.Ask(fmt.Sprintf("%s %s может выполнить commit/push/PR. Продолжить? [y/N]",
		ui.Colorize("Delivery:", ui.ColorBold), ui.Colorize(name, ui.ColorYellow)))
	if ans != "y" {
		return fmt.Errorf("%w: delivery перед %s", ErrUserStopped, name)
	}
	return approve("interactive_exact_plan")
}

func (rs *runState) writeDeliveryPlan(ctx context.Context, a *agent.Agent, preconditions map[string]delivery.PreconditionEvidence) error {
	files := rs.attributedDeliveryFiles()
	var plan delivery.Plan
	var err error
	if len(files) == 0 {
		var exists bool
		plan, exists, err = delivery.LoadPreparedPlan(rs.runCfg.TargetDir, rs.runCfg.Feature)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("delivery planner: в текущем run нет атрибутированных изменений и prepared plan отсутствует")
		}
		workspaceDigest, digestErr := checks.WorkspaceDigest(rs.runCfg.TargetDir)
		if digestErr != nil {
			return digestErr
		}
		if verifyErr := delivery.VerifyPreparedWorkspace(rs.runCfg.TargetDir, plan, workspaceDigest); verifyErr != nil {
			return verifyErr
		}
		if verifyErr := evidence.VerifyCheckEvidence(filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs"), plan.SourceRunID, plan.CheckEvidenceDigest, workspaceDigest); verifyErr != nil {
			return fmt.Errorf("prepared delivery provenance: %w", verifyErr)
		}
		if verifyErr := delivery.VerifyPreconditions(plan, preconditions); verifyErr != nil {
			return verifyErr
		}
	} else {
		verification, verificationErr := rs.currentDeliveryVerification()
		if verificationErr != nil {
			return verificationErr
		}
		verification.Preconditions = preconditions
		plan, err = delivery.BuildPlan(ctx, rs.runCfg.TargetDir, rs.runCfg.Feature, rs.runCfg.TaskDesc, files, verification)
		if err != nil {
			return err
		}
	}
	planPath, exists := a.Outputs["plan"]
	if !exists {
		return fmt.Errorf("delivery definition не содержит output plan")
	}
	fullPath, err := confinedArtifactPath(rs.task.ArtifactRoot, runtime.ReplaceVars(planPath, rs.runCfg.Feature))
	if err != nil {
		return err
	}
	return delivery.WritePlan(fullPath, plan)
}

func (rs *runState) attributedDeliveryFiles() []string {
	seen := make(map[string]bool)
	var files []string
	for _, result := range rs.results {
		if result.Err != nil {
			continue
		}
		for _, changedPath := range result.Mutations {
			changedPath = filepath.ToSlash(changedPath)
			if changedPath == ".ai-team" || strings.HasPrefix(changedPath, ".ai-team/") || seen[changedPath] {
				continue
			}
			seen[changedPath] = true
			files = append(files, changedPath)
		}
	}
	sort.Strings(files)
	return files
}

func validateSnapshotPreconditions(name string, a *agent.Agent, inputs []runtime.Artifact) (map[string]delivery.PreconditionEvidence, error) {
	result := make(map[string]delivery.PreconditionEvidence, len(a.Preconditions))
	byName := make(map[string]runtime.Artifact, len(inputs))
	for _, input := range inputs {
		byName[input.Name] = input
	}
	inputNames := make([]string, 0, len(a.Preconditions))
	for inputName := range a.Preconditions {
		inputNames = append(inputNames, inputName)
	}
	sort.Strings(inputNames)
	for _, inputName := range inputNames {
		artifact, exists := byName[inputName]
		if !exists {
			return nil, fmt.Errorf("агент %s: immutable precondition input %s отсутствует", name, inputName)
		}
		actual, err := verdict.FromOutputsContract([]string{artifact.Path}, a.Preconditions[inputName])
		if err != nil {
			return nil, fmt.Errorf("агент %s: precondition %s не выполнен на immutable snapshot: %w", name, inputName, err)
		}
		if actual.IsNegative() {
			return nil, fmt.Errorf("агент %s: precondition %s отклонён verdict %s", name, inputName, actual)
		}
		artifactType, size, digest, err := evidence.ArtifactDigest(artifact.Path)
		if err != nil {
			return nil, fmt.Errorf("агент %s: precondition %s digest: %w", name, inputName, err)
		}
		result[inputName] = delivery.PreconditionEvidence{Type: artifactType, Size: size, SHA256: digest, Verdict: string(actual)}
	}
	return result, nil
}

// enforce обрабатывает негативный вердикт: сначала loopback (интерактивно,
// если сконфигурирован), затем политика on_negative_verdict.
// Возвращает индекс цели loopback (-1, если loopback не выполняется).
func (rs *runState) enforce(i int, name string, r notifier.StageResult) (int, error) {
	agentCfg := rs.p.cfg.AgentConfig(name)
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Name: name, OnNegativeVerdict: config.OnNegativeStop}
	}

	targetName := agentCfg.LoopbackTo
	if targetName == "" {
		targetName = "coder"
	}
	targetIdx := findLoopbackTarget(rs.names, i, targetName)

	if targetIdx >= 0 {
		target := rs.names[targetIdx]
		targetCfg := rs.p.cfg.AgentConfig(target)
		maxRetries := 0
		if targetCfg != nil {
			maxRetries = targetCfg.MaxRetries
		}
		if maxRetries > 0 {
			if rs.retryCounts[target] >= maxRetries {
				return -1, fmt.Errorf("превышен лимит retries (%d) для %s", maxRetries, target)
			}
			if rs.p.prompter.Interactive() {
				for {
					ans := rs.p.prompter.Ask(fmt.Sprintf("%s %s: вердикт %s — отправить обратно %s-у? retry %d/%d [Y/n/diff]",
						ui.Colorize("⟳", ui.ColorYellow),
						ui.Colorize(name, ui.ColorYellow),
						r.Verdict, target,
						rs.retryCounts[target]+1, maxRetries))
					switch ans {
					case "y", "":
						rs.retryCounts[target]++
						rs.extraInputs[target] = r.Outputs
						return targetIdx, nil
					case "diff":
						fmt.Println(gitDiffOutput(rs.runCfg.TargetDir))
					case "n":
						return -1, fmt.Errorf("%w: отказ от retry после вердикта %s от %s", ErrUserStopped, r.Verdict, name)
					default:
						fmt.Printf("  неизвестный ответ: %s (ожидалось Y/n/diff)\n", ans)
					}
				}
			}
		}
	}

	switch agentCfg.OnNegativeVerdict {
	case config.OnNegativeContinue:
		fmt.Printf("  %s вердикт %s от %s — продолжаю (on_negative_verdict: continue)\n",
			ui.Colorize("⚠", ui.ColorYellow), r.Verdict, name)
		return -1, nil
	case config.OnNegativeAsk:
		if rs.p.prompter.Interactive() {
			ans := rs.p.prompter.Ask(fmt.Sprintf("Вердикт %s от %s. Продолжить несмотря на это? [y/N]", r.Verdict, name))
			if ans == "y" {
				return -1, nil
			}
			return -1, fmt.Errorf("%w: после вердикта %s от %s", ErrUserStopped, r.Verdict, name)
		}
		return -1, &NegativeVerdictError{Agent: name, Verdict: r.Verdict}
	default: // stop
		return -1, &NegativeVerdictError{Agent: name, Verdict: r.Verdict}
	}
}

// checkpoints применяет единую checkpoint policy. Legacy gate/transition поля
// нормализуются Config.Checkpoint*Policy и не создают отдельные механизмы.
func (rs *runState) checkpoints(i int, name string, r notifier.StageResult) error {
	agentCfg := rs.p.cfg.AgentConfig(name)
	if agentCfg == nil {
		return nil
	}

	afterPolicy := agentCfg.CheckpointAfterPolicy()
	if afterPolicy != config.CheckpointAuto {
		showGateSummary(name, r, len(rs.names))
		if err := rs.applyCheckpoint("после "+name, afterPolicy, name); err != nil {
			return err
		}
	}

	if i+1 < len(rs.names) {
		nextName := rs.names[i+1]
		nextCfg := rs.p.cfg.AgentConfig(nextName)
		if nextCfg != nil && nextCfg.CheckpointBeforePolicy() != config.CheckpointAuto {
			nextAgent, loadErr := rs.p.reg.Load(nextName)
			if loadErr != nil {
				return fmt.Errorf("ошибка загрузки агента %s: %w", nextName, loadErr)
			}
			// Delivery имеет отдельный mandatory approval в authorizeStage.
			if nextAgent.Kind != "delivery" {
				showPipelineSummary(rs.results)
				if err := rs.applyCheckpoint("перед "+nextName, nextCfg.CheckpointBeforePolicy(), name); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (rs *runState) applyCheckpoint(label, policy, summaryAgent string) error {
	if policy == config.CheckpointAuto {
		return nil
	}
	subjectHash, err := rs.checkpointSubjectHash(label, summaryAgent)
	if err != nil {
		return err
	}
	fmt.Printf("  Checkpoint subject SHA-256: %s\n", subjectHash)
	record := func(eventType, mode string) error {
		return rs.evidence.Append(evidence.Event{Type: eventType, Timestamp: time.Now().UTC(), Data: map[string]any{
			"label": label, "subject_hash": subjectHash, "mode": mode, "actor": "local-user",
		}})
	}
	if rs.runCfg.ApproveGates {
		return record("checkpoint_approved", "blanket_flag_at_reached_checkpoint")
	}
	if !rs.p.prompter.Interactive() {
		if err := record("checkpoint_approval_required", "non_interactive"); err != nil {
			return err
		}
		return &ApprovalRequiredError{Checkpoint: "checkpoint " + label}
	}
	for {
		ans := rs.p.prompter.Ask(fmt.Sprintf("%s %s %s",
			ui.Colorize("Checkpoint:", ui.ColorBold), ui.Colorize(label, ui.ColorYellow),
			ui.Colorize("[Y/n/diff/summary]", ui.ColorBold)))
		switch ans {
		case "y", "":
			return record("checkpoint_approved", "interactive_exact_subject")
		case "n":
			if err := record("checkpoint_rejected", "interactive_exact_subject"); err != nil {
				return err
			}
			return fmt.Errorf("%w: checkpoint %s", ErrUserStopped, label)
		case "diff":
			fmt.Println(gitDiffOutput(rs.runCfg.TargetDir))
		case "summary":
			fmt.Println(report.ReadStageSummary(rs.task.ArtifactRoot, rs.runCfg.Feature, summaryAgent))
		default:
			fmt.Printf("  неизвестный ответ: %s (ожидалось Y/n/diff/summary)\n", ans)
		}
	}
}

func (rs *runState) checkpointSubjectHash(label, stage string) (string, error) {
	type artifactSubject struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	type subject struct {
		RunID      string                `json:"run_id"`
		Label      string                `json:"label"`
		AttemptID  string                `json:"attempt_id"`
		Stage      string                `json:"stage"`
		State      workflow.AttemptState `json:"state"`
		Verdict    verdict.Verdict       `json:"verdict,omitempty"`
		Artifacts  []artifactSubject     `json:"artifacts,omitempty"`
		CheckProof []string              `json:"check_evidence_digests,omitempty"`
	}
	value := subject{RunID: rs.runID, Label: label, Stage: stage}
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.Name != stage || result.Superseded {
			continue
		}
		value.AttemptID, value.State, value.Verdict = result.AttemptID, result.State, result.Verdict
		for _, output := range result.Outputs {
			artifactType, size, digest, err := evidence.ArtifactDigest(output.Path)
			if err != nil {
				return "", fmt.Errorf("checkpoint %s artifact %s: %w", label, output.Name, err)
			}
			value.Artifacts = append(value.Artifacts, artifactSubject{Name: output.Name, Type: artifactType, Size: size, SHA256: digest})
		}
		for _, check := range result.Checks {
			if check.EvidenceDigest != "" {
				value.CheckProof = append(value.CheckProof, check.EvidenceDigest)
			}
		}
		break
	}
	if value.AttemptID == "" {
		return "", fmt.Errorf("checkpoint %s: subject stage %s не найден", label, stage)
	}
	sort.Strings(value.CheckProof)
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

// prepareRetryFrom валидирует выходные артефакты пропускаемых агентов
// и возвращает индекс старта.
func (rs *runState) prepareRetryFrom() (int, error) {
	startIdx := -1
	for i, name := range rs.names {
		if name == rs.runCfg.RetryFrom {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return 0, fmt.Errorf("агент %s не найден в пайплайне", rs.runCfg.RetryFrom)
	}

	for i := 0; i < startIdx; i++ {
		name := rs.names[i]
		a, err := rs.p.reg.Load(name)
		if err != nil {
			return 0, fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
		}
		for _, outPath := range a.Outputs {
			replaced := runtime.ReplaceVars(outPath, rs.runCfg.Feature)
			fullPath := filepath.Join(rs.task.ArtifactRoot, replaced)
			if err := validateExistingArtifactPath(rs.task.ArtifactRoot, fullPath); err != nil {
				return 0, fmt.Errorf("missing artifacts from previous stage: %s (%s)", name, fullPath)
			}
		}
		fmt.Printf("  %s %s пропущен\n", ui.Colorize("⏭", ui.ColorCyan), ui.Colorize(name, ui.ColorYellow))
	}

	fmt.Printf("%s  %s: перезапуск с %s\n",
		ui.Colorize("⟳", ui.ColorYellow),
		ui.Colorize("Retry", ui.ColorCyan),
		ui.Colorize(rs.runCfg.RetryFrom, ui.ColorYellow))
	return startIdx, nil
}

// finalize — итоговый отчёт, статус-бар, сводка, запись финального статуса.
// Вызывается на всех исходах, включая отмену контекста.
func (rs *runState) finalize(runErr error) (workflow.RunOutcome, error) {
	endTime := time.Now()
	status := runStatusFor(runErr, rs.results)
	var finalizeErr error
	if err := report.GenerateFinalReport(rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime, rs.task.ArtifactRoot, status); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("final report: %w", err))
	} else if err := rs.evidence.PublishReportTree(rs.runCfg.Feature, filepath.Join(rs.reportsDir, rs.runCfg.Feature)); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("publish immutable report: %w", err))
	}
	if finalizeErr != nil {
		status = string(workflow.RunFailed)
		if err := report.GenerateFinalReport(rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime, rs.task.ArtifactRoot, status); err != nil {
			finalizeErr = errors.Join(finalizeErr, fmt.Errorf("failed-status report: %w", err))
		}
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "run_finished", Timestamp: endTime.UTC(),
		Data: map[string]any{"status": status, "stage_attempts": len(rs.results)},
	}); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("запись run_finished: %w", err))
		status = string(workflow.RunFailed)
		_ = report.GenerateFinalReport(rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime, rs.task.ArtifactRoot, status)
	}
	rs.ps.Finalize()
	rs.printSummary()
	if rs.p.recorder != nil {
		rs.p.recorder.RunFinished(rs.runID, status, endTime.UTC())
	}
	combinedErr := errors.Join(runErr, finalizeErr)
	outcome := workflow.RunOutcome(status)
	if combinedErr != nil {
		return outcome, &RunError{Outcome: outcome, Err: combinedErr}
	}
	return outcome, nil
}

// runStatus — финальный статус запуска для store.
func runStatus(err error) string {
	return string(workflow.DeriveRun(runSignal(err), nil))
}

func runStatusFor(err error, results []notifier.StageResult) string {
	states := make([]workflow.AttemptState, 0, len(results))
	for _, result := range results {
		state := result.State
		if state.Execution == "" {
			execution := workflow.ExecutionSucceeded
			if result.Err != nil {
				execution = workflow.ExecutionInfraFailed
			}
			state, _ = workflow.DeriveAttempt(workflow.AttemptFacts{
				Execution: execution, Verdict: result.Verdict,
				Blocked: result.Status == notifier.StatusBlocked, Superseded: result.Superseded,
			})
		}
		states = append(states, state)
	}
	return string(workflow.DeriveRun(runSignal(err), states))
}

func runSignal(err error) workflow.RunSignal {
	if err == nil {
		return workflow.SignalCompleted
	}
	var runErr *RunError
	if errors.As(err, &runErr) {
		return workflow.RunSignal(runErr.Outcome)
	}
	switch {
	case isBlockedErr(err):
		return workflow.SignalBlocked
	case isUserStopped(err):
		return workflow.SignalStopped
	case errors.Is(err, context.Canceled):
		return workflow.SignalCanceled
	default:
		return workflow.SignalFailed
	}
}

func isBlockedErr(err error) bool {
	var be *BlockedError
	return errors.As(err, &be)
}

func isUserStopped(err error) bool {
	return errors.Is(err, ErrUserStopped)
}

func (rs *runState) printSummary() {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	title := fmt.Sprintf("=== ИТОГ ПАЙПЛАЙНА: %s ===", rs.runCfg.Feature)
	fmt.Fprintf(w, "%s\n", ui.Colorize(title, ui.ColorBold))

	fmt.Fprintf(w, "%s\t%s\t\t%s\n",
		ui.Colorize("Этап", ui.ColorCyan),
		ui.Colorize("Статус", ui.ColorCyan),
		ui.Colorize("Результат", ui.ColorCyan),
	)
	fmt.Fprintf(w, "───\t───\t\t───\n")

	for _, r := range rs.results {
		var status string
		switch r.Status {
		case notifier.StatusBlocked:
			status = ui.ColoredStatusBlocked()
		case notifier.StatusRejected, notifier.StatusFailed, notifier.StatusCanceled:
			status = ui.ColoredStatus(false)
		case notifier.StatusWarning:
			status = ui.Colorize("!", ui.ColorYellow)
		case notifier.StatusSkipped:
			status = ui.Colorize("⏸", ui.ColorCyan)
		case notifier.StatusInvalidated:
			status = ui.Colorize("↺", ui.ColorCyan)
		default:
			status = ui.ColoredStatus(r.Status == notifier.StatusPassed && r.Err == nil)
		}

		var resultStr string
		switch {
		case r.Err != nil:
			resultStr = ui.Colorize(shortenError(r.Err), ui.ColorRed)
		case r.Status == notifier.StatusBlocked:
			resultStr = ui.Colorize("BLOCKED: "+ui.Truncate(r.Blocker, 60), ui.ColorYellow)
		default:
			var labels []string
			for _, out := range r.Outputs {
				labels = append(labels, ui.Colorize(out.Name, ui.ColorGreen))
			}
			if len(labels) == 0 {
				labels = append(labels, "—")
			}
			resultStr = strings.Join(labels, ", ")
			if r.Verdict != "" {
				resultStr += " (" + string(r.Verdict) + ")"
			}
		}

		fmt.Fprintf(w, "%s\t%s\t\t%s\n",
			ui.Colorize(r.Name, ui.ColorYellow),
			status,
			resultStr,
		)
	}

	fmt.Fprintf(w, "\n%s  %s\n",
		ui.Colorize("📄", ui.ColorBold),
		ui.Colorize("Report: "+filepath.Join(rs.reportsDir, rs.runCfg.Feature, "index.html"), ui.ColorCyan),
	)

	w.Flush()
	fmt.Println()
}

func shortenError(err error) string {
	return ui.Truncate(err.Error(), 80)
}
