//go:build linux

package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
)

func TestBubblewrapBuilderRejectsQuestionAnswerMountOutsideRunIdentity(t *testing.T) {
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "bwrap"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	target := makeBubblewrapTarget(t)
	const runID, approvalID = "question-mount-builder-run", "question-mount-builder-approval"
	value := workerAnalystQuestionApproval(runID, approvalID, "Which buyer?", "B2B buyers", approval.StatusResolved)
	source, err := (pipeline.ControllerQuestionAnswerStore{TargetDir: target}).Prepare(value)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := pipeline.QuestionAnswerMaterializationPath(target, runID, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	mount := workerReadOnlyInputMount{SourcePath: source, TargetPath: destination + ".other", TargetInfo: info}
	_, err = bubblewrapWorkerCommandWithInputs(
		context.Background(), exec.Command("worker"), target, filepath.Join(target, "controller.db"), runID,
		nil, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}, []workerReadOnlyInputMount{mount},
	)
	if err == nil || !strings.Contains(err.Error(), "validate worker clarification input mount") {
		t.Fatalf("bubblewrap accepted a clarification path outside the run-scoped identity: %v", err)
	}
}
