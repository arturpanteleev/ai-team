package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// stage.go — исполнение одного этапа: runtime вызов, guard артефактов,
// парсинг вердикта и сбор входов/выходов.

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
			AskQuestions: a.AskQuestions,
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
		if _, prepareErr := delivery.Prepare(rs.sourceDir(), rs.runCfg.Feature, plan); prepareErr != nil {
			return fail(fmt.Errorf("агент %s: подготовка delivery state: %w", name, prepareErr))
		}
		if approvalErr := rs.authorizeDelivery(name, r, plan); approvalErr != nil {
			r.ControlStopped = true
			return fail(approvalErr)
		}
		deliveryResult, deliveryErr := rs.p.delivery.Execute(stageCtx, delivery.Request{
			TargetDir: rs.sourceDir(), Feature: rs.runCfg.Feature, Plan: plan,
		})
		r.Delivery = &deliveryResult
		if deliveryErr != nil {
			return fail(fmt.Errorf("агент %s: delivery execution: %w", name, deliveryErr))
		}
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
		validationRoot := rs.task.ArtifactRoot
		runEvidenceRoot := filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs", rs.runID)
		if relative, relErr := filepath.Rel(runEvidenceRoot, extra.Path); relErr == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			validationRoot = runEvidenceRoot
		}
		if err := validateExistingArtifactPath(validationRoot, extra.Path); err != nil {
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
