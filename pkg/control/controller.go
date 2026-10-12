// Package control связывает долговечный RunEngine с process-local workers.
package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/preflight"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// humanInputTrigger mirrors pipeline.humanInputTrigger: the approval trigger
// used for stage-bound human input decisions.
const humanInputTrigger = "human_input"

var ErrActive = errors.New("run уже исполняется")

type worker struct {
	cancel          context.CancelFunc
	cancelRequested chan struct{}
}

type runEngine interface {
	Start(context.Context, pipeline.RunConfig) (pipeline.RunResult, error)
	Resume(context.Context, pipeline.ResumeConfig) (pipeline.RunResult, error)
	Cancel(pipeline.CancelConfig) (pipeline.RunResult, error)
}

// skipStageEngine is implemented by pipeline.RunEngine. Controllers built on
// lighter engines keep the REST skip action returning controller_action_unavailable.
type skipStageEngine interface {
	SkipStage(context.Context, pipeline.SkipStageConfig) (pipeline.RunResult, error)
}

type PreflightChecker interface {
	Check(context.Context) preflight.Report
}

type Option func(*Controller)

type DeferredDeliveryFunc func(context.Context, string, string) (delivery.TerminalRecord, error)

func WithPreflight(checker PreflightChecker) Option {
	return func(controller *Controller) { controller.preflight = checker }
}

// WithApprovalStore uses the same approval persistence as the worker pipeline.
// In web mode this lets authenticated controller decisions survive process
// restarts and be observed by scheduler workers.
func WithApprovalStore(store pipeline.ApprovalStore) Option {
	return func(controller *Controller) {
		if store != nil {
			controller.approvals = store
		}
	}
}

// WithDeferredDelivery installs the trusted retry path for terminal delivery.
// The run engine can resolve the immutable task template before selecting its
// delivery timeout; scheduler and process engines do not own that project config.
func WithDeferredDelivery(deliver DeferredDeliveryFunc) Option {
	return func(controller *Controller) {
		if deliver != nil {
			controller.deferredDelivery = deliver
		}
	}
}

type Controller struct {
	engine           runEngine
	target           string
	approvals        pipeline.ApprovalStore
	deferredDelivery DeferredDeliveryFunc
	preflight        PreflightChecker
	failureSink      FailureSink
	admissionSink    func(string, int64) error

	mu     sync.Mutex
	active map[string]*worker
}

func New(engine runEngine, target string, options ...Option) (*Controller, error) {
	if engine == nil {
		return nil, errors.New("run engine обязателен")
	}
	controller := &Controller{
		engine: engine, target: target,
		active: make(map[string]*worker),
	}
	for _, option := range options {
		option(controller)
	}
	if controller.approvals == nil {
		store, err := approval.NewStore(target)
		if err != nil {
			return nil, err
		}
		controller.approvals = store
	}
	return controller, nil
}

func (c *Controller) Start(feature, task string) (string, error) {
	return c.start(feature, task, "", nil)
}

// StartWithAdmission persists the caller's visible admission record before a
// worker can enqueue or execute the run. A failed admission therefore never
// leaves a scheduler job behind a 503 response.
func (c *Controller) StartWithAdmission(feature, task string, admit func(runID string) error) (string, error) {
	if admit == nil {
		return "", errors.New("admission callback обязателен")
	}
	return c.start(feature, task, "", admit)
}

// runModeEngine is implemented by engines that own run-mode pinning (currently
// pipeline.RunEngine). Controllers built on lighter engines keep auto mode
// failing closed instead of silently running manual mode.
type runModeEngine interface {
	SupportsRunMode(mode string) bool
}

// StartTask admits a browser-created task with its metadata: the idea text
// becomes the run description consumed by the intent stage brief, and auto
// mode is handed to the engine only when it owns run-mode pinning.
func (c *Controller) StartTask(_ context.Context, feature, task, _, idea, mode string, _ []string, admit func(runID string) error) (string, error) {
	if mode != "" && mode != "manual" && mode != "auto" {
		return "", fmt.Errorf("unsupported run mode %q", mode)
	}
	description := strings.TrimSpace(idea)
	if description == "" {
		description = task
	}
	runMode := mode
	if runMode == "manual" {
		runMode = ""
	}
	if runMode == "auto" {
		engine, ok := c.engine.(runModeEngine)
		if !ok || !engine.SupportsRunMode("auto") {
			return "", errors.New("auto mode не поддерживается этим режимом запуска")
		}
	}
	if admit == nil {
		return "", errors.New("admission callback обязателен")
	}
	return c.start(feature, description, runMode, admit)
}

func (c *Controller) start(feature, task, mode string, admit func(runID string) error) (string, error) {
	if !workflow.ValidFeature(feature) || strings.TrimSpace(task) == "" {
		return "", errors.New("feature и непустой task обязательны")
	}
	if c.preflight != nil {
		if report := c.Preflight(context.Background()); !report.Ready && !report.Unknown {
			return "", report.Error()
		}
	}
	// Синхронное резервирование target до ответа 202: без захваченного
	// lock второй start мог бы получить 202, а затем молча исчезнуть.
	lock, err := evidence.AcquireWorkspaceLock(c.target)
	if err != nil {
		return "", fmt.Errorf("target занят другим run: %w", err)
	}
	runID, err := evidence.NewRunID(time.Now().UTC())
	if err != nil {
		_ = lock.Close()
		return "", err
	}
	if admit != nil {
		if err := admit(runID); err != nil {
			_ = lock.Close()
			return "", fmt.Errorf("run admission: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &worker{cancel: cancel, cancelRequested: make(chan struct{})}
	c.mu.Lock()
	c.active[runID] = active
	c.mu.Unlock()
	go c.run(runID, ctx, lock, func(ctx context.Context) (pipeline.RunResult, error) {
		return c.engine.Start(ctx, pipeline.RunConfig{
			RunID: runID, Feature: feature, TaskDesc: task, TargetDir: c.target, Mode: mode,
			WorkspaceLock: lock, CancelRequested: func() bool { return cancellationRequested(c.target, runID, active.cancelRequested) },
		})
	})
	return runID, nil
}

func (c *Controller) Preflight(ctx context.Context) preflight.Report {
	if c.preflight == nil {
		// Enqueue-only режим: готовность runtime известна только воркерам.
		return preflight.Report{Unknown: true, CheckedAt: time.Now().UTC()}
	}
	return c.preflight.Check(ctx)
}

func (c *Controller) Resume(runID string) error {
	stateStore, err := lifecycle.NewStore(c.target)
	if err != nil {
		return err
	}
	state, err := stateStore.Load(runID)
	if err != nil {
		return err
	}
	if state.Phase == lifecycle.PhaseTerminal {
		return fmt.Errorf("run %s уже terminal", runID)
	}
	if state.Phase == lifecycle.PhaseWaiting {
		value, err := c.approvals.Load(runID, state.PendingApprovalID)
		if err != nil {
			return err
		}
		if value.Status != approval.StatusResolved {
			return errors.New("run всё ещё ожидает human decision")
		}
	}
	return c.launch(runID, func(ctx context.Context, cancelRequested <-chan struct{}) (pipeline.RunResult, error) {
		return c.engine.Resume(ctx, pipeline.ResumeConfig{RunID: runID, TargetDir: c.target,
			CancelRequested: func() bool { return cancellationRequested(c.target, runID, cancelRequested) }})
	})
}

// launch registers a process-local worker exactly like Resume. Engine actions
// that must not block the HTTP request (skip, deferred delivery) reuse it so
// the active-run guard stays consistent.
func (c *Controller) launch(runID string, execute func(context.Context, <-chan struct{}) (pipeline.RunResult, error)) error {
	ctx, cancel := context.WithCancel(context.Background())
	active := &worker{cancel: cancel, cancelRequested: make(chan struct{})}
	c.mu.Lock()
	if _, exists := c.active[runID]; exists {
		c.mu.Unlock()
		cancel()
		return ErrActive
	}
	c.active[runID] = active
	c.mu.Unlock()
	go c.run(runID, ctx, nil, func(ctx context.Context) (pipeline.RunResult, error) {
		return execute(ctx, active.cancelRequested)
	})
	return nil
}

func (c *Controller) Cancel(runID string) error {
	c.mu.Lock()
	if active := c.active[runID]; active != nil {
		if err := writeCancellationRequest(c.target, runID); err != nil {
			c.mu.Unlock()
			return err
		}
		select {
		case <-active.cancelRequested:
		default:
			close(active.cancelRequested)
		}
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	_, err := c.engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: c.target})
	return err
}

func (c *Controller) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	return c.approvals.Decide(runID, approvalID, decision)
}

func (c *Controller) Approvals(runID string) ([]approval.PendingApproval, error) {
	return c.approvals.List(runID)
}

// AnswerStageGrill resumes the checker with a human answer to its current
// question. The response is stored in the approval decision and fed to the
// checker as untrusted context on the next round.
func (c *Controller) AnswerStageGrill(ctx context.Context, runID, stageID, answer, actorID, actorRole string) error {
	if strings.TrimSpace(answer) == "" {
		return errors.New("ответ на вопрос грилла не может быть пустым")
	}
	return c.resolveStageGrill(ctx, runID, stageID, approval.GrillActionAnswer, answer, actorID, actorRole)
}

// SubmitStageGrillVersion resumes the checker with the exact immutable human
// revisions created while the grill approval was pending.
func (c *Controller) SubmitStageGrillVersion(ctx context.Context, runID, stageID, comment, actorID, actorRole string) error {
	return c.resolveStageGrill(ctx, runID, stageID, approval.GrillActionNewVersion, comment, actorID, actorRole)
}

// CompleteStageGrill records the responsible person's explicit decision that
// no further assistance is needed; it does not mark the check as passed.
func (c *Controller) CompleteStageGrill(ctx context.Context, runID, stageID, actorID, actorRole string) error {
	return c.resolveStageGrill(ctx, runID, stageID, approval.GrillActionDone, "", actorID, actorRole)
}

func (c *Controller) resolveStageGrill(_ context.Context, runID, stageID, action, comment, actorID, actorRole string) error {
	if strings.TrimSpace(actorID) == "" {
		return errors.New("actor id is required to resolve a grill question")
	}
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.Phase != lifecycle.PhaseWaiting || state.NextStage != stageID || state.PendingApprovalID == "" {
		return errors.New("grill action requires the current waiting stage")
	}
	value, err := c.approvals.Load(runID, state.PendingApprovalID)
	if err != nil {
		return err
	}
	if value.Kind != approval.KindGrillQuestions || value.Trigger != approval.GrillTrigger ||
		value.FromStage != stageID || value.ToStage != stageID || value.Status != approval.StatusPending {
		return errors.New("current stage has no pending grill question")
	}
	stage, err := c.pinnedStage(runID, stageID)
	if err != nil {
		return err
	}
	if !containsAction(value.Actions, action) || actorRole != stage.Function || !containsAction(value.RequiredRoles, actorRole) {
		return errors.New("actor role or action is not allowed for this grill question")
	}
	decision := approval.Decision{
		ActorID: actorID, ActorRole: actorRole, Action: action, Comment: comment,
		SubjectHash: value.SubjectHash, ControllerAuthenticated: true,
	}
	if action == approval.GrillActionNewVersion {
		var payload approval.GrillQuestionsPayload
		if err := json.Unmarshal(value.Payload, &payload); err != nil {
			return errors.New("grill question payload is invalid")
		}
		decision.ArtifactRevisions, err = c.reviewedRevisions(runID, value)
		if err != nil {
			return fmt.Errorf("resolve new grill result revisions: %w", err)
		}
		changed, err := c.grillResultContentChanged(runID, value.AttemptID, payload.ArtifactRevisions, payload.ArtifactContentSHA256, decision.ArtifactRevisions)
		if err != nil {
			return fmt.Errorf("verify changed grill result content: %w", err)
		}
		if !changed {
			return errors.New("new_version requires result content to differ from the pinned revision")
		}
	}
	if _, err := c.approvals.Decide(runID, value.ID, decision); err != nil {
		return err
	}
	return c.Resume(runID)
}

func (c *Controller) grillResultContentChanged(runID, attemptID string, baseline, baselineSHA256, selected map[string]string) (bool, error) {
	artifacts, err := humanartifact.New(c.target)
	if err != nil {
		return false, err
	}
	changed := false
	for artifactPath, revisionID := range selected {
		baselineID := baseline[artifactPath]
		var beforeSHA string
		if baselineID != "" {
			before, err := artifacts.Get(runID, artifactPath, baselineID)
			if err != nil {
				return false, fmt.Errorf("read pinned revision %q: %w", artifactPath, err)
			}
			beforeSHA = humanartifact.Digest([]byte(before.Content))
			if before.RunID != runID || before.ArtifactPath != artifactPath || before.SHA256 != beforeSHA {
				return false, fmt.Errorf("pinned revision content digest mismatch for %q", artifactPath)
			}
		} else if expectedSHA := baselineSHA256[artifactPath]; expectedSHA != "" {
			beforeSHA, err = c.verifiedAttemptOutputSHA256(runID, attemptID, artifactPath, expectedSHA)
			if err != nil {
				return false, fmt.Errorf("verify immutable source output %q: %w", artifactPath, err)
			}
		} else {
			return false, fmt.Errorf("selected revision %q has no pinned baseline for %q", revisionID, artifactPath)
		}
		after, err := artifacts.Get(runID, artifactPath, revisionID)
		if err != nil {
			return false, fmt.Errorf("read selected revision %q: %w", artifactPath, err)
		}
		afterSHA := humanartifact.Digest([]byte(after.Content))
		if after.RunID != runID || after.ArtifactPath != artifactPath || after.SHA256 != afterSHA {
			return false, fmt.Errorf("revision content digest mismatch for %q", artifactPath)
		}
		if beforeSHA != afterSHA {
			changed = true
		}
	}
	return changed, nil
}

func (c *Controller) verifiedAttemptOutputSHA256(runID, attemptID, artifactPath, expectedSHA string) (string, error) {
	if attemptID == "" || expectedSHA == "" {
		return "", errors.New("attempt, path, and expected digest are required")
	}
	if len(expectedSHA) != 64 {
		return "", errors.New("source output baseline digest is malformed")
	}
	if _, err := hex.DecodeString(expectedSHA); err != nil {
		return "", errors.New("source output baseline digest is malformed")
	}
	runDir := filepath.Join(c.target, ".ai-team", "runs", runID)
	_, manifest, err := evidence.ReadAttemptManifest(nil, runDir, runID, attemptID)
	if err != nil {
		return "", err
	}
	if manifest.RunID != runID || manifest.AttemptID != attemptID {
		return "", errors.New("attempt manifest identity mismatch")
	}
	var output *evidence.ArtifactRecord
	for i := range manifest.Outputs {
		if manifest.Outputs[i].EvidencePath == artifactPath {
			output = &manifest.Outputs[i]
			break
		}
	}
	if output == nil || output.Type != "file" || !strings.EqualFold(output.SHA256, expectedSHA) {
		return "", errors.New("baseline digest is not pinned by the source attempt manifest")
	}
	cleanPath := filepath.Clean(filepath.FromSlash(output.EvidencePath))
	if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", errors.New("source output path is not local to the run")
	}
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, cleanPath), humanartifact.MaxContentBytes)
	if err != nil {
		return "", err
	}
	actualSHA := humanartifact.Digest(data)
	if !strings.EqualFold(actualSHA, output.SHA256) || int64(len(data)) != output.Size {
		return "", errors.New("source output bytes do not match the pinned attempt manifest")
	}
	return actualSHA, nil
}

// ChangeStageExecutor persists a human-selected executor for the stage that is
// currently ready. The override is bound to the pending approval visit, so the
// pipeline applies it once and a later visit cannot inherit it. The
// executor_changed evidence event is emitted by the engine when the run resumes,
// keeping state transitions inside the engine.
func (c *Controller) ChangeStageExecutor(runID, stageID, executor, actorID string) error {
	if strings.TrimSpace(actorID) == "" {
		return errors.New("actor id is required to change a stage executor")
	}
	if executor != "human" && executor != "agent" {
		return errors.New("executor must be human or agent")
	}
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" || state.NextStage != stageID {
		return errors.New("executor можно менять только у текущего ready stage с ожидающим approval")
	}
	stage, err := c.pinnedStage(runID, stageID)
	if err != nil {
		return err
	}
	if executor == "agent" && stage.Agent == "" {
		return errors.New("у stage нет привязанного агента")
	}
	value, err := c.approvals.Load(runID, state.PendingApprovalID)
	if err != nil {
		return err
	}
	opensStage := false
	for _, target := range value.Targets {
		if target == stageID {
			opensStage = true
			break
		}
	}
	if value.Status != approval.StatusPending || !opensStage {
		return errors.New("approval больше не открывает этот stage")
	}
	stateStore, err := lifecycle.NewStore(c.target)
	if err != nil {
		return err
	}
	previousState := state
	previousExecutor := stage.Executor
	if prior, ok := state.ExecutorOverrides[stageID]; ok && prior.ApprovalID == state.PendingApprovalID {
		previousExecutor = prior.Executor
	}
	if state.ExecutorOverrides == nil {
		state.ExecutorOverrides = make(map[string]lifecycle.ExecutorOverride)
	}
	state.ExecutorOverrides[stageID] = lifecycle.ExecutorOverride{
		Executor: executor, PreviousExecutor: previousExecutor, ActorID: actorID,
		ApprovalID: state.PendingApprovalID, VisitID: state.PendingApprovalID, ChangedAt: time.Now().UTC(),
	}
	return stateStore.Save(previousState, state)
}

// RunStageAgent dispatches the configured agent for a ready or resumable stage.
// A pending human input approval is resolved with run_agent (or refine_agent,
// which feeds the current result back as refinement input) before resuming.
func (c *Controller) RunStageAgent(_ context.Context, runID, stageID, actorID string, refine bool) error {
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.NextStage != stageID {
		return fmt.Errorf("stage %q is not current", stageID)
	}
	switch state.Phase {
	case lifecycle.PhaseResumable:
		return c.Resume(runID)
	case lifecycle.PhaseWaiting:
		if state.PendingApprovalID == "" {
			return errors.New("stage has no pending approval")
		}
		value, err := c.approvals.Load(runID, state.PendingApprovalID)
		if err != nil {
			return err
		}
		if value.Kind != approval.KindInput || value.Trigger != humanInputTrigger || value.FromStage != stageID {
			return errors.New("agent run requires a pending human input approval")
		}
		stage, err := c.pinnedStage(runID, stageID)
		if err != nil {
			return err
		}
		action, comment := "run_agent", ""
		if refine {
			action = "refine_agent"
			var payload approval.InputPayload
			if len(value.Payload) == 0 || json.Unmarshal(value.Payload, &payload) != nil {
				return errors.New("refine requires the current agent result payload")
			}
			comment = payload.InitialResult
			if strings.TrimSpace(comment) == "" {
				return errors.New("refine_agent requires the current result as input")
			}
		}
		if _, err := c.approvals.Decide(runID, value.ID, approval.Decision{
			ActorID: actorID, ActorRole: stage.Function, Action: action, Comment: comment,
			SubjectHash: value.SubjectHash, ControllerAuthenticated: true,
		}); err != nil {
			return err
		}
		return c.Resume(runID)
	default:
		return errors.New("agent run requires a ready or resumable stage")
	}
}

// SkipStage records an explicit, reasoned skip. Pending human input resolves its
// stage-bound approval with the skip action (preserving the actor identity);
// an agent stage is skipped from its resumable checkpoint through the engine.
func (c *Controller) SkipStage(_ context.Context, runID, stageID, reason, actorID, actorRole string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("пропуск этапа требует причину")
	}
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.NextStage != stageID {
		return fmt.Errorf("stage %q is not current", stageID)
	}
	switch state.Phase {
	case lifecycle.PhaseWaiting:
		if state.PendingApprovalID == "" {
			return errors.New("stage has no pending approval")
		}
		value, err := c.approvals.Load(runID, state.PendingApprovalID)
		if err != nil {
			return err
		}
		if value.Kind != approval.KindInput || value.Trigger != humanInputTrigger || value.FromStage != stageID ||
			!containsAction(value.Actions, "skip") {
			return errors.New("stage has no pending skippable human input")
		}
		stage, err := c.pinnedStage(runID, stageID)
		if err != nil {
			return err
		}
		if !stage.Skippable {
			return fmt.Errorf("stage %q is not configured as skippable", stageID)
		}
		if _, err := c.approvals.Decide(runID, value.ID, approval.Decision{
			ActorID: actorID, ActorRole: actorRole, Action: "skip", Comment: reason,
			SubjectHash: value.SubjectHash, ControllerAuthenticated: true,
		}); err != nil {
			return err
		}
		return c.Resume(runID)
	case lifecycle.PhaseResumable:
		engine, ok := c.engine.(skipStageEngine)
		if !ok {
			return errors.New("run engine does not support stage skip")
		}
		return c.launch(runID, func(ctx context.Context, _ <-chan struct{}) (pipeline.RunResult, error) {
			return engine.SkipStage(ctx, pipeline.SkipStageConfig{RunID: runID, TargetDir: c.target, StageID: stageID, Reason: reason})
		})
	default:
		return errors.New("stage cannot be skipped in its current state")
	}
}

// ReturnStage resolves the current ready approval with the exact configured
// return route to toStage. The reviewed artifact revisions are pinned on the
// first vote and reused afterwards, so a return cannot silently adopt a newer
// human edit made while the approval stayed pending.
func (c *Controller) ReturnStage(_ context.Context, runID, stageID, toStage, reason, actorID, actorRole string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("для возврата требуется причина")
	}
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.NextStage != stageID {
		return fmt.Errorf("stage %q is not current", stageID)
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		return errors.New("return requires a current waiting stage")
	}
	value, err := c.approvals.Load(runID, state.PendingApprovalID)
	if err != nil {
		return err
	}
	action := ""
	for _, candidate := range value.Actions {
		if strings.HasPrefix(candidate, "return_to_") && value.Targets[candidate] == toStage {
			action = candidate
			break
		}
	}
	if action == "" {
		return fmt.Errorf("return to %q is not offered by the pending approval", toStage)
	}
	revisions, err := c.reviewedRevisions(runID, value)
	if err != nil {
		return err
	}
	if _, err := c.approvals.Decide(runID, value.ID, approval.Decision{
		ActorID: actorID, ActorRole: actorRole, Action: action, Comment: reason,
		SubjectHash: value.SubjectHash, ArtifactRevisions: revisions, ControllerAuthenticated: true,
	}); err != nil {
		return err
	}
	return c.Resume(runID)
}

func (c *Controller) loadLifecycle(runID string) (lifecycle.State, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return lifecycle.State{}, err
	}
	stateStore, err := lifecycle.NewStore(c.target)
	if err != nil {
		return lifecycle.State{}, err
	}
	return stateStore.Load(runID)
}

func (c *Controller) pinnedStage(runID, stageID string) (config.TemplateStage, error) {
	store, err := config.NewTemplateStore(c.target)
	if err != nil {
		return config.TemplateStage{}, errors.New("pinned template store недоступен")
	}
	data, _, found, err := store.ReadPinnedRun(runID)
	if err != nil || !found {
		return config.TemplateStage{}, errors.New("у run нет проверяемого pinned template")
	}
	template, err := config.ParseYAML(data)
	if err != nil {
		return config.TemplateStage{}, errors.New("pinned template повреждён")
	}
	for _, stage := range template.Stages {
		if stage.ID == stageID {
			return stage, nil
		}
	}
	return config.TemplateStage{}, fmt.Errorf("stage %q отсутствует в pinned template", stageID)
}

// reviewedRevisions mirrors the legacy decision pinning: the first vote fixes
// the artifact revisions (including an intentionally empty selection); later
// votes reuse that binding. Only when no vote exists yet does it read the
// reviewed attempt's latest human revisions.
func (c *Controller) reviewedRevisions(runID string, value approval.PendingApproval) (map[string]string, error) {
	if len(value.Decisions) > 0 {
		pinned := value.Decisions[0].ArtifactRevisions
		revisions := make(map[string]string, len(pinned))
		for path, revisionID := range pinned {
			revisions[path] = revisionID
		}
		return revisions, nil
	}
	if value.AttemptID == "" {
		return nil, nil
	}
	_, manifest, err := evidence.ReadAttemptManifest(nil, filepath.Join(c.target, ".ai-team", "runs", runID), runID, value.AttemptID)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if manifest.RunID != runID || manifest.AttemptID != value.AttemptID {
		return nil, errors.New("approval attempt manifest identity mismatch")
	}
	store, err := humanartifact.New(c.target)
	if err != nil {
		return nil, err
	}
	selection := make(map[string]string)
	for _, output := range manifest.Outputs {
		revisions, listErr := store.List(runID, output.EvidencePath)
		if listErr != nil {
			return nil, listErr
		}
		if len(revisions) > 0 {
			selection[output.EvidencePath] = revisions[len(revisions)-1].ID
		}
	}
	for _, artifactPath := range []string{
		"stages/" + manifest.Stage + "/result.md",
		"stages/" + manifest.Stage + "/result.link",
		"stages/" + manifest.Stage + "/result.txt",
	} {
		revisions, listErr := store.List(runID, artifactPath)
		if listErr != nil {
			return nil, listErr
		}
		if len(revisions) > 0 {
			selection[artifactPath] = revisions[len(revisions)-1].ID
		}
	}
	return selection, nil
}

func containsAction(actions []string, wanted string) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

// DeliverDeferred retries the exact deferred delivery prepared by a terminal
// run. Delivery executes in this trusted controller process and reuses the
// pipeline's marker, plan, candidate, and workspace-lock checks.
func (c *Controller) DeliverDeferred(ctx context.Context, runID string) (delivery.TerminalRecord, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: invalid run id: %w", err)
	}
	c.mu.Lock()
	_, active := c.active[runID]
	c.mu.Unlock()
	if active {
		return delivery.TerminalRecord{}, ErrActive
	}
	if c.deferredDelivery != nil {
		return c.deferredDelivery(ctx, runID, c.target)
	}
	if engine, ok := c.engine.(interface {
		DeliverDeferred(context.Context, string, string) (delivery.TerminalRecord, error)
	}); ok {
		return engine.DeliverDeferred(ctx, runID, c.target)
	}
	runDir := filepath.Join(c.target, ".ai-team", "runs", runID)
	return pipeline.New(nil, nil, pipeline.WithApprovalStore(c.approvals)).DeliverDeferred(ctx, runDir, "", c.target)
}

func (c *Controller) run(runID string, ctx context.Context, lock *evidence.WorkspaceLock, execute func(context.Context) (pipeline.RunResult, error)) {
	result, execErr := execute(ctx)
	if execErr == nil && result.QueueJobID > 0 && c.admissionSink != nil {
		if err := c.admissionSink(runID, result.QueueJobID); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ queue identity projection for run %s: %v\n", runID, err)
		} else if activator, ok := c.engine.(interface{ ActivateQueuedJob(int64) error }); ok {
			if err := activator.ActivateQueuedJob(result.QueueJobID); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ queue activation for run %s: %v\n", runID, err)
			}
		}
	}
	if lock != nil {
		_ = lock.Close()
	}
	c.mu.Lock()
	active := c.active[runID]
	c.mu.Unlock()
	if active != nil && requested(active.cancelRequested) {
		_, _ = c.engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: c.target})
	}
	if active != nil && active.cancel != nil {
		active.cancel()
	}
	if execErr != nil && !errors.Is(execErr, pipeline.ErrUserStopped) {
		// Фоновая ошибка не должна «проглатываться» в 202: отдаём её в
		// failure sink (SQLite projection web-сервера), иначе run остаётся
		// невидимым «призраком».
		if c.failureSink != nil {
			c.failureSink(runID, execErr.Error())
		} else {
			fmt.Fprintf(os.Stderr, "⚠ run %s завершился ошибкой: %v\n", runID, execErr)
		}
	}
	c.mu.Lock()
	delete(c.active, runID)
	c.mu.Unlock()
}

func requested(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func cancellationRequestPath(targetDir, runID string) (string, error) {
	root, err := safeio.EnsureDir(targetDir, ".ai-team", "runs", runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "cancel.requested"), nil
}

func writeCancellationRequest(targetDir, runID string) error {
	path, err := cancellationRequestPath(targetDir, runID)
	if err != nil {
		return err
	}
	if err := safeio.WriteRegularFileNoFollow(path, []byte("safe-boundary cancellation requested\n"), 0o444); err != nil {
		data, readErr := safeio.ReadRegularFile(path, 1024)
		if readErr != nil || string(data) != "safe-boundary cancellation requested\n" {
			return err
		}
	}
	return nil
}

func cancellationRequested(targetDir, runID string, signal <-chan struct{}) bool {
	if requested(signal) {
		return true
	}
	path, err := cancellationRequestPath(targetDir, runID)
	if err != nil {
		return false
	}
	data, err := safeio.ReadRegularFile(path, 1024)
	return err == nil && string(data) == "safe-boundary cancellation requested\n"
}

// FailureSink получает фоновые ошибки run для durable-фиксации.
type FailureSink func(runID, cause string)

func WithFailureSink(sink FailureSink) Option {
	return func(controller *Controller) { controller.failureSink = sink }
}

// SetFailureSink доустанавливает sink после сборки (web-сервер создаёт
// SQLite store позже контроллера).
func (c *Controller) SetFailureSink(sink FailureSink) { c.failureSink = sink }

// SetAdmissionSink links the queue's durable job identity to its run projection.
func (c *Controller) SetAdmissionSink(sink func(string, int64) error) { c.admissionSink = sink }
