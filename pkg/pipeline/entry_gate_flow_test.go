package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// entry_gate_flow_test.go — the entry chain is wired into the stage executor:
// a failed required entry gate blocks StageExecutor.Execute, a passing one calls
// it exactly once, and the attempts carry phase entry (finding 1).

// entryGateConfig builds a schema-v7 config whose single agent stage checks its
// md input with a required_phrases entry gate and runs through the "demo"
// external adapter.
func entryGateConfig(t *testing.T, registry *agent.Registry, phrases []string) *config.Config {
	t.Helper()
	cfg := &config.Config{
		SchemaVersion:  config.WorkflowSchemaVersion,
		Template:       "entry-gate-test",
		Title:          "Entry gate test",
		PipelineAgents: []config.AgentConfig{{Name: "tech"}},
		Stages: []config.TemplateStage{{
			ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md",
			Executor: "agent", Agent: "tech", Adapter: "demo", Confirm: "auto",
			InputArtifacts: []workflow.ArtifactType{workflow.ArtifactTypeMD},
			EntryGateGroups: workflow.GateGroups{{
				codeGate(gatelib.GateRequiredPhrases, map[string]any{
					"phrases": phrases, "case": "insensitive", "scope": "result", "match": "all",
				}),
			}},
		}},
		ExecutorAdapters: map[string]stageexec.AdapterSpec{
			"demo": {Argv: []string{"./scripts/executor.sh"}, Timeout: "30s"},
		},
		CLI: "opencode",
	}
	if err := cfg.Validate(registry); err != nil {
		t.Fatalf("entry gate config must validate: %v", err)
	}
	return cfg
}

// fakeEntryExecutor counts Execute calls and writes the declared outputs so a
// passing run can finish.
type fakeEntryExecutor struct {
	calls int
}

func (f *fakeEntryExecutor) Identity() stageexec.Identity {
	return stageexec.Identity{Kind: stageexec.KindExternal, Name: "fake"}
}

func (f *fakeEntryExecutor) Execute(_ context.Context, req stageexec.Request) (stageexec.Result, error) {
	f.calls++
	for _, out := range req.Outputs {
		if err := os.MkdirAll(filepath.Dir(out.Path), 0o755); err != nil {
			return stageexec.Result{Identity: f.Identity()}, err
		}
		if err := os.WriteFile(out.Path, []byte("# Tech\n\nрешение\n"), 0o644); err != nil {
			return stageexec.Result{Identity: f.Identity()}, err
		}
	}
	return stageexec.Result{Identity: f.Identity()}, nil
}

func runEntryGateFlow(t *testing.T, dir string, phrases []string) (*fakeEntryExecutor, *captureNotifier, error) {
	t.Helper()
	registry := externalExecutorRegistry()
	fake := &fakeEntryExecutor{}
	capture := &captureNotifier{}
	p := New(entryGateConfig(t, registry, phrases), registry,
		WithExternalExecutorFactory(func(string, stageexec.AdapterSpec) (stageexec.StageExecutor, error) {
			return fake, nil
		}),
		WithNotifier(capture),
		WithPrompter(&scriptedPrompter{}),
	)
	// The pipeline writes the task description into task.md, which is the md
	// input the entry gate checks.
	err := p.Run(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "tech spec with a REQUIRED PHRASE", TargetDir: dir, ApproveGates: true,
	})
	return fake, capture, err
}

func TestEntryGateBlocksExternalExecutorWhenRequiredGateFails(t *testing.T) {
	dir := env(t)
	fake, capture, err := runEntryGateFlow(t, dir, []string{"an absent phrase"})
	if err == nil {
		t.Fatalf("a failed required entry gate must block the run: %+v", capture.calls)
	}
	if fake.calls != 0 {
		t.Fatalf("executor must not be called when a required entry gate fails: calls=%d", fake.calls)
	}
	attempts, err := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	if err != nil || len(attempts) == 0 {
		t.Fatalf("the failed entry attempt must be recorded: %v %v", attempts, err)
	}
	for _, attempt := range attempts {
		if attempt.Phase != workflow.GatePhaseEntry {
			t.Fatalf("entry attempt must carry phase entry: %+v", attempt)
		}
	}
}

func TestEntryGateAllowsExternalExecutorOnceWhenPassing(t *testing.T) {
	dir := env(t)
	fake, capture, err := runEntryGateFlow(t, dir, []string{"REQUIRED PHRASE"})
	if err != nil {
		t.Fatalf("a passing entry gate must allow the run: %v %+v", err, capture.calls)
	}
	if fake.calls != 1 {
		t.Fatalf("executor must be called exactly once when the entry gate passes: calls=%d", fake.calls)
	}
}
