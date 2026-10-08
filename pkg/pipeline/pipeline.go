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
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/containment"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
	"github.com/arturpanteleev/ai-team/pkg/report"
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

// ApprovalStore is the persistence port used by pipeline approval decisions.
// Local CLI runs default to the filesystem store; web and scheduler workers
// can use the shared SQLite store. Sharing that database does not isolate a
// worker process. Runtime output is never treated as a human decision.
type ApprovalStore interface {
	Create(approval.PendingApproval) (approval.PendingApproval, error)
	Load(runID, approvalID string) (approval.PendingApproval, error)
	List(runID string) ([]approval.PendingApproval, error)
	Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
	ResolveDeferred(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
}

// UsageEnvelopeWriter publishes the final run summary. Cloud workers use a
// per-invocation controller API; local CLI runs retain the filesystem path.
type UsageEnvelopeWriter interface {
	WriteUsageEnvelope(metrics.UsageEnvelope) error
}

// TerminalRecordWriter persists worker-supplied terminal delivery metadata.
// Cloud workers route writes through their scoped controller API.
type TerminalRecordWriter interface {
	WriteTerminalRecord(delivery.TerminalRecord) error
}

// AttestationWriter persists the worker-supplied terminal statement in
// controller-owned storage for sandboxed workers.
type AttestationWriter interface{ WriteAttestation(*attest.Statement) error }

type ContainmentReceiptWriter interface {
	WriteContainmentReceipt(containment.Receipt) error
}

// CandidateEvidenceStore keeps the two generated gate identity documents in
// controller-owned storage for isolated workers. Stage agents still receive a
// worker-visible projection as an ordinary input artifact.
type CandidateEvidenceStore interface {
	WriteCandidateEvidence(name string, document CandidateEvidence) error
	ReadCandidateEvidence(name string) (CandidateEvidence, error)
}

type AttemptManifestWriter interface {
	WriteAttemptManifest(evidence.AttemptManifest) error
}

type Pipeline struct {
	cfg                   *config.Config
	reg                   *agent.Registry
	notifier              notifier.Notifier
	prompter              Prompter
	newRuntime            runtime.Factory
	recorder              Recorder
	delivery              delivery.Service
	approvals             ApprovalStore
	lifecycle             lifecycle.StorePort
	evidence              EvidenceStoreFactory
	briefs                BriefStore
	candidateMetadata     candidate.MetadataStore
	usageEnvelopeWriter   UsageEnvelopeWriter
	terminalRecordWriter  TerminalRecordWriter
	attestationWriter     AttestationWriter
	containmentWriter     ContainmentReceiptWriter
	candidateEvidence     CandidateEvidenceStore
	attemptManifestSource evidence.AttemptManifestSource
	attemptManifestWriter AttemptManifestWriter
	eventLogSource        evidence.EventLog
	deliveryApprovalHash  string
	questionAnswerInputs  QuestionAnswerInputProvider
	reportsDir            string
}

type Option func(*Pipeline)

func WithNotifier(n notifier.Notifier) Option {
	return func(p *Pipeline) { p.notifier = n }
}

func WithReportsDir(dir string) Option {
	return func(p *Pipeline) { p.reportsDir = dir }
}

// WithDeliveryApprovalHash carries a plan hash explicitly confirmed by the
// current controller invocation into deferred delivery and recovery checks.
func WithDeliveryApprovalHash(hash string) Option {
	return func(p *Pipeline) { p.deliveryApprovalHash = strings.ToLower(strings.TrimSpace(hash)) }
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

// WithApprovalStore injects an ApprovalStore port. Local CLI runs default to
// the filesystem-backed approval.Store; web mode injects the SQLite store.
// This seam does not isolate worker access to the shared database or move
// lifecycle/evidence persistence out of the target.
func WithApprovalStore(store ApprovalStore) Option {
	return func(p *Pipeline) { p.approvals = store }
}

// WithLifecycleStore injects the mutable checkpoint port. The filesystem
// lifecycle store remains the default for local CLI runs.
func WithLifecycleStore(store lifecycle.StorePort) Option {
	return func(p *Pipeline) { p.lifecycle = store }
}

// WithEvidenceStoreFactory injects the run evidence persistence port. The
// filesystem-backed evidence store remains the default. This seam alone does
// not move evidence or artifacts out of the target filesystem or isolate a
// worker from controller-owned state.
func WithEvidenceStoreFactory(factory EvidenceStoreFactory) Option {
	return func(p *Pipeline) { p.evidence = factory }
}

// WithEventLogSource injects the scoped authority used for lifecycle journal
// reads and appends. Local CLI pipelines keep the run-local file implementation.
func WithEventLogSource(source evidence.EventLog) Option {
	return func(p *Pipeline) { p.eventLogSource = source }
}

// WithBusinessBriefStore routes durable business-brief persistence through a
// controller-owned typed store. The default remains the local filesystem store.
func WithBusinessBriefStore(store BriefStore) Option {
	return func(p *Pipeline) { p.briefs = store }
}

// WithCandidateMetadataStore routes candidate identity persistence through the
// controller API. Local CLI runs keep the candidate package file-store default.
func WithCandidateMetadataStore(store candidate.MetadataStore) Option {
	return func(p *Pipeline) { p.candidateMetadata = store }
}

// WithUsageEnvelopeWriter routes cloud usage summaries through the controller.
func WithUsageEnvelopeWriter(writer UsageEnvelopeWriter) Option {
	return func(p *Pipeline) { p.usageEnvelopeWriter = writer }
}

func WithTerminalRecordWriter(writer TerminalRecordWriter) Option {
	return func(p *Pipeline) { p.terminalRecordWriter = writer }
}

func WithAttestationWriter(writer AttestationWriter) Option {
	return func(p *Pipeline) { p.attestationWriter = writer }
}

func WithContainmentReceiptWriter(writer ContainmentReceiptWriter) Option {
	return func(p *Pipeline) { p.containmentWriter = writer }
}

func WithCandidateEvidenceStore(store CandidateEvidenceStore) Option {
	return func(p *Pipeline) { p.candidateEvidence = store }
}

// WithQuestionAnswerInputProvider routes resolved clarification answers
// through a typed controller API. Local CLI pipelines retain the filesystem
// materialization path.
func WithQuestionAnswerInputProvider(provider QuestionAnswerInputProvider) Option {
	return func(p *Pipeline) { p.questionAnswerInputs = provider }
}

// WithAttemptManifestStore routes canonical manifest reads and writes through
// a controller-owned source while the legacy snapshots remain in the run tree.
func WithAttemptManifestStore(source evidence.AttemptManifestSource, writer AttemptManifestWriter) Option {
	return func(p *Pipeline) {
		p.attemptManifestSource = source
		p.attemptManifestWriter = writer
	}
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
	if p.evidence == nil {
		p.evidence = filesystemEvidenceStoreFactory{}
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
	// CancelRequested is polled only between stage attempts so cancellation
	// never interrupts an agent while it is mutating its workspace.
	CancelRequested func() bool
	// ContainmentProfile — containment profile run (trusted-local | strict).
	// Пустое значение → trusted-local. Влияет на containment receipt в evidence.
	ContainmentProfile string
}

type RunResult struct {
	RunID      string
	QueueJobID int64
	Outcome    workflow.RunOutcome
}

func (p *Pipeline) Agents() []string {
	return p.cfg.AgentNames()
}

// runState — состояние одного запуска (Pipeline не мутируется, можно
// переиспользовать для нескольких запусков).
type runState struct {
	p                         *Pipeline
	runCfg                    RunConfig
	task                      *runtime.Task
	reportsDir                string
	names                     []string
	results                   []notifier.StageResult
	extraInputs               map[string][]runtime.Artifact // loopback: выходы вердикт-агента → входы цели
	ps                        *ui.PipelineStatus
	startTime                 time.Time
	approvedPlanHash          string
	approvePlanExplicit       bool
	runID                     string
	evidence                  EvidenceStore
	attemptOrdinal            int
	userOwnedPaths            map[string]bool
	lifecycleStore            lifecycle.StorePort
	lifecycleState            lifecycle.State
	approvalStore             ApprovalStore
	resumedApproval           *approval.PendingApproval
	selectedArtifactRevisions map[string]string
	resumed                   bool
	brief                     briefVersion
	graph                     workflow.Graph
	visits                    map[string]int
	candidate                 *candidate.Manager
	sourceTarget              string
	liveWorkspaceSHA          string
	loopbackCycles            int
	deferredDelivery          *deferredDelivery
	budgetConfig              *config.BudgetConfig
	usageTotal                runtime.Usage
	usageUnknown              bool
	attestationDigest         string
}

// deferredDelivery (V0-9) — подготовленный canonical plan, чей commit/push/PR
// отложен до terminal finalize, когда attestation digest уже детерминирован.
// Инициализируется в delivery-стадии после успешной авторизации плана; реальный
// git-commit создаёт контроллер после finalize (см. executeDeferredDelivery).
type deferredDelivery struct {
	StatePath string
	PlanHash  string
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
	approvePlanExplicit := strings.TrimSpace(runCfg.ApprovePlanHash) != ""
	if err := p.cfg.Validate(p.reg); err != nil {
		return RunResult{}, err
	}
	// AUD-02 fail-closed: strict-контракт не реализован, поэтому запрос
	// strict-профиля блокируется ДО первого обращения к runtime/checks —
	// единая точка входа (Start и Resume идут через RunWithResult). Никакого
	// misleading receipt в evidence не создаётся.
	if effectiveContainmentProfile(runCfg, p.cfg) == "strict" {
		return RunResult{}, &RunError{
			Outcome: workflow.RunBlocked,
			Err:     fmt.Errorf("strict containment профиль недоступен: run отклонён fail-closed (AUD-02); используйте trusted-local"),
		}
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

	lifecycleStore := p.lifecycle
	if lifecycleStore == nil {
		lifecycleStore, err = lifecycle.NewStore(runCfg.TargetDir)
		if err != nil {
			return RunResult{}, fmt.Errorf("lifecycle store: %w", err)
		}
	}
	approvalStore := p.approvals
	if approvalStore == nil {
		approvalStore, err = approval.NewStore(runCfg.TargetDir)
		if err != nil {
			return RunResult{}, fmt.Errorf("approval store: %w", err)
		}
	}
	var resumedState lifecycle.State
	var resumedApproval *approval.PendingApproval
	var recoveredClarification *approval.PendingApproval
	var recoveredGraphApproval *approval.PendingApproval
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
			if value.Trigger == "delivery_plan" && value.ResolvedAction == "approve" {
				// A resolved JSON file is not an approval authority: it can be
				// edited outside this process. Continue only when this invocation
				// explicitly reasserted the exact plan hash or when the decision
				// came from a trusted controller decision store/API.
				if runCfg.ApprovePlanHash != "" {
					if normalized := strings.ToLower(strings.TrimSpace(runCfg.ApprovePlanHash)); normalized != value.SubjectHash {
						return RunResult{}, fmt.Errorf("resume run: --approve-plan %s не совпадает с subject approval %s", normalized, value.SubjectHash)
					}
				} else if trustedStore, trusted := approvalStore.(approval.TrustedDecisionAuthority); !trusted || !trustedStore.HasAuthenticatedControllerDecision(value) {
					return RunResult{}, &ApprovalRequiredError{
						Checkpoint: "delivery требует явного --approve-plan в текущем процессе",
						RunID:      value.RunID, ApprovalID: value.ID, SubjectHash: value.SubjectHash,
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
				if trustedStore, trusted := approvalStore.(approval.TrustedDecisionAuthority); trusted &&
					trustedStore.HasAuthenticatedControllerDecision(value) && !approvePlanExplicit {
					resumedApproval = &value
				} else {
					// Local approvals are not authority after process restart. The
					// explicit hash above authorizes a fresh approval for this attempt.
					resumedApproval = nil
				}
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
	taskCreatedAt := runStartedAt
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
		candidateManager, err = loadResumeCandidate(ctx, runCfg.TargetDir, runID, p.candidateMetadata)
		if err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resume candidate: %w", err)
		}
	} else {
		var gitAvailable bool
		if p.candidateMetadata != nil {
			candidateManager, gitAvailable, err = candidate.CreateWithMetadataStore(ctx, runCfg.TargetDir, runID, p.candidateMetadata)
		} else {
			candidateManager, gitAvailable, err = candidate.Create(ctx, runCfg.TargetDir, runID)
		}
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
	var evidenceStore EvidenceStore
	var replayedRun evidence.ReplayedRun
	var resumeInvalidated []string
	var resumeMutations []string
	attemptOrdinal := 0
	if runCfg.ResumeRunID != "" {
		var manifest evidence.RunManifest
		runEvidenceDir := filepath.Join(runCfg.TargetDir, ".ai-team", "runs", runID)
		// OPS-3: fail-closed проверка применимой evidence chain/snapshots перед
		// продолжением. Если цепочка/снимки повреждены — отклоняем resume и явно
		// фиксируем причину в evidence (resume_blocked), пока лог аппендабелен.
		verifyErr := evidence.VerifyResumeEvidenceWithSources(runEvidenceDir, p.attemptManifestSource, p.eventLogSource)
		if verifyErr != nil {
			recErr := &evidence.ResumeEvidenceError{}
			if errors.As(verifyErr, &recErr) && recErr.Reason != evidence.ReasonEventChain &&
				recErr.Reason != evidence.ReasonManifestIdentity &&
				recErr.Reason != evidence.ReasonAlreadyTerminal {
				_ = evidence.AppendBlockedEventWithSources(runEvidenceDir, runID, recErr.Reason, recErr.Detail, p.eventLogSource, p.attemptManifestSource)
			}
			return RunResult{}, fmt.Errorf("resume evidence run: %w", verifyErr)
		}
		evidenceStore, manifest, replayedRun, err = p.resumeEvidence(filepath.Join(runCfg.TargetDir, ".ai-team", "runs"), runID)
		if err != nil {
			return RunResult{}, fmt.Errorf("resume evidence run: %w", err)
		}
		taskCreatedAt = manifest.StartedAt
		if taskCreatedAt.IsZero() || taskCreatedAt.After(runStartedAt) {
			return RunResult{}, fmt.Errorf("resume evidence run: invalid task creation time")
		}
		if resumedState.Phase == lifecycle.PhaseRunning || resumedState.Phase == lifecycle.PhaseResumable {
			recoveredGraphApproval, err = recoveredGraphInputApproval(approvalStore, runID, resumedState.NextStage, compiledGraph, replayedRun)
			if err != nil {
				return RunResult{}, fmt.Errorf("resume graph handoff input: %w", err)
			}
			if recoveredGraphApproval == nil {
				// A resolved graph return into the current stage is newer than an
				// older clarification targeting that same stage. Only reconcile a
				// stale checkpoint after checking for that exact handoff first.
				var reconciledTo string
				var reconciled bool
				reconciledTo, reconciled, err = ReconcileResumeNextStage(resumedState.NextStage, compiledGraph, replayedRun)
				if err != nil {
					return RunResult{}, fmt.Errorf("resume graph checkpoint: %w", err)
				}
				if reconciled {
					runCfg.retryFrom = reconciledTo
				}
				recoveredClarification, err = RecoveredQuestionApproval(approvalStore, runID, runCfg.retryFrom, replayedRun)
				if err != nil {
					return RunResult{}, fmt.Errorf("resume clarification input: %w", err)
				}
				if recoveredClarification == nil && runCfg.retryFrom != "analyst" {
					recoveredClarification, err = recoveredQuestionApproval(approvalStore, runID, runCfg.retryFrom)
					if err != nil {
						return RunResult{}, fmt.Errorf("resume clarification input: %w", err)
					}
				}
			}
		}
		if resumedApproval != nil && resumedApproval.ResolvedAction == "answer_questions" {
			if err := ValidateQuestionAnswerApproval(*resumedApproval, replayedRun); err != nil {
				return RunResult{}, fmt.Errorf("resume clarification approval: %w", err)
			}
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
		if len(manifest.Provenance) > 0 {
			var storedProvenance provenance.Manifest
			if json.Unmarshal(manifest.Provenance, &storedProvenance) != nil {
				return RunResult{}, fmt.Errorf("resume provenance manifest повреждён")
			}
			liveIdentity, liveErr := evidence.CurrentControllerIdentity()
			if liveErr != nil {
				return RunResult{}, fmt.Errorf("resume controller identity: %w", liveErr)
			}
			liveProvenance, provErr := p.captureProvenance(runID, liveIdentity, candidateManager)
			if provErr != nil {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed}, provErr
			}
			if err := provenance.CheckDrift(&storedProvenance, liveProvenance); err != nil {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed},
					fmt.Errorf("resume run: %w", err)
			}
		}
		for _, attempt := range replayedRun.Attempts {
			if attempt.ManifestSHA256 != "" {
				_, attemptManifest, readErr := evidence.ReadAttemptManifest(p.attemptManifestSource, evidenceStore.RunDir(), runID, attempt.AttemptID)
				if readErr != nil {
					return RunResult{}, readErr
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
				Timestamp: approvalDecisionTimestamp(*resumedApproval), Data: approvalEventData(*resumedApproval),
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
					invalidate := targetIndex >= 0 && attempt.StageIndex > targetIndex+1
					if resumedApproval.Kind == approval.KindQuestions && attempt.AttemptID == resumedApproval.AttemptID {
						invalidate = true
					}
					if invalidate && !attempt.Superseded {
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
		savedState, err := saveLifecycleCheckpoint(lifecycleStore, resumedState, nextState)
		if err != nil {
			return RunResult{}, err
		}
		resumedState = savedState
	} else {
		identity, identityErr := evidence.CurrentControllerIdentity()
		if identityErr != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed},
				fmt.Errorf("controller identity: %w", identityErr)
		}
		provenanceManifest, provErr := p.captureProvenance(runID, identity, candidateManager)
		if provErr != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, provErr
		}
		provenanceData, marshalErr := json.Marshal(provenanceManifest)
		if marshalErr != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("provenance manifest: %w", marshalErr)
		}
		evidenceStore, err = p.startEvidence(filepath.Join(runCfg.TargetDir, ".ai-team", "runs"), evidence.RunManifest{
			RunID: runID, Feature: runCfg.Feature, TargetDir: runCfg.TargetDir, StartedAt: runStartedAt,
			ConfigSnapshot: configSnapshot, WorkflowSnapshot: workflowSnapshot, Provenance: provenanceData,
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
		resumedState, err = lifecycleStore.Load(runID)
		if err != nil {
			return RunResult{}, fmt.Errorf("чтение созданного lifecycle state: %w", err)
		}
	}
	briefStore := p.briefs
	if briefStore == nil {
		briefStore = NewFileBriefStore(runCfg.TargetDir)
	}
	artifactRoot, err := safeio.EnsureDir(sourceTarget, ".ai-team", "artifacts")
	if err != nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("prepare private brief workspace: %w", err)
	}
	briefWorkspace, err := os.MkdirTemp(artifactRoot, ".brief-"+runID+"-")
	if err != nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("create private brief workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(briefWorkspace) }()
	var currentBrief briefVersion
	if runCfg.ResumeRunID == "" {
		var document BriefDocument
		document, err = briefStore.CreateInitial(runID, runCfg.TaskDesc)
		if err == nil {
			currentBrief, err = materializeBriefDocument(briefWorkspace, document)
		}
	} else {
		versions, briefErr := briefStore.List(runID)
		if os.IsNotExist(briefErr) || (briefErr == nil && len(versions) == 0) {
			// Runs created before business-brief versioning can resume from their
			// immutable lifecycle task snapshot; new runs always write version 1.
			var document BriefDocument
			document, err = briefStore.CreateInitial(runID, resumedState.Task)
			if err == nil {
				currentBrief, err = materializeBriefDocument(briefWorkspace, document)
			}
		} else if briefErr != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resume run: durable business brief unavailable: %w", briefErr)
		} else {
			document, readErr := briefStore.Read(runID, versions[len(versions)-1].ID)
			if readErr != nil {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resume run: durable business brief unavailable: %w", readErr)
			}
			currentBrief, err = materializeBriefDocument(briefWorkspace, document)
		}
		answerApproval := resumedApproval
		if answerApproval == nil {
			answerApproval = recoveredClarification
		}
		if answerApproval != nil && answerApproval.Kind == approval.KindQuestions && answerApproval.ResolvedAction == "answer_questions" {
			var payload questionPayload
			if json.Unmarshal(answerApproval.Payload, &payload) != nil || payload.Kind != "questions" {
				return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resume run: invalid clarification payload")
			}
			var document BriefDocument
			document, err = briefStore.AppendClarification(runID, answerApproval.ID, payload.Markdown, questionAnswer(answerApproval.Decisions))
			if err == nil {
				currentBrief, err = materializeBriefDocument(briefWorkspace, document)
			}
		}
	}
	if err != nil {
		return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("durable business brief: %w", err)
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
			// New runs replace this projection. Resumes keep it because earlier
			// attempt pages are still linked from the final report.
			if runCfg.ResumeRunID == "" {
				if err := os.RemoveAll(featureReports); err != nil {
					return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("clear live report projection: %w", err)
				}
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
		reportsDir:  reportsDir,
		names:       p.cfg.AgentNames(),
		extraInputs: make(map[string][]runtime.Artifact),
		selectedArtifactRevisions: func() map[string]string {
			selectedApproval := resumedApproval
			if selectedApproval == nil {
				selectedApproval = recoveredGraphApproval
			}
			if selectedApproval == nil || len(selectedApproval.ArtifactRevisions) == 0 {
				return nil
			}
			copy := make(map[string]string, len(selectedApproval.ArtifactRevisions))
			for path, id := range selectedApproval.ArtifactRevisions {
				copy[path] = id
			}
			return copy
		}(),
		startTime:           taskCreatedAt,
		approvedPlanHash:    runCfg.ApprovePlanHash,
		approvePlanExplicit: approvePlanExplicit,
		runID:               runID,
		evidence:            evidenceStore,
		attemptOrdinal:      attemptOrdinal,
		lifecycleStore:      lifecycleStore,
		lifecycleState:      resumedState,
		approvalStore:       approvalStore,
		resumedApproval:     resumedApproval,
		resumed:             runCfg.ResumeRunID != "",
		brief:               currentBrief,
		graph:               compiledGraph,
		visits:              make(map[string]int),
		candidate:           candidateManager,
		sourceTarget:        sourceTarget,
		budgetConfig:        p.cfg.Budget,
	}
	if runCfg.ResumeRunID != "" {
		var usage runtime.Usage
		rs.results, usage, rs.usageUnknown, err = replayedStageResults(replayedRun, evidenceStore.RunDir(), p.attemptManifestSource, p.reg, len(compiledGraph.Nodes))
		if err != nil {
			return RunResult{RunID: runID, Outcome: workflow.RunFailed}, fmt.Errorf("resume attempt manifests: %w", err)
		}
		rs.usageTotal = usage
		// Preserve historical attempt pages across pauses and rebuild them from
		// the immutable manifest, so report links remain populated after resume.
		for _, previous := range rs.results {
			if previous.FinishedAt.IsZero() {
				continue
			}
			if reportErr := report.GenerateStageReport(rs.reportsDir, runCfg.Feature, previous.AttemptID, previous, rs.task.ArtifactRoot); reportErr != nil {
				logging.Printf("warning: restore report for attempt %s: %v", previous.AttemptID, reportErr)
			}
		}
	}
	if len(resumeInvalidated) > 0 {
		rs.loopbackCycles = 1
	}
	for _, previous := range rs.results {
		rs.visits[previous.Name]++
	}
	rs.ps = ui.NewPipelineStatus(filepath.Base(runCfg.TargetDir), runCfg.Feature, len(rs.names))
	rs.task.ConsoleOut = rs.ps.StatusWriter()
	if p.recorder != nil {
		snapshot, _ := yaml.Marshal(p.cfg)
		p.recorder.ReconcileInterrupted(runStartedAt)
		if rs.resumed {
			if resumedApproval != nil {
				// Attach first so the recorder can append the decision at its
				// actual timestamp, then append the later resume event. Event
				// sequence and timestamps must describe the same chronology.
				p.recorder.RunAttached(runID)
				p.recorder.ApprovalDecided(runID, resumedApproval.ID, resumedApproval.AttemptID,
					approvalDecisionTimestamp(*resumedApproval), approvalEventData(*resumedApproval))
			}
			p.recorder.RunResumed(runID, runStartedAt)
			if resumedApproval != nil {
				if resumedTransitionData != nil {
					p.recorder.TransitionSelected(runID, resumedApproval.AttemptID, runStartedAt, resumedTransitionData)
				}
			}
			if len(resumeInvalidated) > 0 {
				p.recorder.AttemptsInvalidated(runID, resumeInvalidated, runStartedAt)
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
	inputApproval := resumedApproval
	if inputApproval == nil {
		inputApproval = recoveredGraphApproval
	}
	if inputApproval == nil {
		inputApproval = recoveredClarification
	}
	if inputApproval == nil && !rs.resumed {
		rs.extraInputs[rs.graph.Entry] = briefInputs(rs.brief)
	}
	if inputApproval != nil && (inputApproval.Kind == approval.KindQuestions || isApprovedSpecPayload(inputApproval.Payload) || isBackwardTransition(rs.graph, inputApproval)) {
		inputs, inputErr := rs.stageOutputs(inputApproval.FromStage, inputApproval.AttemptID)
		if inputErr != nil {
			outcome, finalErr := rs.finalize(inputErr)
			return RunResult{RunID: runID, Outcome: outcome}, finalErr
		}
		if inputApproval.Kind == approval.KindQuestions && inputApproval.ResolvedAction == "answer_questions" {
			filtered := make([]runtime.Artifact, 0, len(inputs)+1)
			for _, input := range inputs {
				if input.Name == "questions" {
					filtered = append(filtered, input)
				}
			}
			var answer runtime.Artifact
			var answerErr error
			if p.questionAnswerInputs != nil {
				answer, answerErr = p.questionAnswerInputs.MaterializeQuestionAnswer(runID, inputApproval.ID)
			} else {
				answer, answerErr = writeQuestionAnswerInput(runCfg.TargetDir, runID, inputApproval.ID, questionAnswer(inputApproval.Decisions))
			}
			if answerErr != nil {
				outcome, finalErr := rs.finalize(answerErr)
				return RunResult{RunID: runID, Outcome: outcome}, finalErr
			}
			filtered = append(filtered, answer)
			filtered = append(filtered, briefInputs(rs.brief)...)
			rs.extraInputs[runCfg.retryFrom] = filtered
		} else if isApprovedSpecPayload(inputApproval.Payload) {
			var payload approvedSpecPayload
			if err := json.Unmarshal(inputApproval.Payload, &payload); err != nil || payload.Kind != "agreed_spec" ||
				payload.BriefVersion.ID != rs.brief.ID || payload.BriefVersion.SHA256 != rs.brief.SHA256 {
				outcome, finalErr := rs.finalize(errors.New("approved specification is not bound to the current business brief version"))
				return RunResult{RunID: runID, Outcome: outcome}, finalErr
			}
			byName := make(map[string]runtime.Artifact, len(inputs))
			for _, input := range inputs {
				byName[input.Name] = input
			}
			for _, name := range []string{"proposal", "spec"} {
				input, ok := byName[name]
				if !ok {
					outcome, finalErr := rs.finalize(fmt.Errorf("approved stage artifact %s missing", name))
					return RunResult{RunID: runID, Outcome: outcome}, finalErr
				}
				_, _, digest, digestErr := evidence.ArtifactDigest(input.Path)
				if digestErr != nil || payload.Artifacts[name] != digest {
					outcome, finalErr := rs.finalize(fmt.Errorf("approved stage artifact %s digest mismatch", name))
					return RunResult{RunID: runID, Outcome: outcome}, finalErr
				}
				input, inputErr = selectedHumanRevision(runCfg.TargetDir, runID, input, inputApproval.ArtifactRevisions)
				if inputErr != nil {
					outcome, finalErr := rs.finalize(inputErr)
					return RunResult{RunID: runID, Outcome: outcome}, finalErr
				}
				input.Name = "approved-" + name
				rs.extraInputs[runCfg.retryFrom] = append(rs.extraInputs[runCfg.retryFrom], input)
			}
			rs.extraInputs[runCfg.retryFrom] = append(rs.extraInputs[runCfg.retryFrom], briefInputs(rs.brief)...)
		} else {
			for index := range inputs {
				inputs[index], inputErr = selectedHumanRevision(runCfg.TargetDir, runID, inputs[index], inputApproval.ArtifactRevisions)
				if inputErr != nil {
					outcome, finalErr := rs.finalize(inputErr)
					return RunResult{RunID: runID, Outcome: outcome}, finalErr
				}
			}
			rs.extraInputs[runCfg.retryFrom] = inputs
			if isBackwardTransition(rs.graph, inputApproval) {
				feedback, feedbackErr := writeReturnFeedback(runCfg.TargetDir, *inputApproval)
				if feedbackErr != nil {
					outcome, finalErr := rs.finalize(feedbackErr)
					return RunResult{RunID: runID, Outcome: outcome}, finalErr
				}
				rs.extraInputs[runCfg.retryFrom] = append(rs.extraInputs[runCfg.retryFrom], feedback)
			}
		}
	}

	// Wall-time бюджет применяется к этому вызову RunWithResult. Пауза
	// завершает вызов; последующий resume получает отдельный полный бюджет.
	// Длительность задачи независимо считается от taskCreatedAt.
	budgetDur, budgetStr := rs.budgetConfig.EffectiveMaxExecutionTime()
	budgetCtx, cancel := context.WithTimeout(ctx, budgetDur)
	defer cancel()
	runErr := rs.execute(budgetCtx)
	// Здесь матчится ТОЛЬКО подлинное превышение budget: per-stage таймауты
	// возвращают ErrStageTimeout (не wrapping context.DeadlineExceeded) и
	// до этой ветки не доходят — остаются resumable, а не терминал-бюджет.
	// ctx.Err() == nil отделяет наш дедлайн от дедлайна вызывающего: чужой
	// таймаут не должен рапортоваться как превышение max_execution_time.
	if errors.Is(runErr, context.DeadlineExceeded) && ctx.Err() == nil {
		runErr = fmt.Errorf("run budget: превышен max_execution_time %s", budgetStr)
	}
	outcome, finalErr := rs.finalize(runErr)
	if finalErr != nil {
		return RunResult{RunID: runID, Outcome: outcome}, finalErr
	}
	// V0-9: отложенная (deferred) доставка — только после terminal finalize и
	// только для полностью completed-ранна (включая completed_with_warnings:
	// AUD-06 — warning не меняет delivery-контракт, пользователь ждёт
	// commit/push/PR), когда attestation digest и runtime identity
	// детерминированы. Enforcement: план перегружается из prepared state и
	// должен совпасть с approvedPlanHash и маркером стадии.
	switch outcome {
	case workflow.RunCompleted, workflow.RunCompletedWithWarnings:
		if rs.deferredDelivery != nil {
			// QS-06: ctx, а не budgetCtx — доставка не отменяется исчерпанным
			// бюджетом run'а, но остаётся отменяемой сигналом процессу.
			if deferredErr := rs.executeDeferredDelivery(ctx); deferredErr != nil {
				return RunResult{RunID: runID, Outcome: outcome}, deferredErr
			}
		}
	}
	return RunResult{RunID: runID, Outcome: outcome}, finalErr
}

func (p *Pipeline) recoverInitialLifecycle(runID, targetDir, feature, task string) error {
	if runID == "" || !workflow.ValidFeature(feature) || strings.TrimSpace(task) == "" {
		return errors.New("recover initial lifecycle: run_id, feature и task обязательны")
	}
	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	targetDir, err = filepath.EvalSymlinks(filepath.Clean(targetDir))
	if err != nil {
		return err
	}
	store := p.lifecycle
	if store == nil {
		store, err = lifecycle.NewStore(targetDir)
		if err != nil {
			return err
		}
	}
	if _, err := store.Load(runID); err == nil {
		return errors.New("recover initial lifecycle: lifecycle state already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	runRoot := filepath.Join(targetDir, ".ai-team", "runs")
	evidenceStore, manifest, replayed, err := p.resumeEvidence(runRoot, runID)
	if err != nil {
		return fmt.Errorf("recover initial lifecycle: verify evidence: %w", err)
	}
	if manifest.Feature != feature || manifest.TargetDir != targetDir || replayed.RunID != runID ||
		len(replayed.Attempts) != 0 || !replayed.FinishedAt.IsZero() {
		return errors.New("recover initial lifecycle: evidence identity/state mismatch")
	}
	events, err := evidence.VerifyEventLogWithSource(filepath.Join(runRoot, runID, "events.jsonl"), runID, p.eventLogSource)
	if err != nil || (len(events) != 0 && (len(events) != 1 || events[0].Type != "run_started")) {
		return errors.New("recover initial lifecycle: evidence is not the initial run_started checkpoint")
	}
	graph, err := p.cfg.CompiledGraph()
	if err != nil {
		return err
	}
	configSnapshot, workflowSnapshot, err := p.resolvedEvidenceSnapshots()
	if err != nil {
		return err
	}
	configDigest := sha256.Sum256(configSnapshot)
	workflowDigest := sha256.Sum256(workflowSnapshot)
	if fmt.Sprintf("%x", configDigest[:]) != manifest.ConfigSHA256 ||
		fmt.Sprintf("%x", workflowDigest[:]) != manifest.ResolvedWorkflowSHA256 {
		return errors.New("recover initial lifecycle: current config/workflow differs from run evidence")
	}
	// The task artifact was durably published before candidate/evidence setup.
	// Read it through the regular resume path and compare to the immutable queue
	// admission payload before restoring task identity into lifecycle state.
	initial, err := prepareTaskArtifact(RunConfig{
		TargetDir: targetDir, Feature: feature, retryFrom: graph.Entry,
	})
	if err != nil {
		return err
	}
	if initial != task {
		return errors.New("recover initial lifecycle: admitted task differs from durable task artifact")
	}
	if len(events) == 0 {
		if err := evidenceStore.Append(evidence.Event{Type: "run_started", Timestamp: manifest.StartedAt}); err != nil {
			return fmt.Errorf("recover initial lifecycle: restore run_started: %w", err)
		}
	}
	return store.Create(lifecycle.State{
		RunID: runID, Feature: feature, TargetDir: targetDir, Task: task,
		Phase: lifecycle.PhaseRunning, NextStage: graph.Entry,
		ConfigSHA256: manifest.ConfigSHA256, WorkflowSHA256: manifest.ResolvedWorkflowSHA256,
		CreatedAt: manifest.StartedAt,
	})
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
	defer func() { _ = os.Remove(temporaryPath) }() // temp-файл удаляется, только если rename не состоялся; ENOENT после успеха — норма.
	if err := temporary.Chmod(0644); err != nil {
		// аварийный путь: значимая ошибка уже возвращается, Close только освобождает дескриптор.
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
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
