package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestArtifactMutationAllowedUsesStageQuestionPolicy(t *testing.T) {
	artifactRoot := t.TempDir()
	outputDir := filepath.Join(artifactRoot, "feat", "results")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rs := &runState{
		runCfg: RunConfig{Feature: "feat"},
		task:   &runtime.Task{ArtifactRoot: artifactRoot},
	}
	stage := &agent.Agent{
		Name: "questioner", AskQuestions: true,
		Outputs: map[string]string{"report": "{feature}/report.md", "results": "{feature}/results"},
	}
	cases := []struct {
		name     string
		agent    *agent.Agent
		path     string
		wantOkay bool
	}{
		{name: "stage status", agent: stage, path: "feat/status/questioner.md", wantOkay: true},
		{name: "stage summary", agent: stage, path: "feat/.stage-summary/questioner.md", wantOkay: true},
		{name: "questions enabled by stage", agent: stage, path: "tasks/feat/questions.md", wantOkay: true},
		{name: "declared output", agent: stage, path: "feat/report.md", wantOkay: true},
		{name: "declared output directory child", agent: stage, path: "feat/results/summary.md", wantOkay: true},
		{name: "unknown path", agent: stage, path: "feat/other.md", wantOkay: false},
		{name: "questions disabled", agent: &agent.Agent{Name: "reader"}, path: "tasks/feat/questions.md", wantOkay: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rs.artifactMutationAllowed(tc.agent, tc.agent.Name, tc.path); got != tc.wantOkay {
				t.Fatalf("artifactMutationAllowed(%q) = %v, want %v", tc.path, got, tc.wantOkay)
			}
		})
	}
}
