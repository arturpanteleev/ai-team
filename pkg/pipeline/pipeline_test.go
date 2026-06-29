package pipeline

import (
	"context"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestNewPipeline(t *testing.T) {
	r := agent.NewRegistry("../../e2etest/pipeline-test/agents")
	p := New([]string{"mock-agent"}, r)

	if len(p.Agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(p.Agents))
	}
}

func TestRun_CancelledContext(t *testing.T) {
	r := agent.NewRegistry("../../e2etest/pipeline-test/agents")
	p := New([]string{"mock-agent"}, r)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	task := &runtime.Task{
		Feature:      "test",
		TaskDesc:     "test",
		ArtifactRoot: t.TempDir(),
	}

	err := p.Run(ctx, task)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
