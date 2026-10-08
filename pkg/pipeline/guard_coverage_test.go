package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestArtifactMutationAllowedDeclaredAndStagePaths(t *testing.T) {
	root := t.TempDir()
	rs := &runState{
		runCfg: RunConfig{Feature: "demo"},
		task:   &runtime.Task{ArtifactRoot: root},
	}

	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		agentName   string
		outputs     map[string]string
		relative    string
		wantAllowed bool
	}{
		{
			name:        "stage status",
			agentName:   "coder",
			relative:    "demo/status/coder.md",
			wantAllowed: true,
		},
		{
			name:        "stage summary",
			agentName:   "coder",
			relative:    "demo/.stage-summary/coder.md",
			wantAllowed: true,
		},
		{
			name:        "declared output file",
			agentName:   "coder",
			outputs:     map[string]string{"report": "reports/final.md"},
			relative:    "reports/final.md",
			wantAllowed: true,
		},
		{
			name:        "child of declared output directory",
			agentName:   "coder",
			outputs:     map[string]string{"reports": "reports"},
			relative:    "reports/final.md",
			wantAllowed: true,
		},
		{
			name:      "undeclared path",
			agentName: "coder",
			outputs:   map[string]string{"report": "reports/final.md"},
			relative:  "private/notes.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &agent.Agent{Outputs: tt.outputs}
			if got := rs.artifactMutationAllowed(a, tt.agentName, tt.relative); got != tt.wantAllowed {
				t.Fatalf("artifactMutationAllowed(%q) = %t, want %t", tt.relative, got, tt.wantAllowed)
			}
		})
	}
}
