package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

const untrustedRunActiveYAML = "schema_version: 6\ntemplate: t\ntitle: T\nstages:\n  - {id: spec, title: Spec, function: po, result: md, executor: human}\n"

// A template published through the web before the trusted-config rule, with a
// host-command gate.
const untrustedRunOldYAML = `schema_version: 6
template: t
title: Old
stages:
  - id: spec
    title: Spec
    function: po
    result: md
    executor: human
    exit_gate_groups:
      - - gate: external_program
          version: 1
          name: evil
          params: {command: ["sh", "-c", "id"], timeout_seconds: 30}
`

func untrustedRunTarget(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(untrustedRunActiveYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	// An old published version, pinned to a run.
	_, version, _ := store.ReadCurrent()
	if _, err := store.Publish([]byte(untrustedRunOldYAML), version); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish([]byte(untrustedRunActiveYAML), config.TemplateVersionID([]byte(untrustedRunOldYAML))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinDataForRun("run-old", "run-old", []byte(untrustedRunOldYAML)); err != nil {
		t.Fatal(err)
	}
	return target
}

func requireTrustedRefusal(t *testing.T, path string, err error) {
	t.Helper()
	var refused *config.TrustedConfigError
	if !errors.As(err, &refused) || refused.Code() != config.ErrCodeTrustedConfigOnly {
		t.Errorf("%s: want a trusted_config_only refusal, got %v", path, err)
	}
}

// An old published template with a command gate does not run by any path:
// CLI resume, the engine (Start/Resume/SkipStage/delivery recovery), the web
// controller (REST and MCP resume/skip) and the worker process.
func TestOldCommandGateTemplateRunsByNoPath(t *testing.T) {
	target := untrustedRunTarget(t)
	reg := agent.NewFS(os.DirFS("../../agents"))

	// ai-team run --resume
	if _, _, err := loadPinnedResumeConfig(target, "run-old", nil); err == nil {
		t.Error("loadPinnedResumeConfig: the pinned command-gate template was loaded")
	} else {
		requireTrustedRefusal(t, "ai-team run --resume", err)
	}

	// The engine behind every launch path.
	active, err := config.ParseYAML([]byte(untrustedRunActiveYAML))
	if err != nil {
		t.Fatal(err)
	}
	engine := pipeline.NewRunEngine(pipeline.New(active, reg))
	_, err = engine.Resume(context.Background(), pipeline.ResumeConfig{RunID: "run-old", TargetDir: target})
	requireTrustedRefusal(t, "RunEngine.Resume", err)
	_, err = engine.SkipStage(context.Background(), pipeline.SkipStageConfig{RunID: "run-old", TargetDir: target, StageID: "spec", Reason: "x"})
	requireTrustedRefusal(t, "RunEngine.SkipStage", err)
	_, err = engine.DeliverDeferred(context.Background(), "run-old", target)
	requireTrustedRefusal(t, "RunEngine.DeliverDeferred", err)

	// The web controller: REST and MCP resume/skip/run-agent all end here.
	controller, err := control.New(engine, target)
	if err != nil {
		t.Fatal(err)
	}
	requireTrustedRefusal(t, "Controller.Resume", controller.Resume("run-old"))
	requireTrustedRefusal(t, "Controller.ResumeWithRuntimeUpgrade", controller.ResumeWithRuntimeUpgrade("run-old"))
	requireTrustedRefusal(t, "Controller.SkipStage", controller.SkipStage(context.Background(), "run-old", "spec", "x", "actor", "po"))

	// The worker process (web/MCP launches) refuses before it builds a pipeline.
	command := exec.Command(os.Args[0], "-test.run=^TestUntrustedWorkerTemplateHelper$")
	command.Env = append(os.Environ(), "AI_TEAM_UNTRUSTED_WORKER_HELPER=1", "AI_TEAM_UNTRUSTED_TARGET="+target)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("the worker must exit non-zero for a command-gate pin:\n%s", output)
	}
	if !strings.Contains(string(output), config.ErrCodeTrustedConfigOnly) {
		t.Fatalf("the worker refusal must carry the code: %s", output)
	}
}

func TestUntrustedWorkerTemplateHelper(t *testing.T) {
	if os.Getenv("AI_TEAM_UNTRUSTED_WORKER_HELPER") != "1" {
		return
	}
	loadWorkerTemplateConfig(os.Getenv("AI_TEAM_UNTRUSTED_TARGET"),
		worker.Job{Operation: worker.OperationResume, RunID: "run-old"}, nil)
}

// The active template and a process-only difference still run.
func TestActiveAndProcessOnlyPinsStillRun(t *testing.T) {
	target := untrustedRunTarget(t)
	store, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(untrustedRunActiveYAML, "title: T", "title: Renamed", 1)
	if _, err := store.PinDataForRun("run-renamed", "run-renamed", []byte(renamed)); err != nil {
		t.Fatal(err)
	}
	reg := agent.NewFS(os.DirFS("../../agents"))
	cfg, found, err := loadPinnedResumeConfig(target, "run-renamed", reg)
	if err != nil || !found || cfg.Title != "Renamed" {
		t.Fatalf("a process-only pin must load: %v %v", found, err)
	}
}
