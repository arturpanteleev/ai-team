package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

func TestStageApprovalAndReturnFeedbackReadReservedAttemptManifest(t *testing.T) {
	target := t.TempDir()
	runID, attemptID := "pipeline-canonical-run", "attempt-1"
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	outputRel := filepath.ToSlash(filepath.Join("attempts", attemptID, "artifacts", "review.md"))
	outputPath := filepath.Join(runDir, filepath.FromSlash(outputRel))
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("reviewed output"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := evidence.AttemptManifest{SchemaVersion: evidence.SchemaVersion, RunID: runID, AttemptID: attemptID,
		Stage: "reviewer", StageIndex: 1, StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
		Status: "completed", Execution: "success", Decision: "return", Outcome: "reviewed",
		Outputs: []evidence.ArtifactRecord{{Name: "review", EvidencePath: outputRel}}}
	store := evidence.ControllerAttemptManifestStore{TargetDir: target}
	if err := store.Reserve(runID); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(runID, manifest); err != nil {
		t.Fatal(err)
	}
	rs := &runState{p: &Pipeline{}, runID: runID, runCfg: RunConfig{TargetDir: target}}
	outputs, err := rs.stageOutputs("reviewer", attemptID)
	if err != nil || len(outputs) != 1 || outputs[0].Path != outputPath {
		t.Fatalf("approval did not resolve canonical manifest output: outputs=%+v err=%v", outputs, err)
	}
	approvalValue := approval.PendingApproval{RunID: runID, AttemptID: attemptID}
	references, err := returnArtifactReferences(target, approvalValue)
	if err != nil || !strings.Contains(references, outputRel) || !strings.Contains(references, "SHA-256") {
		t.Fatalf("return feedback did not resolve canonical manifest output: %q err=%v", references, err)
	}
	if err := os.Remove(filepath.Join(runDir, "attempts", attemptID, "manifest.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := rs.stageOutputs("reviewer", attemptID); err != nil {
		t.Fatalf("reserved approval read should continue using canonical manifest: %v", err)
	}
}
