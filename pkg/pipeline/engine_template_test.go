package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

func TestRunEngineUsesPinnedTemplateForContinuationAndCurrentForNewTask(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.Mkdir(controlDir, 0755); err != nil {
		t.Fatal(err)
	}
	initial, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	initialYAML, err := initial.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), initialYAML, 0644); err != nil {
		t.Fatal(err)
	}
	engine := NewRunEngine(New(initial, agent.NewFS(os.DirFS("../../agents"))))
	firstRun, err := engine.pipelineForTask("task-first-run", target, true)
	if err != nil {
		t.Fatal(err)
	}
	if firstRun.cfg.Title != initial.Title {
		t.Fatalf("first task title=%q want %q", firstRun.cfg.Title, initial.Title)
	}

	updated := *initial
	updated.Title = "New active template"
	updatedYAML, err := updated.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(updatedYAML, config.TemplateVersionID(initialYAML)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.PinCurrentForRun("task-first-recovery", "task-first-run"); err != nil {
		t.Fatal(err)
	}
	continuation, err := engine.pipelineForTask("task-first-recovery", target, true)
	if err != nil {
		t.Fatal(err)
	}
	if continuation.cfg.Title != initial.Title {
		t.Fatalf("continuation switched to new template %q", continuation.cfg.Title)
	}
	newTask, err := engine.pipelineForTask("task-second-run", target, true)
	if err != nil {
		t.Fatal(err)
	}
	if newTask.cfg.Title != updated.Title {
		t.Fatalf("new task title=%q want %q", newTask.cfg.Title, updated.Title)
	}
}

func TestRunEnginePreservesLegacyExecutionOverrideButPinsDeliveryRecovery(t *testing.T) {
	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.Mkdir(controlDir, 0755); err != nil {
		t.Fatal(err)
	}
	published, err := config.DefaultProfile(config.ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	publishedYAML, err := published.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), publishedYAML, 0644); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "legacy-override-recovery"
	if _, err := store.PinCurrentForRun(runID, runID); err != nil {
		t.Fatal(err)
	}

	legacyOverride := config.Default()
	engine := NewRunEngine(New(legacyOverride, nil))
	resumed, err := engine.pipelineForTask(runID, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.cfg.Template != "" || resumed.cfg.Workflow == nil {
		t.Fatalf("Resume replaced explicit in-memory legacy config with template %q", resumed.cfg.Template)
	}
	pinnedDelivery, err := engine.pipelineForPinnedDelivery(runID, target)
	if err != nil {
		t.Fatal(err)
	}
	if pinnedDelivery.cfg.Template != published.Template || pinnedDelivery.cfg.DeliveryTimeout != published.DeliveryTimeout {
		t.Fatalf("delivery recovery did not resolve pinned template: got template=%q timeout=%q, want template=%q timeout=%q",
			pinnedDelivery.cfg.Template, pinnedDelivery.cfg.DeliveryTimeout, published.Template, published.DeliveryTimeout)
	}
}
