package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func (rs *runState) replayedSkippedStage(stageID, reason string) (notifier.StageResult, bool, error) {
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.Name != stageID || result.Superseded || result.State.Outcome != workflow.OutcomeSkipped ||
			rs.stageSkipTransitioned[result.AttemptID] {
			continue
		}
		recordedReason, exists := rs.stageSkipReasons[result.AttemptID]
		if !exists {
			continue
		}
		if strings.TrimSpace(recordedReason) != strings.TrimSpace(reason) {
			return notifier.StageResult{}, false, fmt.Errorf("stage %q already has an unfinished skip with a different reason", stageID)
		}
		return result, true, nil
	}
	return notifier.StageResult{}, false, nil
}

func replayedStageSkipTransition(replayed evidence.ReplayedRun, graph workflow.Graph, stageID, reason string) bool {
	edge, exists := graph.Edge(stageID, workflow.OutcomeSkipped)
	if !exists {
		return false
	}
	for _, skipped := range replayed.StageSkips {
		if skipped.Stage != stageID || strings.TrimSpace(skipped.Reason) != strings.TrimSpace(reason) {
			continue
		}
		for _, transition := range replayed.Transitions {
			if transition.AttemptID == skipped.AttemptID && transition.From == stageID &&
				transition.Outcome == string(workflow.OutcomeSkipped) && transition.EdgeTarget == edge.To {
				return true
			}
		}
	}
	return false
}

func (rs *runState) runSkippedStage(ctx context.Context, index int, stageID, reason string) (notifier.StageResult, error) {
	if err := ctx.Err(); err != nil {
		return notifier.StageResult{}, err
	}
	stage, exists := rs.p.templateStage(stageID)
	if !exists || !stage.Skippable {
		return notifier.StageResult{}, fmt.Errorf("stage %q is not configured as skippable", stageID)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return notifier.StageResult{}, fmt.Errorf("пропуск этапа требует причину")
	}
	definition, err := rs.p.loadStageDefinition(stageID)
	if err != nil {
		return notifier.StageResult{}, fmt.Errorf("load skipped stage outputs %s: %w", stageID, err)
	}
	if err := rs.clearStageEphemeral(stageID, definition); err != nil {
		return notifier.StageResult{}, fmt.Errorf("clear stale outputs before skipping %s: %w", stageID, err)
	}

	rs.attemptOrdinal++
	started := time.Now().UTC()
	attemptID := rs.evidence.NewAttemptID(stageID, rs.attemptOrdinal)
	result := notifier.StageResult{
		RunID: rs.runID, AttemptID: attemptID, Name: stageID, Executor: "agent",
		StageIndex: index + 1, TotalStages: len(rs.names), StartedAt: started,
		Usage:          &workflow.AttemptUsage{Attested: true},
		ControlStopped: true,
	}
	rs.usageTotal.Attested = true
	rs.ps.StartAgent(index+1, stageID)
	if rs.p.recorder != nil {
		rs.p.recorder.StageStarted(rs.runID, attemptID, stageID, index+1, started)
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "attempt_started", Stage: stageID, AttemptID: attemptID, Timestamp: started,
		Data: map[string]any{"stage_index": index + 1, "executor": "agent", "stage_action": "skip"},
	}); err != nil {
		return notifier.StageResult{}, fmt.Errorf("record skipped attempt start: %w", err)
	}

	result.FinishedAt = time.Now().UTC()
	result.Duration = result.FinishedAt.Sub(started)
	rs.deriveStageState(&result)
	manifest := evidence.AttemptManifest{
		AttemptID: attemptID, Stage: stageID, Executor: result.Executor,
		StageIndex: index + 1, TotalStages: len(rs.names), StartedAt: started, FinishedAt: result.FinishedAt,
		Status: result.Status, Execution: string(result.State.Execution), Decision: string(result.State.Decision),
		Outcome: string(result.State.Outcome), Usage: result.Usage,
	}
	if err := rs.evidence.PublishAttempt(manifest, rs.task.ArtifactRoot, nil, nil); err != nil {
		return notifier.StageResult{}, fmt.Errorf("publish skipped attempt: %w", err)
	}
	digest, _, err := evidence.AttemptManifestDigest(rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, attemptID)
	if err != nil {
		return notifier.StageResult{}, fmt.Errorf("digest skipped attempt manifest: %w", err)
	}
	if rs.p.attemptManifestWriter != nil {
		_, canonical, readErr := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), rs.evidence.RunDir(), rs.runID, attemptID)
		if readErr == nil {
			readErr = rs.p.attemptManifestWriter.WriteAttemptManifest(canonical)
		}
		if readErr != nil {
			return notifier.StageResult{}, fmt.Errorf("controller skipped attempt manifest: %w", readErr)
		}
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "attempt_finished", Stage: stageID, AttemptID: attemptID, Timestamp: result.FinishedAt,
		Data: map[string]any{
			"status": result.Status, "execution": result.State.Execution, "decision": result.State.Decision,
			"outcome": result.State.Outcome, "executor": result.Executor,
			"manifest_sha256": digest, "stage_skip_reason": reason,
		},
	}); err != nil {
		return notifier.StageResult{}, fmt.Errorf("record skipped attempt finish: %w", err)
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "stage_skipped", Stage: stageID, AttemptID: attemptID, Timestamp: result.FinishedAt,
		Data: map[string]any{"reason": reason, "warning": true},
	}); err != nil {
		return notifier.StageResult{}, fmt.Errorf("record stage skip warning: %w", err)
	}
	return result, nil
}
