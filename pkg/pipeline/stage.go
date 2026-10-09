package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// stage.go — исполнение одного этапа: runtime вызов, guard артефактов,
// парсинг вердикта и сбор входов/выходов.

func uniqueAbsoluteReadPaths(paths []string) ([]string, error) {
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("denied path must be absolute and clean: %q", path)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

// ErrStageTimeout — отдельный sentinel таймаута отдельной стадии. Намеренно
// НЕ оборачивает context.DeadlineExceeded (в отличие от типовой ошибки),
// чтобы бюджетный guard в pipeline.go (errors.Is(runErr, context.DeadlineExceeded))
// не принимал таймаут стадии за превышение общего budget run'а и не превращал
// resumable стадию в терминальную бюджет-ошибку.
var ErrStageTimeout = errors.New("stage timeout")

// stageTimeoutError — ошибка таймаута стадии. Реализует Is так, что матчится
// только ErrStageTimeout, но не context.DeadlineExceeded → бюджетный guard
// и resumable-переход finalize.go не путают её с подлинным превышением budget.
type stageTimeoutError struct {
	stage   string
	timeout time.Duration
}

func (e *stageTimeoutError) Error() string {
	return fmt.Sprintf("этап %s превысил таймаут %s", e.stage, e.timeout)
}

func (e *stageTimeoutError) Is(target error) bool {
	return target == ErrStageTimeout
}

func (rs *runState) runStage(ctx context.Context, i int, name string) (r notifier.StageResult) {
	stageStart := time.Now()
	rs.attemptOrdinal++
	attemptID := rs.evidence.NewAttemptID(name, rs.attemptOrdinal)
	r = notifier.StageResult{
		RunID:       rs.runID,
		AttemptID:   attemptID,
		Name:        name,
		Executor:    "agent",
		StageIndex:  i + 1,
		TotalStages: len(rs.names),
		StartedAt:   stageStart.UTC(),
	}
	var evidenceInputs []evidence.Artifact
	modelAttempt := false
	executionInvoked := false
	agentEventStarted := false
	cleanupEvidenceInputs := func() error { return nil }
	fail := func(err error) notifier.StageResult {
		r.Err = err
		r.Status = notifier.StatusFailed
		r.Duration = time.Since(stageStart)
		return r
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "attempt_started", Stage: name, AttemptID: attemptID, Timestamp: stageStart.UTC(),
		Data: map[string]any{"stage_index": i + 1, "executor": "agent"},
	}); err != nil {
		return fail(fmt.Errorf("агент %s: запись attempt_started: %w", name, err))
	}
	stageApproval := rs.activeResolvedApproval(name)
	defer func() {
		r.FinishedAt = time.Now().UTC()
		r.Duration = r.FinishedAt.Sub(r.StartedAt)
		r.Summary = report.ReadStageSummary(rs.task.ArtifactRoot, rs.runCfg.Feature, name)
		rs.deriveStageState(&r)
		if modelAttempt && !executionInvoked {
			// This attempt failed before the runtime was invoked, so its known
			// token usage is zero rather than missing.
			r.Usage = &workflow.AttemptUsage{Attested: true}
			rs.usageTotal.Attested = true
		}
		manifest := evidence.AttemptManifest{
			AttemptID: attemptID, Stage: name, Executor: "agent", StageIndex: i + 1, TotalStages: len(rs.names),
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			Status: r.Status, Verdict: string(r.Verdict), Blocker: r.Blocker,
			Execution: string(r.State.Execution), Decision: string(r.State.Decision), Outcome: string(r.State.Outcome),
			Checks:          append([]checks.Result(nil), r.Checks...),
			Mutations:       append([]string(nil), r.Mutations...),
			MutationChanges: append([]workflow.MutationChange(nil), r.MutationChanges...),
			Delivery:        r.Delivery,
			Usage:           r.Usage,
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
			if rs.p.attemptManifestWriter != nil {
				_, canonicalCandidate, readErr := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), rs.evidence.RunDir(), rs.runID, attemptID)
				if readErr == nil {
					readErr = rs.p.attemptManifestWriter.WriteAttemptManifest(canonicalCandidate)
				}
				if readErr != nil {
					r.Err = errors.Join(r.Err, fmt.Errorf("controller attempt manifest %s: %w", attemptID, readErr))
					rs.deriveStageState(&r)
					manifestPublished = false
				}
			}
		}
		if cleanupErr := cleanupEvidenceInputs(); cleanupErr != nil {
			logging.Printf("warning: cleanup inflight input snapshot for attempt %s failed: %v; remaining files are included in terminal evidence", attemptID, cleanupErr)
		}
		data := map[string]any{
			"status": r.Status, "execution": r.State.Execution, "decision": r.State.Decision,
			"outcome": r.State.Outcome, "verdict": r.Verdict, "executor": "agent",
		}
		if manifestPublished {
			digest, _, digestErr := evidence.AttemptManifestDigest(rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, attemptID)
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
		if agentEventStarted {
			agentData := map[string]any{"status": r.Status}
			if stageApproval != nil && stageApproval.Kind == approval.KindInput &&
				(stageApproval.ResolvedAction == "run_agent" || stageApproval.ResolvedAction == "refine_agent") {
				agentData["action"] = stageApproval.ResolvedAction
				agentData["approval_id"] = stageApproval.ID
			}
			if r.Err != nil {
				agentData["error"] = r.Err.Error()
			}
			if err := rs.appendAgentFinished(evidence.Event{Type: "agent_finished", Stage: name, AttemptID: attemptID, Timestamp: r.FinishedAt, Data: agentData}); err != nil {
				r.Err = errors.Join(r.Err, fmt.Errorf("запись agent_finished %s: %w", attemptID, err))
				logging.Printf("error: запись agent_finished %s не подтверждена: %v", attemptID, err)
			}
		}
	}()
	agentStartData := map[string]any{"stage_index": i + 1}
	if stageApproval != nil && stageApproval.Kind == approval.KindInput &&
		(stageApproval.ResolvedAction == "run_agent" || stageApproval.ResolvedAction == "refine_agent") {
		agentStartData["action"] = stageApproval.ResolvedAction
		agentStartData["approval_id"] = stageApproval.ID
		if decision := lastApprovalDecision(stageApproval); decision.ActorID != "" {
			agentStartData["started_by"] = decision.ActorID
		}
	}
	if err := rs.evidence.Append(evidence.Event{Type: "agent_started", Stage: name, AttemptID: attemptID, Timestamp: stageStart.UTC(), Data: agentStartData}); err != nil {
		return fail(fmt.Errorf("агент %s: запись agent_started: %w", name, err))
	}
	agentEventStarted = true

	rs.ps.StartAgent(i+1, name)
	if rs.p.recorder != nil {
		rs.p.recorder.StageStarted(rs.runID, attemptID, name, i+1, stageStart.UTC())
	}
	logging.Printf("\n%s %s\n",
		ui.Colorize("▶", ui.ColorCyan),
		ui.Colorize(name, ui.ColorBold+ui.ColorYellow))

	a, err := rs.p.loadStageDefinition(name)
	if err != nil {
		return fail(fmt.Errorf("ошибка загрузки агента %s: %w", name, err))
	}
	if a == nil {
		return fail(fmt.Errorf("human stage %s must execute through the typed input path", name))
	}
	modelAttempt = a.Kind != "delivery"
	agentCfg := rs.p.cfg.AgentConfig(name)
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Name: name}
	}

	// The runtime normally receives its immutable evidence snapshot. A controller
	// handoff answer is the exception: its worker-visible path is a read-only
	// bind mount and must remain the actual runtime input, not a writable copy.
	_, inputArtifacts, err := rs.collectInputs(a, name)
	r.Inputs = inputArtifacts
	if err != nil {
		return fail(err)
	}
	if rs.questionAnswerTargetStage != "" && name != rs.questionAnswerTargetStage {
		for _, input := range inputArtifacts {
			inputPath, absErr := filepath.Abs(input.Path)
			if absErr != nil {
				return fail(fmt.Errorf("agent %s: clarification input path: %w", name, absErr))
			}
			for _, protected := range rs.questionAnswerDeniedPaths {
				if filepath.Clean(inputPath) == protected {
					return fail(fmt.Errorf("agent %s: refusing to pass a prior clarification answer into a later stage", name))
				}
			}
		}
	}
	evidenceInputs, cleanupEvidenceInputs, err = rs.evidence.SnapshotInputs(attemptID, toEvidenceArtifacts(inputArtifacts))
	if err != nil {
		return fail(fmt.Errorf("агент %s: immutable input snapshot: %w", name, err))
	}
	for index, input := range inputArtifacts {
		if input.Name != "clarification-answer" && !(input.Name == "business-brief" && rs.brief.Kind == "clarification") {
			continue
		}
		inputName := fmt.Sprintf("%03d-%s", index+1, input.Name)
		if index >= len(evidenceInputs) {
			return fail(fmt.Errorf("agent %s: clarification-bearing input snapshot is missing", name))
		}
		rs.questionAnswerDeniedPaths = append(rs.questionAnswerDeniedPaths, evidenceInputs[index].Path)
		rs.questionAnswerDeniedPaths = append(rs.questionAnswerDeniedPaths,
			filepath.Join(rs.evidence.RunDir(), "attempts", attemptID, "inputs", inputName, filepath.Base(input.Path)))
	}
	inputs := toRuntimeArtifacts(evidenceInputs)
	if rs.p.questionAnswerInputs != nil {
		for _, materialized := range inputArtifacts {
			if materialized.Name != "clarification-answer" {
				continue
			}
			for index := range inputs {
				if inputs[index].Name == materialized.Name {
					inputs[index] = materialized
					break
				}
			}
		}
	}
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
			AskQuestions: a.AskQuestions,
			ReadScope:    a.ReadScope,
			MCPServers:   append([]runtime.MCPServerConfig(nil), agentCfg.MCPServers...),
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
		workspaceBefore, err = captureWorkspaceSnapshot(rs.sourceDir())
		if err != nil {
			return fail(fmt.Errorf("агент %s: не удалось снять workspace baseline: %w", name, err))
		}
		gitBefore, gitAvailable, err = captureGitMetadataSnapshot(rs.sourceDir())
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
		if rs.candidate != nil {
			currentLive, liveErr := checks.WorkspaceDigest(rs.runCfg.TargetDir)
			if liveErr != nil || currentLive != rs.liveWorkspaceSHA {
				r.ValidationFailed = true
				r.Err = errors.Join(r.Err, fmt.Errorf("live workspace изменён во время isolated candidate attempt"))
				r.Status = notifier.StatusFailed
			}
		}
	}()

	var execErr error
	executionInvoked = true
	if a.Kind == "delivery" {
		execErr = rs.writeDeliveryPlan(stageCtx, a, preconditions)
	} else {
		stageTask := *rs.task
		stageTask.DeniedReadPaths, err = uniqueAbsoluteReadPaths(rs.questionAnswerDeniedPaths)
		if err != nil {
			return fail(fmt.Errorf("agent %s: clarification read boundary: %w", name, err))
		}
		execErr = stageRuntime.Execute(stageCtx, runtimeAgent, &stageTask, inputs)
	}
	// Persist reported usage before interpreting the execution result so that
	// interrupted or failed model invocations still contribute their attested
	// tokens to the durable per-attempt record.
	if stageRuntime != nil {
		r.Usage = &workflow.AttemptUsage{}
		reported := false
		if reporter, ok := stageRuntime.(runtime.UsageReporter); ok {
			if u := reporter.Usage(); u != nil && u.Attested && u.TokensInput >= 0 && u.TokensOutput >= 0 &&
				!math.IsNaN(u.CostUSD) && !math.IsInf(u.CostUSD, 0) && u.CostUSD >= 0 {
				r.Usage = &workflow.AttemptUsage{Attested: true, TokensInput: u.TokensInput, TokensOutput: u.TokensOutput, CostUSD: u.CostUSD}
				reported = true
				rs.usageTotal.Attested = true
				costTotal := rs.usageTotal.CostUSD + u.CostUSD
				if rs.usageTotal.TokensInput > math.MaxInt64-u.TokensInput || rs.usageTotal.TokensOutput > math.MaxInt64-u.TokensOutput || math.IsInf(costTotal, 0) {
					rs.usageUnknown = true
				} else {
					rs.usageTotal.TokensInput += u.TokensInput
					rs.usageTotal.TokensOutput += u.TokensOutput
					rs.usageTotal.CostUSD = costTotal
				}
			}
		}
		if !reported {
			rs.usageUnknown = true
		}
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
		if a.AskQuestions {
			questionsPath := stageQuestionsPath(rs.task.ArtifactRoot, rs.runCfg.Feature)
			if questionData, questionErr := safeio.ReadRegularFile(questionsPath, maxQuestionBytes); questionErr == nil {
				if strings.TrimSpace(string(questionData)) == "" {
					return fail(errors.New("stage questions artifact is empty"))
				}
				info, statErr := os.Stat(questionsPath)
				if statErr != nil {
					return fail(statErr)
				}
				r.Outputs = append(r.Outputs, runtime.Artifact{Name: "questions", Path: questionsPath, Size: info.Size(), ModTime: info.ModTime()})
			} else if !os.IsNotExist(questionErr) {
				return fail(fmt.Errorf("stage questions artifact: %w", questionErr))
			}
		}
		r.Status = notifier.StatusBlocked
		r.Blocker = reason
		r.Duration = time.Since(stageStart)
		return r
	}

	if execErr != nil {
		if stageCtx.Err() == context.DeadlineExceeded && timeout > 0 && ctx.Err() == nil {
			// Исчерпан собственный per-stage таймаут, родительский (бюджетный)
			// ctx жив → resumable sentinel, не матчащий context.DeadlineExceeded.
			return fail(&stageTimeoutError{stage: name, timeout: timeout})
		}
		if stageCtx.Err() == context.DeadlineExceeded {
			// Истёк родительский (бюджетный) ctx — подлинное превышение budget,
			// сохраняем context.DeadlineExceeded для бюджетного guard'а.
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
		statePath, prepareErr := delivery.Prepare(rs.sourceDir(), rs.runCfg.Feature, plan)
		if prepareErr != nil {
			return fail(fmt.Errorf("агент %s: подготовка delivery state: %w", name, prepareErr))
		}
		if approvalErr := rs.authorizeDelivery(name, r, plan); approvalErr != nil {
			r.ControlStopped = true
			return fail(approvalErr)
		}
		// V0-9: commit/push/PR откладывается до terminal finalize — к этому
		// моменту известен attestation digest, и commit trailer'ы (run ID,
		// runtime identity, attestation digest) детерминированы. Внутри run
		// git-история не меняется (провенанс переживает потерю bundle).
		planHash, hashErr := plan.Hash()
		if hashErr != nil {
			return fail(fmt.Errorf("агент %s: delivery plan hash: %w", name, hashErr))
		}
		rs.deferredDelivery = &deferredDelivery{StatePath: statePath, PlanHash: planHash}
		if err := rs.evidence.Append(evidence.Event{
			Type: "delivery_deferred", Stage: name, AttemptID: r.AttemptID, Timestamp: time.Now().UTC(),
			Data: map[string]any{"plan_hash": planHash, "state_path": filepath.ToSlash(statePath)},
		}); err != nil {
			r.ValidationFailed = true
			return fail(fmt.Errorf("агент %s: запись delivery_deferred event: %w", name, err))
		}
		r.Delivery = &delivery.Result{PlanHash: planHash, StatePath: statePath}
	}

	definitions := mergeChecks(a.Checks, agentCfg.Checks)
	if len(definitions) > 0 {
		r.Checks, err = (checks.Runner{TargetDir: rs.sourceDir()}).RunAll(stageCtx, definitions)
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

func (rs *runState) deriveStageState(result *notifier.StageResult) {
	execution := workflow.ExecutionSucceeded
	switch {
	case errors.Is(result.Err, ErrStageTimeout):
		execution = workflow.ExecutionTimedOut
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
	if a.AskQuestions {
		paths = append(paths, stageQuestionsPath(rs.task.ArtifactRoot, rs.runCfg.Feature))
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

func (rs *runState) collectInputs(a *agent.Agent, name string) ([]runtime.Artifact, []runtime.Artifact, error) {
	var promptInputs, all []runtime.Artifact
	for _, inName := range sortedStringMapKeys(a.Inputs) {
		inPath := a.Inputs[inName]
		replaced := runtime.ReplaceVars(inPath, rs.runCfg.Feature)
		fullPath := filepath.Join(rs.task.ArtifactRoot, replaced)
		validationRoot := rs.task.ArtifactRoot
		if selected, ok := rs.selectedInputOverrides[name][inName]; ok {
			fullPath = selected.Path
			validationRoot = filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs", rs.runID)
		}
		if err := validateExistingArtifactPath(validationRoot, fullPath); err != nil {
			return nil, all, fmt.Errorf("агент %s: вход %s (%s) небезопасен: %w", name, inName, fullPath, err)
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, all, fmt.Errorf("агент %s: вход %s (%s) не найден: %w", name, inName, fullPath, err)
		}

		logging.Printf("  %s %s %s(%s, %d байт)\n",
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
		validationRoot := rs.task.ArtifactRoot
		runEvidenceRoot := filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs", rs.runID)
		if relative, relErr := filepath.Rel(runEvidenceRoot, extra.Path); relErr == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			validationRoot = runEvidenceRoot
		}
		if err := validateExistingArtifactPath(validationRoot, extra.Path); err != nil {
			return nil, all, fmt.Errorf("агент %s: loopback input %s небезопасен: %w", name, extra.Name, err)
		}
		logging.Printf("  %s %s %s(loopback)\n",
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
		logging.Printf("  %s %s %s(%s, %d байт)\n",
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
	_, err := rs.p.loadStageDefinition(name)
	if err != nil {
		return fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
	}
	return nil
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
