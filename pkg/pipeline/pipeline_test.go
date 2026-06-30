package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

func TestNewPipeline(t *testing.T) {
	cfg := &config.Config{
		PipelineAgents: []config.AgentConfig{
			{Name: "mock-agent"},
		},
		CLI: "opencode",
	}
	r := agent.NewRegistry("../../e2etest/pipeline-test/agents")
	p := New(cfg, r)

	if len(p.Agents()) != 1 {
		t.Errorf("expected 1 agent, got %d", len(p.Agents()))
	}
}

func TestRun_CancelledContext(t *testing.T) {
	dir := t.TempDir()

	artifactsDir := filepath.Join(dir, ".ai-team", "artifacts")
	os.MkdirAll(artifactsDir, 0755)

	cfg := &config.Config{
		PipelineAgents: []config.AgentConfig{
			{Name: "mock-agent"},
			{Name: "mock-agent"},
		},
		CLI: "opencode",
	}
	r := agent.NewRegistry("../../e2etest/pipeline-test/agents")
	p := New(cfg, r)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Run(ctx, RunConfig{
		Feature:   "test",
		TaskDesc:  "test",
		TargetDir: dir,
	})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
