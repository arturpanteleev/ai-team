package pipeline

import (
	"context"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestNewPipeline(t *testing.T) {
	r := agent.NewRegistry("../../testdata/pipeline-test/agents")
	rt, _ := runtime.NewRuntime("agentcli")
	p := New([]string{"mock-agent"}, rt, r)

	if len(p.Agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(p.Agents))
	}
}

func TestRun_CancelledContext(t *testing.T) {
	r := agent.NewRegistry("../../testdata/pipeline-test/agents")
	rt, _ := runtime.NewRuntime("agentcli")
	p := New([]string{"mock-agent"}, rt, r)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	task := &runtime.Task{
		Feature:  "test",
		TaskDesc: "test",
	}

	err := p.Run(ctx, task)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
