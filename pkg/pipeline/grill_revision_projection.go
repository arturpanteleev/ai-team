package pipeline

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

// latestInboundTransition finds the exact source attempt that dispatched a
// resumed stage. It is used only to restore an approved grill revision after a
// crash between the durable transition and the next stage attempt.
func latestInboundTransition(transitions []evidence.ReplayedTransition, target string) *evidence.ReplayedTransition {
	var latest *evidence.ReplayedTransition
	for index := range transitions {
		transition := &transitions[index]
		if transition.Target != target || transition.AttemptID == "" {
			continue
		}
		if latest == nil || transition.Sequence > latest.Sequence {
			latest = transition
		}
	}
	return latest
}

// grillRevisionSelection returns only human revisions selected by verified
// new_version decisions for this exact source attempt. Later answer decisions
// keep the most recently approved bytes; they do not revert to the immutable
// agent attempt output.
func (rs *runState) grillRevisionSelection(replayed evidence.ReplayedRun, stageID, attemptID string) (map[string]string, error) {
	values, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return nil, fmt.Errorf("list grill revisions for %s: %w", stageID, err)
	}
	var rounds []approval.PendingApproval
	for _, value := range values {
		if value.Kind != approval.KindGrillQuestions || value.Trigger != approval.GrillTrigger ||
			value.RunID != rs.runID || value.FromStage != stageID || value.AttemptID != attemptID {
			continue
		}
		rounds = append(rounds, value)
	}
	sort.Slice(rounds, func(i, j int) bool {
		left, right := grillRound(&rounds[i]), grillRound(&rounds[j])
		if left != right {
			return left < right
		}
		return rounds[i].CreatedAt.Before(rounds[j].CreatedAt)
	})

	selection := make(map[string]string)
	for _, value := range rounds {
		if value.Status != approval.StatusResolved {
			continue
		}
		if replayed.RunID != rs.runID {
			return nil, fmt.Errorf("grill approval %s has no verified run replay", value.ID)
		}
		if err := validateRecordedGrillDecision(replayed, value); err != nil {
			return nil, fmt.Errorf("verify grill approval %s: %w", value.ID, err)
		}
		if value.ResolvedAction != approval.GrillActionNewVersion {
			continue
		}
		decision := lastApprovalDecision(&value)
		if len(decision.ArtifactRevisions) == 0 {
			return nil, fmt.Errorf("grill approval %s has no pinned revisions", value.ID)
		}
		for artifactPath, revisionID := range decision.ArtifactRevisions {
			selection[artifactPath] = revisionID
		}
	}
	return selection, nil
}

// projectGrillRevisionInputs exposes the verified human revision to the next
// stage without rewriting the source attempt manifest or workspace output.
// The materialized input path includes the immutable revision ID, and the
// next attempt manifest records the full content SHA-256 when it snapshots
// that input.
func (rs *runState) projectGrillRevisionInputs(replayed evidence.ReplayedRun, sourceStage, attemptID, targetStage string) error {
	if sourceStage == "" || attemptID == "" || targetStage == "" || sourceStage == targetStage {
		return nil
	}
	stage, ok := rs.p.templateStage(sourceStage)
	if !ok || stage.GrillCheck() == nil {
		return nil
	}
	selection, err := rs.grillRevisionSelection(replayed, sourceStage, attemptID)
	if err != nil {
		return err
	}
	if len(selection) == 0 {
		return nil
	}

	runRoot := filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs", rs.runID)
	_, manifest, err := evidence.ReadAttemptManifest(rs.p.attemptManifestSource, runRoot, rs.runID, attemptID)
	if err != nil {
		return fmt.Errorf("read immutable source attempt: %w", err)
	}
	if manifest.Stage != sourceStage || manifest.AttemptID != attemptID || manifest.RunID != rs.runID {
		return errors.New("grill revision source attempt identity mismatch")
	}
	definition, err := rs.p.loadStageDefinition(targetStage)
	if err != nil {
		return fmt.Errorf("load target stage %s: %w", targetStage, err)
	}
	revisions, err := humanartifact.New(rs.runCfg.TargetDir)
	if err != nil {
		return err
	}
	for _, output := range manifest.Outputs {
		artifactPath := output.EvidencePath
		revisionID := selection[artifactPath]
		input := runtime.Artifact{
			Name: output.Name,
			Path: filepath.Join(runRoot, filepath.FromSlash(output.EvidencePath)),
		}
		if revisionID == "" {
			artifactPath = "stages/" + sourceStage + "/result" + filepath.Ext(input.Path)
			revisionID = selection[artifactPath]
		}
		if revisionID == "" {
			continue
		}
		selected, err := selectedGrillRevision(rs.runCfg.TargetDir, rs.runID, sourceStage, input, selection)
		if err != nil {
			return fmt.Errorf("materialize grill revision %s: %w", output.EvidencePath, err)
		}
		revision, err := revisions.Get(rs.runID, artifactPath, revisionID)
		if err != nil {
			return fmt.Errorf("read pinned grill revision %s: %w", revisionID, err)
		}
		_, _, digest, err := evidence.ArtifactDigest(selected.Path)
		if err != nil || revision.ID != revisionID || digest != revision.SHA256 {
			return fmt.Errorf("materialized grill revision %s failed content digest verification", revisionID)
		}

		matchedInput := false
		if definition != nil {
			for inputName, configuredPath := range definition.Inputs {
				if !grillOutputMatchesInput(rs, output, configuredPath, inputName) {
					continue
				}
				projected := selected
				projected.Name = inputName
				if rs.selectedInputOverrides[targetStage] == nil {
					rs.selectedInputOverrides[targetStage] = make(map[string]runtime.Artifact)
				}
				rs.selectedInputOverrides[targetStage][inputName] = projected
				matchedInput = true
			}
		}
		if !matchedInput {
			projected := selected
			projected.Name = "grill-selected-" + output.Name
			if !hasInputPath(rs.extraInputs[targetStage], projected.Path) {
				rs.extraInputs[targetStage] = append(rs.extraInputs[targetStage], projected)
			}
		}
	}
	return nil
}

func grillOutputMatchesInput(rs *runState, output evidence.ArtifactRecord, configuredPath, inputName string) bool {
	if inputName == output.Name {
		return true
	}
	if output.SourcePath == "" || configuredPath == "" {
		return false
	}
	configured := filepath.Join(rs.task.ArtifactRoot, runtime.ReplaceVars(configuredPath, rs.runCfg.Feature))
	configured, err := filepath.Abs(configured)
	if err != nil {
		return false
	}
	source, err := filepath.Abs(output.SourcePath)
	return err == nil && filepath.Clean(configured) == filepath.Clean(source)
}

func hasInputPath(inputs []runtime.Artifact, path string) bool {
	for _, input := range inputs {
		if input.Path == path {
			return true
		}
	}
	return false
}
