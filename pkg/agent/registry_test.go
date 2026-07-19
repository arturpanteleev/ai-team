package agent

import (
	"testing"
)

func TestRegistry_Load(t *testing.T) {
	r := NewRegistry("../../e2etest/agents")
	a, err := r.Load("test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "test-agent" {
		t.Errorf("expected test-agent, got %s", a.Name)
	}
	if a.RuntimeType != "agentcli" {
		t.Errorf("expected agentcli, got %s", a.RuntimeType)
	}
	if a.Prompt == "" {
		t.Error("expected prompt to be loaded")
	}
}

func TestRegistry_Load_NotFound(t *testing.T) {
	r := NewRegistry("../../e2etest/agents")
	_, err := r.Load("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestRegistry_DefaultPipeline(t *testing.T) {
	r := NewRegistry("../../e2etest/agents")
	p := r.DefaultPipeline()
	if len(p) != 7 {
		t.Errorf("expected 7 agents, got %d", len(p))
	}
}
