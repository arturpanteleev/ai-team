package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
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

func TestNewPipeline_DefaultConfig(t *testing.T) {
	p := New(nil, nil)
	if p.cfg == nil {
		t.Error("expected default config, got nil")
	}
	if p.notifier == nil {
		t.Error("expected default notifier, got nil")
	}
}

func TestNewPipeline_WithNotifier(t *testing.T) {
	cfg := &config.Config{
		PipelineAgents: []config.AgentConfig{{Name: "a"}},
		CLI:            "opencode",
	}
	custom := &mockNotifier{}
	p := New(cfg, nil, WithNotifier(custom), WithReportsDir("/tmp/reports"))

	if p.notifier != custom {
		t.Error("expected custom notifier")
	}
	if p.reportsDir != "/tmp/reports" {
		t.Errorf("expected /tmp/reports, got %s", p.reportsDir)
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

func TestFindGates(t *testing.T) {
	agents := []*runtime.Agent{
		{Name: "analyst", GateAfter: true},
		{Name: "architect", GateAfter: true},
		{Name: "coder"},
		{Name: "deployer", GateBefore: true},
	}

	gates := findGates(agents)

	if len(gates) != 3 {
		t.Fatalf("expected 3 gates, got %d", len(gates))
	}

	if gates[0].AgentName != "analyst" || gates[0].Type != GateAfter {
		t.Errorf("expected gate after analyst, got %+v", gates[0])
	}
	if gates[1].AgentName != "architect" || gates[1].Type != GateAfter {
		t.Errorf("expected gate after architect, got %+v", gates[1])
	}
	if gates[2].AgentName != "deployer" || gates[2].Type != GateBefore {
		t.Errorf("expected gate before deployer, got %+v", gates[2])
	}
}

func TestFindGates_Empty(t *testing.T) {
	gates := findGates([]*runtime.Agent{})
	if len(gates) != 0 {
		t.Errorf("expected 0 gates, got %d", len(gates))
	}
}

func TestFindGates_None(t *testing.T) {
	agents := []*runtime.Agent{
		{Name: "a", GateAfter: false, GateBefore: false},
		{Name: "b"},
	}
	gates := findGates(agents)
	if len(gates) != 0 {
		t.Errorf("expected 0 gates, got %d", len(gates))
	}
}

func TestHasGate(t *testing.T) {
	gates := []PipelineGate{
		{AgentName: "analyst", Type: GateAfter},
		{AgentName: "deployer", Type: GateBefore},
	}

	if !hasGate(gates, "analyst", GateAfter) {
		t.Error("expected hasGate for analyst after")
	}
	if hasGate(gates, "analyst", GateBefore) {
		t.Error("expected no gate before analyst")
	}
	if !hasGate(gates, "deployer", GateBefore) {
		t.Error("expected hasGate for deployer before")
	}
	if hasGate(gates, "coder", GateAfter) {
		t.Error("expected no gate for coder")
	}
}

func TestHasGate_Empty(t *testing.T) {
	if hasGate(nil, "analyst", GateAfter) {
		t.Error("expected no gate with nil gates")
	}
	if hasGate([]PipelineGate{}, "analyst", GateAfter) {
		t.Error("expected no gate with empty gates")
	}
}

func TestReadVerdict(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{"approved", "**Verdict:** APPROVED", "APPROVED"},
		{"changes_requested", "**Verdict:** CHANGES_REQUESTED", "CHANGES_REQ"},
		{"rejected", "**Verdict:** REJECTED", "REJECTED"},
		{"pass", "**Result:** PASS", "PASS"},
		{"fail", "**Result:** FAIL", "FAIL"},
		{"empty", "", ""},
		{"no_verdict", "some random content", ""},
		{"partial_match", "Verdict: APPROVED", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "verdict.md")
			os.WriteFile(f, []byte(tt.content), 0644)
			got := readVerdict(f)
			if got != tt.expected {
				t.Errorf("readVerdict() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestReadVerdict_NonExistentFile(t *testing.T) {
	got := readVerdict("/nonexistent/path.md")
	if got != "" {
		t.Errorf("expected empty string for nonexistent file, got %q", got)
	}
}

func TestShortenError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"output_not_created",
			fmt.Errorf("агент coder: выход design.md (/path) не создан"),
			"выход design.md не создан"},
		{"input_not_found",
			fmt.Errorf("агент architect: вход design.md (/path) не найден"),
			"вход design.md не найден"},
		{"execution_error",
			fmt.Errorf("some details завершился с ошибкой"),
			"ошибка выполнения"},
		{"load_error",
			fmt.Errorf("ошибка загрузки агента"),
			"агент не загружен"},
		{"short_error",
			fmt.Errorf("short"),
			"short"},
		{"long_error",
			fmt.Errorf("this is a very long error message that exceeds fifty characters"),
			"this is a very long error message that exceeds fif..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenError(tt.err)
			if got != tt.expected {
				t.Errorf("shortenError() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRetryFrom_InvalidAgent(t *testing.T) {
	cfg := &config.Config{
		PipelineAgents: []config.AgentConfig{
			{Name: "analyst"},
		},
		CLI: "opencode",
	}
	p := New(cfg, nil)

	ctx := context.Background()
	err := p.Run(ctx, RunConfig{
		Feature:   "test",
		TargetDir: t.TempDir(),
		RetryFrom: "nonexistent",
	})
	if err == nil {
		t.Error("expected error for nonexistent retry agent")
	}
}

type mockNotifier struct {
	calls []notifier.StageResult
}

func (m *mockNotifier) Notify(ctx context.Context, stage notifier.StageResult) error {
	m.calls = append(m.calls, stage)
	return nil
}
