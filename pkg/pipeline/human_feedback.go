package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// writeReturnFeedback materializes the persisted human return reason as a
// read-only run input. It never edits the artifact or evidence being reviewed.
func writeReturnFeedback(targetDir string, value approval.PendingApproval) (runtime.Artifact, error) {
	if !strings.HasPrefix(value.ResolvedAction, "return_to_") {
		return runtime.Artifact{}, fmt.Errorf("approval action is not a return")
	}
	var reason string
	var actor, role string
	for i := len(value.Decisions) - 1; i >= 0; i-- {
		if value.Decisions[i].Action == value.ResolvedAction {
			reason = strings.TrimSpace(value.Decisions[i].Comment)
			actor, role = value.Decisions[i].ActorID, value.Decisions[i].ActorRole
			break
		}
	}
	if reason == "" {
		return runtime.Artifact{}, fmt.Errorf("return approval %s has no reason", value.ID)
	}
	target := value.Targets[value.ResolvedAction]
	references, err := returnArtifactReferences(targetDir, value)
	if err != nil {
		return runtime.Artifact{}, err
	}
	content := fmt.Sprintf("# Human feedback for %s\n\n- From: %s\n- Requested by: %s (%s)\n- Approval: %s\n- Subject SHA-256: %s\n\n## Reason and requested changes\n\n%s\n\n## Immutable artifacts from the returned attempt\n\n%s",
		target, value.FromStage, actor, role, value.ID, value.SubjectHash, reason, references)
	path := filepath.Join(targetDir, ".ai-team", "runs", value.RunID, "inputs", value.ID+"-feedback.md")
	if err := safeio.WriteRegularFileNoFollow(path, []byte(content), 0o444); err != nil {
		if info, statErr := os.Lstat(path); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return runtime.Artifact{}, err
		}
		old, readErr := safeio.ReadRegularFile(path, 1<<20)
		if readErr != nil || string(old) != content {
			return runtime.Artifact{}, fmt.Errorf("feedback input is immutable: %w", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return runtime.Artifact{}, err
	}
	return runtime.Artifact{Name: "human-return-feedback", Path: path, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func returnArtifactReferences(targetDir string, value approval.PendingApproval) (string, error) {
	runRoot := filepath.Join(targetDir, ".ai-team", "runs", value.RunID)
	manifestPath := filepath.Join(runRoot, "attempts", value.AttemptID, "manifest.json")
	data, err := safeio.ReadRegularFile(manifestPath, maxArtifactFileBytes)
	if err != nil {
		return "", fmt.Errorf("return feedback attempt manifest: %w", err)
	}
	var manifest evidence.AttemptManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.RunID != value.RunID || manifest.AttemptID != value.AttemptID {
		return "", fmt.Errorf("return feedback attempt manifest identity mismatch")
	}
	var builder strings.Builder
	store, err := humanartifact.New(targetDir)
	if err != nil {
		return "", err
	}
	for _, output := range manifest.Outputs {
		path := filepath.Join(runRoot, filepath.FromSlash(output.EvidencePath))
		relative, relErr := filepath.Rel(runRoot, path)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("return feedback artifact path escapes run: %s", output.EvidencePath)
		}
		_, _, digest, digestErr := evidence.ArtifactDigest(path)
		if digestErr != nil {
			return "", fmt.Errorf("return feedback artifact %s: %w", output.Name, digestErr)
		}
		fmt.Fprintf(&builder, "- `%s` — `%s` (SHA-256 `%s`)", output.Name, filepath.ToSlash(relative), digest)
		if revisionID := value.ArtifactRevisions[filepath.ToSlash(relative)]; revisionID != "" {
			revision, getErr := store.Get(value.RunID, filepath.ToSlash(relative), revisionID)
			if getErr != nil {
				return "", getErr
			}
			fmt.Fprintf(&builder, "; выбранная человеческая версия `%s` (SHA-256 `%s`)", revision.ID, revision.SHA256)
		}
		builder.WriteByte('\n')
	}
	if builder.Len() == 0 {
		builder.WriteString("- Артефакты попытки отсутствуют.\n")
	}
	return builder.String(), nil
}

// selectedHumanRevision replaces a source attempt artifact with the exact
// immutable revision pinned by the handoff decision. It does not consult the
// moving latest pointer during resume.
func selectedHumanRevision(targetDir, runID string, input runtime.Artifact, selection map[string]string) (runtime.Artifact, error) {
	if len(selection) == 0 {
		return input, nil
	}
	runRoot := filepath.Join(targetDir, ".ai-team", "runs", runID)
	relative, err := filepath.Rel(runRoot, input.Path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return runtime.Artifact{}, fmt.Errorf("selected artifact path escapes run evidence: %s", input.Path)
	}
	relative = filepath.ToSlash(relative)
	revisionID := selection[relative]
	if revisionID == "" {
		return input, nil
	}
	store, err := humanartifact.New(targetDir)
	if err != nil {
		return runtime.Artifact{}, err
	}
	revision, err := store.Get(runID, relative, revisionID)
	if err != nil {
		return runtime.Artifact{}, err
	}
	digest := sha256.Sum256([]byte(revision.Content))
	if hex.EncodeToString(digest[:]) != revision.SHA256 {
		return runtime.Artifact{}, fmt.Errorf("human revision %s content hash mismatch", revision.ID)
	}
	directory, err := safeio.EnsureDir(targetDir, ".ai-team", "runs", runID, "inputs", "human-revisions")
	if err != nil {
		return runtime.Artifact{}, err
	}
	path := filepath.Join(directory, revision.ID+"-"+filepath.Base(input.Path))
	if err := safeio.WriteRegularFileNoFollow(path, []byte(revision.Content), 0o444); err != nil {
		old, readErr := safeio.ReadRegularFile(path, humanartifact.MaxContentBytes)
		if readErr != nil || string(old) != revision.Content {
			return runtime.Artifact{}, fmt.Errorf("selected revision snapshot is immutable: %w", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return runtime.Artifact{}, err
	}
	input.Path, input.Size, input.ModTime = path, info.Size(), info.ModTime()
	return input, nil
}
