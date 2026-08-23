package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/process"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// candidate_evidence.go — controller-owned evidence о кандидате:
// baseline identity, changed files, tracked patch и digests проверок.

type candidateCheck struct {
	AttemptID             string   `json:"attempt_id"`
	Name                  string   `json:"name"`
	Class                 string   `json:"class"`
	Adapter               string   `json:"adapter"`
	Command               []string `json:"command"`
	ToolPath              string   `json:"tool_path,omitempty"`
	ToolVersion           string   `json:"tool_version,omitempty"`
	Status                string   `json:"status"`
	ExitCode              int      `json:"exit_code"`
	DiscoveredTests       int      `json:"discovered_tests,omitempty"`
	PassedTests           int      `json:"passed_tests,omitempty"`
	EvidenceDigest        string   `json:"evidence_digest"`
	WorkspaceDigestBefore string   `json:"workspace_digest_before"`
	WorkspaceDigestAfter  string   `json:"workspace_digest_after"`
	StructuredSHA256      string   `json:"structured_output_sha256,omitempty"`
}

type candidateAttempt struct {
	AttemptID string `json:"attempt_id"`
	Stage     string `json:"stage"`
	Outcome   string `json:"outcome"`
	Verdict   string `json:"verdict,omitempty"`
}

type candidateEvidence struct {
	SchemaVersion        int                `json:"schema_version"`
	RunID                string             `json:"run_id"`
	Purpose              string             `json:"purpose"`
	BaselineHead         string             `json:"baseline_head,omitempty"`
	BaselineTree         string             `json:"baseline_tree,omitempty"`
	WorkspaceSHA256      string             `json:"workspace_sha256"`
	ChangedFiles         []candidateFile    `json:"changed_files"`
	TrackedPatchSHA256   string             `json:"tracked_patch_sha256,omitempty"`
	TrackedPatchBytes    int64              `json:"tracked_patch_bytes,omitempty"`
	TrackedPatchIncluded bool               `json:"tracked_patch_included"`
	TrackedPatch         string             `json:"tracked_patch,omitempty"`
	Checks               []candidateCheck   `json:"checks"`
	Attempts             []candidateAttempt `json:"attempts"`
}

type candidateFile struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Mode        string `json:"mode"`
}

func (rs *runState) writeCandidateEvidence(ctx context.Context, name, purpose string) error {
	workspaceDigest, err := checks.WorkspaceDigest(rs.sourceDir())
	if err != nil {
		return err
	}
	snapshot, err := captureWorkspaceSnapshot(rs.sourceDir())
	if err != nil {
		return err
	}
	gitState, gitAvailable, err := captureGitMetadataSnapshot(rs.sourceDir())
	if err != nil {
		return err
	}
	changedSet := make(map[string]bool)
	if gitAvailable {
		for changed := range gitState.Dirty {
			changedSet[changed] = true
		}
	}
	for _, changed := range rs.attributedDeliveryFiles() {
		changedSet[changed] = true
	}
	changed := make([]string, 0, len(changedSet))
	for path := range changedSet {
		changed = append(changed, path)
	}
	sort.Strings(changed)
	evidenceDocument := candidateEvidence{
		SchemaVersion: 1, RunID: rs.runID, Purpose: purpose,
		WorkspaceSHA256: workspaceDigest, ChangedFiles: make([]candidateFile, 0, len(changed)),
		Checks: make([]candidateCheck, 0), Attempts: make([]candidateAttempt, 0),
	}
	if gitAvailable {
		baseline := gitState.Head
		if rs.candidate != nil {
			metadata := rs.candidate.Metadata()
			baseline = metadata.BaseCommit
			evidenceDocument.BaselineTree = metadata.BaseTree
		}
		evidenceDocument.BaselineHead = baseline
		patch, patchErr := collectTrackedPatch(ctx, rs.sourceDir(), baseline)
		if patchErr != nil {
			return fmt.Errorf("tracked patch: %w", patchErr)
		}
		evidenceDocument.TrackedPatchSHA256 = patch.Digest()
		evidenceDocument.TrackedPatchBytes = patch.Total()
		if !patch.Truncated() {
			evidenceDocument.TrackedPatchIncluded = true
			evidenceDocument.TrackedPatch = patch.String()
		}
	}
	for _, changedPath := range changed {
		fingerprint := snapshot.Files[changedPath]
		mode := "deleted"
		if info, statErr := os.Lstat(filepath.Join(rs.sourceDir(), filepath.FromSlash(changedPath))); statErr == nil {
			mode = info.Mode().String()
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		if fingerprint == "" {
			fingerprint = "deleted"
		}
		evidenceDocument.ChangedFiles = append(evidenceDocument.ChangedFiles, candidateFile{
			Path: changedPath, Fingerprint: fingerprint, Mode: mode,
		})
	}
	for _, result := range rs.results {
		if result.Superseded {
			continue
		}
		evidenceDocument.Attempts = append(evidenceDocument.Attempts, candidateAttempt{
			AttemptID: result.AttemptID, Stage: result.Name, Outcome: string(result.State.Outcome), Verdict: string(result.Verdict),
		})
		for _, check := range result.Checks {
			evidenceDocument.Checks = append(evidenceDocument.Checks, candidateCheck{
				AttemptID: result.AttemptID, Name: check.Name, Class: check.Class, Adapter: check.Adapter,
				Command: append([]string(nil), check.Command...), ToolPath: check.ToolPath, ToolVersion: check.ToolVersion,
				Status: check.Status, ExitCode: check.ExitCode, DiscoveredTests: check.DiscoveredTests, PassedTests: check.PassedTests,
				EvidenceDigest: check.EvidenceDigest, WorkspaceDigestBefore: check.WorkspaceDigestBefore,
				WorkspaceDigestAfter: check.WorkspaceDigestAfter, StructuredSHA256: check.StructuredOutputSHA256,
			})
		}
	}
	directory, err := safeio.EnsureDir(rs.task.ArtifactRoot, rs.runCfg.Feature, ".control")
	if err != nil {
		return err
	}
	return writeControllerJSON(filepath.Join(directory, name), evidenceDocument)
}

func (rs *runState) verifyCandidateEvidence(name, purpose string) error {
	path := filepath.Join(rs.task.ArtifactRoot, rs.runCfg.Feature, ".control", name)
	data, err := safeio.ReadRegularFile(path, maxArtifactFileBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var candidate candidateEvidence
	if err := decoder.Decode(&candidate); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("candidate evidence has trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	current, err := checks.WorkspaceDigest(rs.sourceDir())
	if err != nil {
		return err
	}
	if candidate.SchemaVersion != 1 || candidate.RunID != rs.runID || candidate.Purpose != purpose || candidate.WorkspaceSHA256 != current {
		return fmt.Errorf("reviewed candidate identity changed before test authoring")
	}
	return nil
}

func syncArtifactProjection(sourceRoot, destinationRoot, feature string) error {
	source := filepath.Join(sourceRoot, feature)
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := safeio.EnsureDir(destinationRoot); err != nil {
		return err
	}
	destination := filepath.Join(destinationRoot, feature)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("artifact projection %s небезопасна", destination)
		}
		if err := os.RemoveAll(destination); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("artifact projection содержит special file %s", relative)
		}
		data, err := safeio.ReadRegularFile(path, maxArtifactFileBytes)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return nil
	})
}

// digestCapture hashes the complete stream while retaining only a bounded
// prefix. This keeps candidate identity exact without allowing a large diff to
// consume unbounded controller memory.
type digestCapture struct {
	buffer    bytes.Buffer
	hash      hash.Hash
	total     int64
	limit     int
	truncated bool
}

func newDigestCapture(limit int) *digestCapture {
	return &digestCapture{hash: sha256.New(), limit: limit}
}

func collectTrackedPatch(ctx context.Context, dir, baseline string) (*digestCapture, error) {
	stdout := newDigestCapture(maxCandidatePatchBytes)
	stderr := newDigestCapture(maxCandidateGitStderr)
	command := exec.Command("git", "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", baseline, "--")
	command.Dir = dir
	command.Stdout = stdout
	command.Stderr = stderr
	if err := process.Run(ctx, command); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%w: %s", err, message)
		}
		return nil, err
	}
	return stdout, nil
}

func writeControllerJSON(path string, value any) error {
	if err := safeio.RejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxArtifactFileBytes {
		return fmt.Errorf("controller evidence %s exceeds %d bytes", path, maxArtifactFileBytes)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".control-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (rs *runState) prepareControllerStageEvidence(ctx context.Context, stage string) error {
	definition, err := rs.p.reg.Load(stage)
	if err != nil {
		return err
	}
	for inputName, declaredPath := range definition.Inputs {
		resolved := filepath.ToSlash(runtime.ReplaceVars(declaredPath, rs.runCfg.Feature))
		switch {
		case inputName == "candidate" && strings.HasSuffix(resolved, "/.control/review-candidate.json"):
			return rs.writeCandidateEvidence(ctx, "review-candidate.json", "semantic_code_review")
		case inputName == "reviewed-candidate" && strings.HasSuffix(resolved, "/.control/review-candidate.json"):
			return rs.verifyCandidateEvidence("review-candidate.json", "semantic_code_review")
		case inputName == "candidate" && strings.HasSuffix(resolved, "/.control/verification-candidate.json"):
			return rs.writeCandidateEvidence(ctx, "verification-candidate.json", "final_verification")
		}
	}
	return nil
}

func (capture *digestCapture) Write(data []byte) (int, error) {
	original := len(data)
	capture.total += int64(original)
	_, _ = capture.hash.Write(data)
	remaining := capture.limit - capture.buffer.Len()
	if remaining <= 0 {
		capture.truncated = capture.truncated || original > 0
		return original, nil
	}
	if original > remaining {
		capture.truncated = true
		data = data[:remaining]
	}
	_, _ = capture.buffer.Write(data)
	return original, nil
}

func (capture *digestCapture) Digest() string { return fmt.Sprintf("%x", capture.hash.Sum(nil)) }

func (capture *digestCapture) String() string { return capture.buffer.String() }

func (capture *digestCapture) Total() int64 { return capture.total }

func (capture *digestCapture) Truncated() bool { return capture.truncated }
