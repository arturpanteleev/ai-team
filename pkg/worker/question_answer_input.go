package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type workerReadOnlyInputMount struct {
	SourcePath string
	TargetPath string
	TargetInfo os.FileInfo
}

func validateQuestionAnswerCandidate(ctx context.Context, target string, value approval.PendingApproval) error {
	if value.CandidateSHA256 == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager, err := candidate.Load(ctx, target, value.RunID)
	if err != nil {
		return fmt.Errorf("load clarification candidate identity: %w", err)
	}
	identity, err := manager.Identity()
	if err != nil {
		return fmt.Errorf("read clarification candidate identity: %w", err)
	}
	if identity.RunID != value.RunID || identity.WorkspaceSHA256 != value.CandidateSHA256 {
		return errors.New("clarification approval candidate identity changed after decision")
	}
	return nil
}

type workerAPIQuestionAnswerInputs struct{ port *workerAPIPort }

// NewWorkerAPIQuestionAnswerInputs exposes a typed, identity-only clarification
// input request. The controller returns a path it has mounted read-only; no
// answer bytes or approval object cross from worker to controller.
func NewWorkerAPIQuestionAnswerInputs(port *WorkerAPIPort) pipeline.QuestionAnswerInputProvider {
	return &workerAPIQuestionAnswerInputs{port: port}
}

func (p *workerAPIQuestionAnswerInputs) MaterializeQuestionAnswer(runID, approvalID string) (runtime.Artifact, error) {
	if p == nil || p.port == nil || !p.port.SupportsControllerUsageStore() {
		return runtime.Artifact{}, errors.New("controller clarification input API requires a sandboxed Unix worker")
	}
	if runID != p.port.scope.RunID {
		return runtime.Artifact{}, errors.New("worker clarification input run mismatch")
	}
	expected, err := pipeline.QuestionAnswerMaterializationPath(p.port.scope.TargetDir, runID, approvalID)
	if err != nil {
		return runtime.Artifact{}, err
	}
	var preparedPath string
	if err := p.port.call("handoff.question_answer.path", workerAPICall{RunID: runID, A: approvalID}, &preparedPath); err != nil {
		return runtime.Artifact{}, err
	}
	if preparedPath != expected {
		return runtime.Artifact{}, errors.New("controller returned an unexpected clarification input path")
	}
	info, err := os.Lstat(preparedPath)
	if err != nil {
		return runtime.Artifact{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
		return runtime.Artifact{}, errors.New("controller clarification input is not a read-only regular file")
	}
	return runtime.Artifact{Name: "clarification-answer", Path: preparedPath, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func validateQuestionAnswerMount(target, runID string, mount workerReadOnlyInputMount) error {
	if mount.SourcePath == "" || mount.TargetPath == "" {
		return errors.New("clarification input mount paths are required")
	}
	approvalID := filepath.Base(filepath.Dir(mount.SourcePath))
	wantSource, err := pipeline.QuestionAnswerCanonicalPath(target, runID, approvalID)
	if err != nil {
		return err
	}
	wantTarget, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		return err
	}
	if mount.SourcePath != wantSource || mount.TargetPath != wantTarget {
		return errors.New("clarification input mount does not match its run-scoped identity")
	}
	if _, err := safeio.ExistingDir(target, ".ai-team", "state", "handoff-inputs", runID, approvalID); err != nil {
		return fmt.Errorf("validate canonical clarification input parents: %w", err)
	}
	if _, err := safeio.ExistingDir(target, ".ai-team", "runs", runID, "inputs"); err != nil {
		return fmt.Errorf("validate clarification input mountpoint parents: %w", err)
	}
	source, sourceInfo, err := readQuestionAnswerMountFile(mount.SourcePath, maxQuestionAnswerMountBytes)
	if err != nil {
		return fmt.Errorf("read canonical clarification input safely: %w", err)
	}
	if sourceInfo.Mode().Perm()&0o222 != 0 || sourceInfo.Size() <= 0 {
		return fmt.Errorf("clarification input source %q must be a nonempty read-only regular file", mount.SourcePath)
	}
	targetData, targetInfo, err := readQuestionAnswerMountFile(mount.TargetPath, maxQuestionAnswerMountBytes)
	if err != nil {
		return err
	}
	if mount.TargetInfo != nil && !os.SameFile(mount.TargetInfo, targetInfo) {
		return errors.New("clarification input mountpoint identity changed after admission")
	}
	if len(targetData) != 0 && (!bytes.Equal(targetData, source) || targetInfo.Mode().Perm()&0o222 != 0) {
		return fmt.Errorf("clarification input target %q is neither empty nor an exact read-only canonical overlay", mount.TargetPath)
	}
	return nil
}

const maxQuestionAnswerMountBytes = 1 << 16

// readQuestionAnswerMountFile opens an input without following a leaf symlink,
// pins its identity across the read, and rejects multiply linked files. The
// target lives in a worker-writable tree, so checking only its bytes after a
// normal os.Open would permit a symlink swap or hardlink alias.
func readQuestionAnswerMountFile(path string, maxBytes int64) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() > maxBytes {
		return nil, nil, errors.New("clarification input path must be a bounded regular file without symlink")
	}
	file, err := openQuestionAnswerMountFileNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, nil, errors.New("clarification input identity changed during open")
	}
	links, err := questionAnswerMountFileLinkCount(file)
	if err != nil {
		return nil, nil, err
	}
	if links != 1 {
		return nil, nil, errors.New("clarification input must have exactly one filesystem link")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, errors.New("clarification input exceeds its read limit")
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	pathAfter, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(opened, openedAfter) || !os.SameFile(opened, pathAfter) ||
		opened.Size() != openedAfter.Size() || !opened.ModTime().Equal(openedAfter.ModTime()) {
		return nil, nil, errors.New("clarification input identity changed during read")
	}
	links, err = questionAnswerMountFileLinkCount(file)
	if err != nil {
		return nil, nil, err
	}
	if links != 1 {
		return nil, nil, errors.New("clarification input acquired another filesystem link during read")
	}
	return data, openedAfter, nil
}
