package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// execute.go — исполнение графа workflow и lifecycle-переходы.

func (rs *runState) execute(ctx context.Context) error {
	if rs.runCfg.resumeDecisionAction == "reject" {
		return fmt.Errorf("%w: ожидавшийся переход отклонён человеком", ErrUserStopped)
	}
	return rs.executeGraph(ctx)
}

func (rs *runState) executeGraph(ctx context.Context) error {
	current := rs.graph.Entry
	if rs.runCfg.retryFrom != "" {
		current = rs.runCfg.retryFrom
	}
	if workflow.IsTerminal(current) {
		return graphTerminalError(current, "", nil)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		node, exists := rs.graph.Node(current)
		if !exists {
			return fmt.Errorf("workflow graph: next node %q не существует", current)
		}
		if node.MaxVisits > 0 && rs.visits[current] >= node.MaxVisits {
			return fmt.Errorf("workflow graph: max_visits=%d исчерпан для %s", node.MaxVisits, current)
		}
		index := rs.graph.Index(current)
		if err := rs.saveLifecycle(lifecycle.PhaseRunning, current); err != nil {
			return err
		}
		if err := rs.prepareControllerStageEvidence(ctx, current); err != nil {
			return fmt.Errorf("controller evidence for %s: %w", current, err)
		}
		if err := rs.authorizeStage(current); err != nil {
			return err
		}
		result := rs.runStage(ctx, index, current)
		rs.results = append(rs.results, result)
		rs.visits[current]++
		if err := rs.afterAttempt(ctx); err != nil {
			return err
		}

		if err := rs.p.notifier.Notify(ctx, result); err != nil {
			fmt.Fprintf(os.Stderr, "  %s notifier error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
		}
		if err := report.GenerateStageReport(rs.reportsDir, rs.runCfg.Feature, result.AttemptID, result, rs.task.ArtifactRoot); err != nil {
			fmt.Fprintf(os.Stderr, "  %s report error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
		}
		if rs.p.recorder != nil {
			rs.p.recorder.StageFinished(result)
		}
		if result.Status == notifier.StatusBlocked {
			fmt.Printf("\n%s %s\n", ui.Colorize("⊘ Блокер:", ui.ColorBold+ui.ColorYellow), result.Blocker)
			fmt.Printf("  Для исправления уточните задачу и запустите заново: ai-team run --feature %s --task \"<описание>\"\n",
				rs.runCfg.Feature)
		}

		edge, found := rs.graph.Edge(current, result.State.Outcome)
		if !found {
			if result.Status == notifier.StatusBlocked {
				return &BlockedError{Agent: current, Reason: result.Blocker}
			}
			if result.Err != nil {
				return result.Err
			}
			if result.Verdict.IsNegative() {
				return &NegativeVerdictError{Agent: current, Verdict: result.Verdict}
			}
			return fmt.Errorf("workflow graph: для %s outcome %s нет ребра", current, result.State.Outcome)
		}

		target := edge.To
		action := ""
		if edge.Approval != nil {
			actions := orderedGraphActions(edge)
			selected, err := rs.authorizeTransition(
				current, edge.To, "graph_outcome:"+string(edge.Outcome), result,
				edge.Approval.Roles, edge.Approval.Quorum, actions, edge.Approval.Actions,
				edge.Approval.Deferred,
			)
			if err != nil {
				return err
			}
			action = selected
			target = edge.Approval.Actions[selected]
		}
		if target == "" {
			return fmt.Errorf("workflow graph: edge %s/%s выбрал пустой target", current, edge.Outcome)
		}
		transitionAt := time.Now().UTC()
		transitionData := map[string]any{
			"from": current, "outcome": string(edge.Outcome), "edge_target": edge.To,
			"action": action, "target": target,
		}
		if err := rs.evidence.Append(evidence.Event{
			Type: "transition_selected", AttemptID: result.AttemptID, Stage: current,
			Timestamp: transitionAt, Data: transitionData,
		}); err != nil {
			return err
		}
		if rs.p.recorder != nil {
			rs.p.recorder.TransitionSelected(rs.runID, result.AttemptID, transitionAt, transitionData)
		}
		rs.ps.DoneAgent(current)
		if workflow.IsTerminal(target) {
			if err := rs.saveLifecycle(lifecycle.PhaseRunning, target); err != nil {
				return err
			}
			return graphTerminalError(target, current, result.Err)
		}
		targetIndex := rs.graph.Index(target)
		if targetIndex < 0 {
			return fmt.Errorf("workflow graph: target %q не существует", target)
		}
		if targetIndex <= index {
			if err := rs.invalidateAttempts(targetIndex + 1); err != nil {
				return err
			}
			rs.extraInputs[target] = result.Outputs
		}
		if err := rs.saveLifecycle(lifecycle.PhaseRunning, target); err != nil {
			return err
		}
		current = target
	}
}

func orderedGraphActions(edge workflow.Edge) []string {
	actions := make([]string, 0, len(edge.Approval.Actions))
	for action := range edge.Approval.Actions {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool {
		iPrimary := edge.Approval.Actions[actions[i]] == edge.To
		jPrimary := edge.Approval.Actions[actions[j]] == edge.To
		if iPrimary != jPrimary {
			return iPrimary
		}
		return actions[i] < actions[j]
	})
	return actions
}

func graphTerminalError(target, stage string, cause error) error {
	switch target {
	case workflow.TerminalComplete:
		return nil
	case workflow.TerminalStop:
		return fmt.Errorf("%w: graph transition после %s", ErrUserStopped, stage)
	case workflow.TerminalBlocked:
		return &BlockedError{Agent: stage, Reason: "graph terminal blocked"}
	case workflow.TerminalFailed:
		if cause != nil {
			return cause
		}
		return fmt.Errorf("workflow graph завершил run как failed после %s", stage)
	default:
		return fmt.Errorf("workflow graph: неизвестный terminal target %q", target)
	}
}

func replayedStageResults(run evidence.ReplayedRun) []notifier.StageResult {
	results := make([]notifier.StageResult, 0, len(run.Attempts))
	for _, attempt := range run.Attempts {
		var attemptErr error
		if attempt.Error != "" {
			attemptErr = errors.New(attempt.Error)
		}
		results = append(results, notifier.StageResult{
			RunID: run.RunID, AttemptID: attempt.AttemptID, Name: attempt.Stage,
			StageIndex: attempt.StageIndex, StartedAt: attempt.StartedAt,
			FinishedAt: attempt.FinishedAt, Duration: attempt.FinishedAt.Sub(attempt.StartedAt),
			Status: attempt.Status, State: attempt.State, Verdict: verdict.Verdict(attempt.Verdict),
			Blocker: attempt.Blocker, Err: attemptErr, Superseded: attempt.Superseded,
		})
	}
	return results
}

func indexOf(values []string, expected string) int {
	for index, value := range values {
		if value == expected {
			return index
		}
	}
	return -1
}

func (rs *runState) saveLifecycle(phase lifecycle.Phase, nextStage string) error {
	next := rs.lifecycleState
	next.Phase = phase
	next.NextStage = nextStage
	next.PendingApprovalID = ""
	next.AttemptOrdinal = rs.attemptOrdinal
	if err := rs.lifecycleStore.Save(rs.lifecycleState, next); err != nil {
		return fmt.Errorf("lifecycle checkpoint: %w", err)
	}
	rs.lifecycleState = next
	return nil
}

func (rs *runState) saveWaiting(nextStage, approvalID string) error {
	next := rs.lifecycleState
	next.Phase = lifecycle.PhaseWaiting
	next.NextStage = nextStage
	next.PendingApprovalID = approvalID
	next.AttemptOrdinal = rs.attemptOrdinal
	if err := rs.lifecycleStore.Save(rs.lifecycleState, next); err != nil {
		return fmt.Errorf("lifecycle approval checkpoint: %w", err)
	}
	rs.lifecycleState = next
	return nil
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
	rs.loopbackCycles++
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
func (rs *runState) stageOutputs(stage, attemptID string) ([]runtime.Artifact, error) {
	if attemptID != "" {
		runDir := filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs", rs.runID)
		manifestPath := filepath.Join(runDir, "attempts", attemptID, "manifest.json")
		data, err := safeio.ReadRegularFile(manifestPath, maxArtifactFileBytes)
		if err != nil {
			return nil, fmt.Errorf("approval source manifest: %w", err)
		}
		var manifest evidence.AttemptManifest
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&manifest); err != nil {
			return nil, fmt.Errorf("approval source manifest: %w", err)
		}
		if manifest.RunID != rs.runID || manifest.AttemptID != attemptID || manifest.Stage != stage {
			return nil, errors.New("approval source manifest identity mismatch")
		}
		outputs := make([]runtime.Artifact, 0, len(manifest.Outputs))
		for _, output := range manifest.Outputs {
			path := filepath.Join(runDir, filepath.FromSlash(output.EvidencePath))
			relative, err := filepath.Rel(runDir, path)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("approval evidence path %s выходит за run", output.EvidencePath)
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, fmt.Errorf("approval evidence output %s: %w", output.Name, err)
			}
			outputs = append(outputs, runtime.Artifact{
				Name: output.Name, Path: path,
				Size: info.Size(), ModTime: info.ModTime(),
			})
		}
		return outputs, nil
	}
	definition, err := rs.p.reg.Load(stage)
	if err != nil {
		return nil, fmt.Errorf("approval source stage %s: %w", stage, err)
	}
	outputs := make([]runtime.Artifact, 0, len(definition.Outputs))
	for _, name := range sortedStringMapKeys(definition.Outputs) {
		path := filepath.Join(rs.task.ArtifactRoot, runtime.ReplaceVars(definition.Outputs[name], rs.runCfg.Feature))
		if err := validateExistingArtifactPath(rs.task.ArtifactRoot, path); err != nil {
			return nil, fmt.Errorf("approval source output %s: %w", name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("approval source output %s: %w", name, err)
		}
		outputs = append(outputs, runtime.Artifact{Name: name, Path: path, Size: info.Size(), ModTime: info.ModTime()})
	}
	return outputs, nil
}

func (rs *runState) afterAttempt(ctx context.Context) error {
	if rs.candidate == nil {
		return nil
	}
	if err := rs.writeCandidateEvidence(ctx, "candidate.json", "run_candidate"); err != nil {
		return fmt.Errorf("candidate identity: %w", err)
	}
	source := filepath.Join(rs.task.ArtifactRoot, rs.runCfg.Feature, ".control", "candidate.json")
	data, err := safeio.ReadRegularFile(source, maxArtifactFileBytes)
	if err != nil {
		return err
	}
	var document candidateEvidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if err := writeControllerJSON(filepath.Join(rs.evidence.RunDir(), "candidate.json"), document); err != nil {
		return err
	}
	return syncArtifactProjection(rs.task.ArtifactRoot, filepath.Join(rs.runCfg.TargetDir, ".ai-team", "artifacts"), rs.runCfg.Feature)
}
