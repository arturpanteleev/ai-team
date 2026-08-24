package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
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
	RunResumed(runID string, resumedAt time.Time)
	RunAttached(runID string)
	RunPaused(runID, status string, pausedAt time.Time)
	RunCanceled(runID string, canceledAt time.Time)
	ApprovalRequested(runID, approvalID, attemptID string, at time.Time, data map[string]any)
	ApprovalDecided(runID, approvalID, attemptID string, at time.Time, data map[string]any)
	TransitionSelected(runID, attemptID string, at time.Time, data map[string]any)
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
	ResumeRunID     string
	Feature         string
	TaskDesc        string
	TargetDir       string
	retryFrom       string // внутреннее: следующий этап при resume; не задаётся извне
	ApproveGates    bool
	ApprovePlanHash string
	// WorkspaceLock — уже захваченный контроллером workspace lock.
	// Обязателен к захвату до постановки run в исполнение; nil означает,
	// что pipeline захватывает lock самостоятельно (CLI-путь).
	WorkspaceLock        *evidence.WorkspaceLock
	resumeDecisionAction string
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
	ps               *ui.PipelineStatus
	startTime        time.Time
	approvedPlanHash string
	runID            string
	evidence         *evidence.Store
	attemptOrdinal   int
	userOwnedPaths   map[string]bool
	lifecycleStore   *lifecycle.Store
	lifecycleState   lifecycle.State
	approvalStore    *approval.Store
	resumedApproval  *approval.PendingApproval
	resumed          bool
	graph            workflow.Graph
	visits           map[string]int
	candidate        *candidate.Manager
	sourceTarget     string
	liveWorkspaceSHA string
}

func (rs *runState) sourceDir() string {
	if rs.sourceTarget != "" {
		return rs.sourceTarget
	}
	return rs.runCfg.TargetDir
}

func (p *Pipeline) Run(ctx context.Context, runCfg RunConfig) error {
	_, err := p.RunWithResult(ctx, runCfg)
	return err
}

func (p *Pipeline) RunWithResult(ctx context.Context, runCfg RunConfig) (RunResult, error) {
	if err := p.cfg.Validate(p.reg); err != nil {
		return RunResult{}, err
	}
	compiledGraph, err := p.cfg.CompiledGraph()
	if err != nil {
		return RunResult{}, err
	}
	if runCfg.ResumeRunID == "" && !workflow.ValidFeature(runCfg.Feature) {
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
	// Контроллер (web control plane) может передать уже захваченный lock —
	// это синхронное резервирование target до ответа 202 и устранение окна
	// «призрачных» runs. Владение переходит: RunWithResult закрывает lock.
	workspaceLock := runCfg.WorkspaceLock
	if workspaceLock == nil {
		workspaceLock, err = evidence.AcquireWorkspaceLock(runCfg.TargetDir)
		if err != nil {
			return RunResult{}, err
		}
	}
	defer func() {
		if closeErr := workspaceLock.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "  %s workspace unlock error: %v\n", ui.Colorize("⚠", ui.ColorYellow), closeErr)
		}
	}()

	lifecycleStore, err := lifecycle.NewStore(runCfg.TargetDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("lifecycle store: %w", err)
	}
	approvalStore, err := approval.NewStore(runCfg.TargetDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("approval store: %w", err)
	}
	var resumedState lifecycle.State
	var resumedApproval *approval.PendingApproval
	var resumedTransitionData map[string]any
	if runCfg.ResumeRunID != "" {
		resumedState, err = lifecycleStore.Load(runCfg.ResumeRunID)
		if err != nil {
			return RunResult{}, fmt.Errorf("resume run: %w", err)
		}
		if resumedState.Phase == lifecycle.PhaseTerminal {
			return RunResult{}, fmt.Errorf("resume run: run %s уже terminal", runCfg.ResumeRunID)
		}
		if resumedState.TargetDir != runCfg.TargetDir {
			return RunResult{}, fmt.Errorf("resume run: target identity mismatch")
		}
		if runCfg.Feature != "" && runCfg.Feature != resumedState.Feature {
			return RunResult{}, fmt.Errorf("resume run: feature нельзя менять")
		}
		runCfg.RunID = resumedState.RunID
		runCfg.Feature = resumedState.Feature
		runCfg.TaskDesc = ""
		runCfg.retryFrom = resumedState.NextStage
		if resumedState.Phase == lifecycle.PhaseWaiting {
			value, loadErr := approvalStore.Load(resumedState.RunID, resumedState.PendingApprovalID)
			if loadErr != nil {
				return RunResult{}, fmt.Errorf("resume approval: %w", loadErr)
			}
			if value.Status != approval.StatusResolved {
				if value.Trigger == "delivery_plan" && runCfg.ApprovePlanHash != "" {
					// CLI-флаг --approve-plan записывает exact-subject решение
					// вместо прямого обхода approval-модели.
					if normalized := strings.ToLower(strings.TrimSpace(runCfg.ApprovePlanHash)); normalized != value.SubjectHash {
						return RunResult{}, fmt.Errorf("resume run: --approve-plan %s не совпадает с subject approval %s", normalized, value.SubjectHash)
					}
					value, err = approvalStore.Decide(value.RunID, value.ID, approval.Decision{
						ActorID: "local-user", ActorRole: deliveryApprovalRole,
						Action: "approve", SubjectHash: value.SubjectHash,
					})
					if err != nil {
						return RunResult{}, fmt.Errorf("resume approval: %w", err)
					}
				} else {
					return RunResult{}, &ApprovalRequiredError{
						Checkpoint: "переход ожидает решения", RunID: value.RunID,
						ApprovalID: value.ID, SubjectHash: value.SubjectHash,
					}
				}
			}
			target := value.Targets[value.ResolvedAction]
			if target == "" {
				return RunResult{}, fmt.Errorf("resume approval: action %s не имеет target", value.ResolvedAction)
			}
			if value.Trigger == "delivery_plan" {
				if value.ResolvedAction != "approve" {
					return RunResult{}, fmt.Errorf("%w: delivery отклонён человеком", ErrUserStopped)
				}
				runCfg.ApprovePlanHash = value.SubjectHash
				resumedApproval = &value
			} else {
				runCfg.retryFrom = target
				runCfg.resumeDecisionAction = value.ResolvedAction
				resumedApproval = &value
				if strings.HasPrefix(value.Trigger, "graph_outcome:") {
					outcome := workflow.Outcome(strings.TrimPrefix(value.Trigger, "graph_outcome:"))
					edge, found := compiledGraph.Edge(value.FromStage, outcome)
					if !found || edge.To != value.ToStage || edge.Approval == nil ||
						edge.Approval.Actions[value.ResolvedAction] != target {
						return RunResult{}, fmt.Errorf("resume approval: graph edge identity mismatch")
					}
					resumedTransitionData = map[string]any{
						"from": value.FromStage, "outcome": string(outcome), "edge_target": edge.To,
						"action": value.ResolvedAction, "target": target,
					}
				}
			}
		}
	}

	// task.md is a workflow input and therefore must be created/read while the
	// workspace lock is held. Otherwise a rejected concurrent run could overwrite
	// the task consumed by the active run before failing to acquire the lock.
	runCfg.TaskDesc, err = prepareTaskArtifact(runCfg)
	if err != nil {
		return RunResult{}, err
	}
	if runCfg.ResumeRunID != "" && runCfg.TaskDesc != resumedState.Task {
		return RunResult{}, fmt.Errorf("resume run: сохранённый task.md изменён")
	}

	runStartedAt := time.Now().UTC()
	runID := runCfg.RunID
	if runID == "" {
		runID, err = evidence.NewRunID(runStartedAt)
		if err != nil {
			return RunResult{}, err
		}
	}
	sourceTarget := runCfg.TargetDir
	var candidateManager *candidate.Manager
	if runCfg.ResumeRunID != "" {
		candidateManager, err = candidate.Load(ctx, runCfg.TargetDir, runID)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resume candidate: %w", err)
		}
	} else {
		var gitAvailable bool
		candidateManager, gitAvailable, err = candidate.Create(ctx, runCfg.TargetDir, runID)
		if err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, err
		}
		if !gitAvailable {
			candidateManager = nil
		}
	}
	if candidateManager != nil {
		sourceTarget = candidateManager.Root()
		if resumedApproval != nil && resumedApproval.CandidateSHA256 != "" {
			identity, identityErr := candidateManager.Identity()
			if identityErr != nil || identity.WorkspaceSHA256 != resumedApproval.CandidateSHA256 {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed},
					fmt.Errorf("resume approval: candidate identity changed after decision request")
			}
		}
		if err := copyTaskToCandidate(runCfg.TargetDir, sourceTarget, runCfg.Feature); err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, err
		}
	}
	if resumedApproval != nil && resumedApproval.CandidateSHA256 != "" && candidateManager == nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed},
			fmt.Errorf("resume approval: candidate worktree отсутствует")
	}
	configSnapshot, workflowSnapshot, err := p.resolvedEvidenceSnapshots()
	if err != nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resolved workflow evidence: %w", err)
	}
	var evidenceStore *evidence.Store
	var replayedRun evidence.ReplayedRun
	var resumeInvalidated []string
	var resumeMutations []string
	attemptOrdinal := 0
	if runCfg.ResumeRunID != "" {
		var manifest evidence.RunManifest
		evidenceStore, manifest, replayedRun, err = evidence.Resume(filepath.Join(runCfg.TargetDir, ".ai-team", "runs"), runID)
		if err != nil {
			return RunResult{}, fmt.Errorf("resume evidence run: %w", err)
		}
		configDigest := sha256.Sum256(configSnapshot)
		workflowDigest := sha256.Sum256(workflowSnapshot)
		if manifest.Feature != runCfg.Feature || manifest.TargetDir != runCfg.TargetDir ||
			manifest.ConfigSHA256 != resumedState.ConfigSHA256 ||
			manifest.ResolvedWorkflowSHA256 != resumedState.WorkflowSHA256 ||
			fmt.Sprintf("%x", configDigest[:]) != resumedState.ConfigSHA256 ||
			fmt.Sprintf("%x", workflowDigest[:]) != resumedState.WorkflowSHA256 {
			return RunResult{}, fmt.Errorf("resume run: config/workflow identity mismatch")
		}
		for _, attempt := range replayedRun.Attempts {
			if attempt.ManifestSHA256 != "" {
				manifestPath := filepath.Join(evidenceStore.RunDir(), "attempts", attempt.AttemptID, "manifest.json")
				data, readErr := safeio.ReadRegularFile(manifestPath, maxArtifactFileBytes)
				if readErr != nil {
					return RunResult{}, readErr
				}
				var attemptManifest evidence.AttemptManifest
				if json.Unmarshal(data, &attemptManifest) != nil {
					return RunResult{}, fmt.Errorf("resume attempt manifest %s повреждён", attempt.AttemptID)
				}
				resumeMutations = append(resumeMutations, attemptManifest.Mutations...)
			}
			if attempt.FinishedAt.IsZero() {
				if err := evidenceStore.Append(evidence.Event{
					Type: "attempt_abandoned", AttemptID: attempt.AttemptID, Stage: attempt.Stage,
					Timestamp: runStartedAt, Data: map[string]any{"reason": "controller restarted"},
				}); err != nil {
					return RunResult{}, err
				}
			}
		}
		if resumedApproval != nil {
			if err := evidenceStore.Append(evidence.Event{
				Type: "approval_decided", AttemptID: resumedApproval.AttemptID,
				Timestamp: runStartedAt, Data: approvalEventData(*resumedApproval),
			}); err != nil {
				return RunResult{}, fmt.Errorf("запись approval_decided: %w", err)
			}
			if resumedTransitionData != nil {
				if err := evidenceStore.Append(evidence.Event{
					Type: "transition_selected", AttemptID: resumedApproval.AttemptID,
					Stage: resumedApproval.FromStage, Timestamp: runStartedAt, Data: resumedTransitionData,
				}); err != nil {
					return RunResult{}, fmt.Errorf("запись resumed transition_selected: %w", err)
				}
			}
			if isBackwardTransition(compiledGraph, resumedApproval) {
				targetIndex := compiledGraph.Index(runCfg.retryFrom)
				for index := range replayedRun.Attempts {
					attempt := &replayedRun.Attempts[index]
					if targetIndex >= 0 && attempt.StageIndex > targetIndex+1 && !attempt.Superseded {
						attempt.Superseded = true
						attempt.State = workflow.Invalidate(attempt.State)
						attempt.Status = attempt.State.LegacyStatus()
						resumeInvalidated = append(resumeInvalidated, attempt.AttemptID)
					}
				}
				if len(resumeInvalidated) > 0 {
					if err := evidenceStore.Append(evidence.Event{
						Type: "attempts_invalidated", Timestamp: runStartedAt,
						Data: map[string]any{
							"from_stage_index": targetIndex + 2,
							"attempt_ids":      resumeInvalidated, "reason": "approved_loopback",
						},
					}); err != nil {
						return RunResult{}, fmt.Errorf("запись loopback invalidation: %w", err)
					}
				}
			}
		}
		if err := evidenceStore.Append(evidence.Event{Type: "run_resumed", Timestamp: runStartedAt}); err != nil {
			return RunResult{}, fmt.Errorf("запись run_resumed: %w", err)
		}
		attemptOrdinal = len(replayedRun.Attempts)
		nextState := resumedState
		nextState.Phase = lifecycle.PhaseRunning
		nextState.PendingApprovalID = ""
		nextState.NextStage = runCfg.retryFrom
		if err := lifecycleStore.Save(resumedState, nextState); err != nil {
			return RunResult{}, err
		}
		resumedState = nextState
	} else {
		evidenceStore, err = evidence.Start(filepath.Join(runCfg.TargetDir, ".ai-team", "runs"), evidence.RunManifest{
			RunID: runID, Feature: runCfg.Feature, TargetDir: runCfg.TargetDir, StartedAt: runStartedAt,
			ConfigSnapshot: configSnapshot, WorkflowSnapshot: workflowSnapshot,
		})
		if err != nil {
			return RunResult{}, fmt.Errorf("создание evidence run: %w", err)
		}
		if err := evidenceStore.Append(evidence.Event{Type: "run_started", Timestamp: runStartedAt}); err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("запись run_started: %w", err)
		}
		configDigest := sha256.Sum256(configSnapshot)
		workflowDigest := sha256.Sum256(workflowSnapshot)
		resumedState = lifecycle.State{
			RunID: runID, Feature: runCfg.Feature, TargetDir: runCfg.TargetDir, Task: runCfg.TaskDesc,
			Phase: lifecycle.PhaseRunning, NextStage: compiledGraph.Entry,
			ConfigSHA256: fmt.Sprintf("%x", configDigest[:]), WorkflowSHA256: fmt.Sprintf("%x", workflowDigest[:]),
			CreatedAt: runStartedAt,
		}
		if err := lifecycleStore.Create(resumedState); err != nil {
			return RunResult{}, fmt.Errorf("создание lifecycle state: %w", err)
		}
	}
	if candidateManager != nil {
		if err := publishCandidateMetadata(evidenceStore.RunDir(), candidateManager.Metadata()); err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, err
		}
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
			TargetDir:    sourceTarget,
			ArtifactRoot: filepath.Join(sourceTarget, ".ai-team", "artifacts"),
			LogDir:       evidenceStore.LogDir(),
			Interactive:  p.prompter.Interactive(),
		},
		reportsDir:       reportsDir,
		names:            p.cfg.AgentNames(),
		extraInputs:      make(map[string][]runtime.Artifact),
		results:          replayedStageResults(replayedRun),
		startTime:        runStartedAt,
		approvedPlanHash: runCfg.ApprovePlanHash,
		runID:            runID,
		evidence:         evidenceStore,
		attemptOrdinal:   attemptOrdinal,
		lifecycleStore:   lifecycleStore,
		lifecycleState:   resumedState,
		approvalStore:    approvalStore,
		resumedApproval:  resumedApproval,
		resumed:          runCfg.ResumeRunID != "",
		graph:            compiledGraph,
		visits:           make(map[string]int),
		candidate:        candidateManager,
		sourceTarget:     sourceTarget,
	}
	for _, previous := range rs.results {
		rs.visits[previous.Name]++
	}
	rs.ps = ui.NewPipelineStatus(filepath.Base(runCfg.TargetDir), runCfg.Feature, len(rs.names))
	rs.task.ConsoleOut = rs.ps.StatusWriter()
	if p.recorder != nil {
		snapshot, _ := yaml.Marshal(p.cfg)
		p.recorder.ReconcileInterrupted(rs.startTime)
		if rs.resumed {
			p.recorder.RunResumed(runID, rs.startTime)
			if resumedApproval != nil {
				p.recorder.ApprovalDecided(runID, resumedApproval.ID, resumedApproval.AttemptID,
					rs.startTime, approvalEventData(*resumedApproval))
				if resumedTransitionData != nil {
					p.recorder.TransitionSelected(runID, resumedApproval.AttemptID, rs.startTime, resumedTransitionData)
				}
			}
			if len(resumeInvalidated) > 0 {
				p.recorder.AttemptsInvalidated(runID, resumeInvalidated, rs.startTime)
			}
		} else {
			p.recorder.RunStarted(runID, runCfg.Feature, string(snapshot), rs.startTime)
		}
	}
	if err := rs.initializeWorkspaceOwnership(); err != nil {
		outcome, finalErr := rs.finalize(err)
		return RunResult{RunID: runID, Outcome: outcome}, finalErr
	}
	for _, mutation := range resumeMutations {
		delete(rs.userOwnedPaths, filepath.ToSlash(mutation))
	}
	if resumedApproval != nil && isBackwardTransition(rs.graph, resumedApproval) {
		inputs, inputErr := rs.stageOutputs(resumedApproval.FromStage, resumedApproval.AttemptID)
		if inputErr != nil {
			outcome, finalErr := rs.finalize(inputErr)
			return RunResult{RunID: runID, Outcome: outcome}, finalErr
		}
		rs.extraInputs[runCfg.retryFrom] = inputs
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
	if rs.runCfg.retryFrom == "" && len(gitState.Dirty) > 0 {
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
	if rs.candidate != nil {
		rs.liveWorkspaceSHA, err = checks.WorkspaceDigest(rs.runCfg.TargetDir)
		if err != nil {
			return fmt.Errorf("live workspace identity: %w", err)
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
		Graph         workflow.Graph  `json:"graph"`
	}
	compiledGraph, err := p.cfg.CompiledGraph()
	if err != nil {
		return nil, nil, err
	}
	resolved := resolvedWorkflow{SchemaVersion: 2, Graph: compiledGraph}
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
	if runCfg.retryFrom != "" {
		data, err := safeio.ReadRegularFile(taskPath, maxArtifactFileBytes)
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("для фичи %q ещё нет сохранённого task.md — сначала запустите run с --task", runCfg.Feature)
		}
		if err != nil {
			return "", fmt.Errorf("сохранённый task.md не читается (%s): %w", taskPath, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("сохранённый task.md пуст (%s)", taskPath)
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

func copyTaskToCandidate(controlTarget, candidateTarget, feature string) error {
	source := filepath.Join(controlTarget, ".ai-team", "artifacts", "tasks", feature, "task.md")
	data, err := safeio.ReadRegularFile(source, maxArtifactFileBytes)
	if err != nil {
		return fmt.Errorf("candidate task input: %w", err)
	}
	directory, err := safeio.EnsureDir(candidateTarget, ".ai-team", "artifacts", "tasks", feature)
	if err != nil {
		return err
	}
	destination := filepath.Join(directory, "task.md")
	if err := safeio.RejectSymlink(destination); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".task-projection-*.tmp")
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
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}

func publishCandidateMetadata(runDir string, metadata candidate.Metadata) error {
	path := filepath.Join(runDir, "candidate-metadata.json")
	if existing, err := safeio.ReadRegularFile(path, maxArtifactFileBytes); err == nil {
		var stored candidate.Metadata
		if json.Unmarshal(existing, &stored) != nil || stored != metadata {
			return fmt.Errorf("candidate evidence metadata identity mismatch")
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeControllerJSON(path, metadata)
}
