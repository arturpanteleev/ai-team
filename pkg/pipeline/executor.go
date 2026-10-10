package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

func (rs *runState) activeExecutorOverride(stageID string) (lifecycle.ExecutorOverride, bool) {
	override, ok := rs.lifecycleState.ExecutorOverrides[stageID]
	if !ok {
		return lifecycle.ExecutorOverride{}, false
	}
	approvalID := rs.lifecycleState.ActiveApprovalID
	if approvalID == "" {
		approvalID = rs.lifecycleState.PendingApprovalID
	}
	return override, approvalID != "" && override.ApprovalID == approvalID
}

func activeStageApprovalID(value *approval.PendingApproval, stageID string) string {
	if value == nil || value.Status != approval.StatusResolved || stageID == "" ||
		value.Targets[value.ResolvedAction] != stageID {
		return ""
	}
	if (value.Kind == approval.KindInput && value.Trigger == humanInputTrigger) ||
		(value.Kind != approval.KindQuestions && strings.HasPrefix(value.Trigger, "graph_outcome:")) {
		return value.ID
	}
	return ""
}

func (rs *runState) activeResolvedApproval(stageID string) *approval.PendingApproval {
	value := rs.resumedApproval
	if value == nil || value.Status != approval.StatusResolved || stageID == "" ||
		value.Targets[value.ResolvedAction] != stageID {
		return nil
	}
	approvalID := rs.lifecycleState.ActiveApprovalID
	if approvalID == "" {
		approvalID = rs.lifecycleState.PendingApprovalID
	}
	if approvalID == "" || approvalID != value.ID {
		return nil
	}
	return value
}

func (rs *runState) clearExecutorOverride(stageID string) {
	if rs.lifecycleState.ExecutorOverrides == nil {
		return
	}
	delete(rs.lifecycleState.ExecutorOverrides, stageID)
	if len(rs.lifecycleState.ExecutorOverrides) == 0 {
		rs.lifecycleState.ExecutorOverrides = nil
	}
}

func (rs *runState) recordExecutorChanged(stageID, from, to, actorID, visitID string, changedAt time.Time) error {
	if from == to {
		return nil
	}
	if actorID == "" || visitID == "" || changedAt.IsZero() {
		return errors.New("executor change has incomplete approval identity")
	}
	changeDigest := sha256.Sum256([]byte(strings.Join([]string{
		rs.runID, stageID, visitID, actorID, to, changedAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	changeID := hex.EncodeToString(changeDigest[:])
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return fmt.Errorf("read executor-change evidence: %w", err)
	}
	if found {
		for _, event := range events {
			if event.Type == "executor_changed" && event.Data["change_id"] == changeID {
				return nil
			}
		}
	}
	return rs.evidence.Append(evidence.Event{
		Type: "executor_changed", Stage: stageID, Timestamp: changedAt.UTC(),
		Data: map[string]any{
			"from_executor": from, "to_executor": to, "actor_id": actorID,
			"approval_id": visitID, "change_id": changeID,
		},
	})
}

func (rs *runState) readEvidenceEvents() ([]evidence.Event, bool, error) {
	if rs.p.eventLogSource != nil {
		events, err := rs.p.eventLogSource.Read(rs.runID)
		return events, true, err
	}
	reader, ok := rs.evidence.(interface {
		ReadEvents() ([]evidence.Event, error)
	})
	if !ok {
		return nil, false, nil
	}
	events, err := reader.ReadEvents()
	return events, true, err
}

func lastApprovalDecision(value *approval.PendingApproval) approval.Decision {
	if value == nil || len(value.Decisions) == 0 {
		return approval.Decision{}
	}
	return value.Decisions[len(value.Decisions)-1]
}

func sameStageTargets(actions []string, stageID string) map[string]string {
	targets := make(map[string]string, len(actions))
	for _, action := range actions {
		targets[action] = stageID
	}
	return targets
}

func (rs *runState) latestAgentStageResult(stageID, outputPath string) (string, string, string, error) {
	expectedPath, err := confinedArtifactPath(rs.task.ArtifactRoot, filepath.FromSlash(outputPath))
	if err != nil {
		return "", "", "", fmt.Errorf("human result path for %s is unsafe: %w", stageID, err)
	}
	for index := len(rs.results) - 1; index >= 0; index-- {
		previous := rs.results[index]
		if previous.Name != stageID || previous.Executor != "agent" || previous.Superseded || previous.FinishedAt.IsZero() {
			continue
		}
		hasContractOutput := false
		for _, output := range previous.Outputs {
			path, absErr := filepath.Abs(output.Path)
			if absErr != nil {
				return "", "", "", fmt.Errorf("prior agent result for %s: %w", stageID, absErr)
			}
			if path == expectedPath {
				hasContractOutput = true
				break
			}
		}
		if !hasContractOutput {
			continue
		}
		_, manifest, manifestErr := evidence.ReadAttemptManifest(rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, previous.AttemptID)
		if manifestErr != nil {
			return "", "", "", fmt.Errorf("read prior agent attempt %s manifest: %w", previous.AttemptID, manifestErr)
		}
		if manifest.RunID != rs.runID || manifest.AttemptID != previous.AttemptID || manifest.Stage != stageID || manifest.Executor != "agent" ||
			!manifest.FinishedAt.Equal(previous.FinishedAt) {
			return "", "", "", fmt.Errorf("prior agent attempt %s manifest identity mismatch", previous.AttemptID)
		}
		for _, output := range manifest.Outputs {
			sourcePath, absErr := filepath.Abs(output.SourcePath)
			if absErr != nil {
				return "", "", "", fmt.Errorf("prior agent result for %s: %w", stageID, absErr)
			}
			if sourcePath != expectedPath {
				continue
			}
			if err := validateExistingArtifactPath(rs.task.ArtifactRoot, sourcePath); err != nil {
				return "", "", "", fmt.Errorf("prior agent result for %s is unsafe: %w", stageID, err)
			}
			evidencePath, pathErr := confinedArtifactPath(rs.evidence.RunDir(), output.EvidencePath)
			if pathErr != nil {
				return "", "", "", fmt.Errorf("prior agent result evidence path for %s is unsafe: %w", stageID, pathErr)
			}
			if err := validateExistingArtifactPath(rs.evidence.RunDir(), evidencePath); err != nil {
				return "", "", "", fmt.Errorf("prior agent result evidence for %s is unsafe: %w", stageID, err)
			}
			artifactType, size, digest, digestErr := evidence.ArtifactDigest(evidencePath)
			if digestErr != nil || artifactType != "file" || size != output.Size || digest != output.SHA256 {
				return "", "", "", fmt.Errorf("prior agent result evidence for %s does not match its attempt manifest", stageID)
			}
			liveType, liveSize, liveDigest, liveErr := evidence.ArtifactDigest(sourcePath)
			if liveErr != nil || liveType != output.Type || liveSize != output.Size || liveDigest != output.SHA256 {
				return "", "", "", fmt.Errorf("prior agent result for %s changed after its attempt was recorded", stageID)
			}
			manifestDigest, _, digestErr := evidence.AttemptManifestDigest(rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, previous.AttemptID)
			if digestErr != nil {
				return "", "", "", fmt.Errorf("digest prior agent attempt %s manifest: %w", previous.AttemptID, digestErr)
			}
			events, found, eventsErr := rs.readEvidenceEvents()
			if eventsErr != nil {
				return "", "", "", fmt.Errorf("read prior agent attempt %s events: %w", previous.AttemptID, eventsErr)
			}
			manifestBound := false
			if found {
				for _, event := range events {
					if event.Type == "attempt_finished" && event.AttemptID == previous.AttemptID && event.Stage == stageID &&
						event.Data["manifest_sha256"] == manifestDigest {
						manifestBound = true
						break
					}
				}
			}
			if !manifestBound {
				return "", "", "", fmt.Errorf("prior agent attempt %s manifest is not bound by finished evidence", previous.AttemptID)
			}
			data, err := safeio.ReadRegularFile(evidencePath, approval.MaxInputCommentBytes)
			if err != nil {
				return "", "", "", fmt.Errorf("read prior agent result evidence for %s: %w", stageID, err)
			}
			return string(data), previous.AttemptID, evidencePath, nil
		}
		return "", "", "", fmt.Errorf("prior agent attempt %s manifest does not record contract output %s", previous.AttemptID, outputPath)
	}
	return "", "", "", nil
}

func (rs *runState) appendAgentFinished(event evidence.Event) error {
	appendErr := rs.evidence.Append(event)
	if appendErr == nil {
		return nil
	}
	confirmed, found, readErr := rs.agentFinishedEventRecorded(event)
	if readErr != nil {
		return errors.Join(appendErr, fmt.Errorf("check agent_finished after append error: %w", readErr))
	}
	if !found {
		return appendErr
	}
	if confirmed {
		return nil
	}

	// Retry only after the event log proves the first append did not take.
	retryErr := rs.evidence.Append(event)
	if retryErr == nil {
		return nil
	}
	confirmed, _, readErr = rs.agentFinishedEventRecorded(event)
	if readErr == nil && confirmed {
		return nil
	}
	return errors.Join(appendErr, retryErr, readErr)
}

func (rs *runState) agentFinishedEventRecorded(expected evidence.Event) (bool, bool, error) {
	events, found, err := rs.readEvidenceEvents()
	if err != nil || !found {
		return false, found, err
	}
	for _, event := range events {
		if event.Type != expected.Type || event.AttemptID != expected.AttemptID {
			continue
		}
		if event.Stage != expected.Stage || !event.Timestamp.Equal(expected.Timestamp) || len(event.Data) != len(expected.Data) {
			return false, true, nil
		}
		for key, value := range expected.Data {
			if event.Data[key] != value {
				return false, true, nil
			}
		}
		return true, true, nil
	}
	return false, true, nil
}

// addRefinementInput binds the decision's current result text to the agent's
// immutable input snapshot for this execution. The temporary lives below the
// current run evidence root so the normal path validator accepts it, and is
// removed after runStage has snapshotted it.
func (rs *runState) addRefinementInput(stageID, content string) (func(), error) {
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("refine_agent requires the current result as input")
	}
	if len(content) > 128<<10 {
		return nil, errors.New("refinement input exceeds 131072 bytes")
	}
	dir, err := os.MkdirTemp(rs.evidence.RunDir(), ".executor-input-*")
	if err != nil {
		return nil, fmt.Errorf("create refinement input: %w", err)
	}
	path := filepath.Join(dir, "current-result.md")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write refinement input: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	previous := rs.extraInputs[stageID]
	rs.extraInputs[stageID] = append(append([]runtime.Artifact(nil), previous...), runtime.Artifact{
		Name: "current-result", Path: path, Size: info.Size(), ModTime: info.ModTime(),
	})
	return func() {
		rs.extraInputs[stageID] = previous
		_ = os.RemoveAll(dir)
	}, nil
}
