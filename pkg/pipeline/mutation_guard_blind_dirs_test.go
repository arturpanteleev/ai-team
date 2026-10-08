package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestMutationGuardRejectsDistMutationForReadOnlyStage(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	flag := filepath.Join(target, "dist", "flag")
	if err := os.WriteFile(flag, []byte("checked"), 0644); err != nil {
		t.Fatal(err)
	}
	beforeDigest, err := checks.WorkspaceDigest(target)
	if err != nil {
		t.Fatal(err)
	}

	rs := &runState{
		runCfg: RunConfig{TargetDir: target},
		task:   &runtime.Task{ArtifactRoot: filepath.Join(target, "artifacts")},
	}
	workspaceBefore, err := captureWorkspaceSnapshot(target)
	if err != nil {
		t.Fatal(err)
	}
	artifactBefore, err := captureArtifactSnapshot(filepath.Join(target, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	afterDigest, err := checks.WorkspaceDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	if beforeDigest == afterDigest {
		t.Fatal("workspace digest must change with dist/flag")
	}

	var result notifier.StageResult
	err = rs.enforceMutationGuard(&agent.Agent{Mutation: "none"}, "verifier", workspaceBefore,
		gitMetadataSnapshot{}, false, true, artifactBefore, &result)
	if err == nil {
		t.Fatal("read-only verifier mutation in dist/flag must fail the mutation guard")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only mutation error, got %v", err)
	}
	if len(result.Mutations) != 1 || result.Mutations[0] != "dist/flag" {
		t.Fatalf("mutation attribution missed dist/flag: %v", result.Mutations)
	}
}
