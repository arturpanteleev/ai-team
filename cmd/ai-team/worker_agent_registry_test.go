package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/worker"
)

const workerRegistryTestEnvVar = "AI_TEAM_WORKER_REGISTRY_TEST"

func TestDisposableWorkerLoadsConfiguredAgentRegistry(t *testing.T) {
	if os.Getenv(workerRegistryTestEnvVar) != "1" {
		return
	}
	target := ""
	for index, arg := range os.Args {
		if arg == "--target" && index+1 < len(os.Args) {
			target = os.Args[index+1]
		}
	}
	job, err := worker.DecodeJob(os.Stdin, target)
	if err != nil {
		t.Fatal(err)
	}
	paths, hasSnapshot, err := worker.AgentRegistryPathsFromEnvironment()
	if err != nil || !hasSnapshot {
		t.Fatalf("worker registry snapshot missing: paths=%v present=%v err=%v", paths, hasSnapshot, err)
	}
	registry, err := newAgentRegistryWithPaths(target, paths)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := registry.Load("configured-agent")
	if err != nil {
		t.Fatal(err)
	}
	marker := os.Getenv("AI_TEAM_WORKER_REGISTRY_MARKER")
	if marker == "" {
		t.Fatal("registry marker missing")
	}
	data, err := json.Marshal(map[string]string{
		"name": configured.Name, "description": configured.Description,
		"source": configured.Source, "prompt": configured.Prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(worker.Result{
		SchemaVersion: worker.ResultSchemaVersion, RunID: job.RunID,
		Operation: job.Operation, ExecutionID: job.ExecutionID, Outcome: worker.OutcomeCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", worker.ResultPrefix, result)
}

func TestDisposableWorkerReceivesSameConfiguredAgentSourceAsPreflight(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team", "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(t.TempDir(), "configured-agents")
	if err := os.MkdirAll(filepath.Join(pluginDir, "configured-agent"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "configured-agent", "def.yaml"), []byte(
		"name: configured-agent\ndescription: configured worker agent\nruntime: agentcli\nmutation: none\ncli: opencode\nprompt_file: prompt.md\noutputs:\n  report: report.md\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "configured-agent", "prompt.md"), []byte("custom prompt body"), 0644); err != nil {
		t.Fatal(err)
	}
	pluginDir, err := filepath.Abs(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	registryPaths := []string{pluginDir}
	preflightRegistry, err := newAgentRegistryWithPaths(target, registryPaths)
	if err != nil {
		t.Fatal(err)
	}
	preflightAgent, err := preflightRegistry.Load("configured-agent")
	if err != nil {
		t.Fatalf("controller preflight cannot load configured agent: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "worker-agent.json")
	t.Setenv(workerRegistryTestEnvVar, "1")
	t.Setenv("AI_TEAM_WORKER_REGISTRY_MARKER", marker)
	allowed := []string{workerRegistryTestEnvVar, "AI_TEAM_WORKER_REGISTRY_MARKER"}
	t.Setenv(worker.WorkerEnvAllowVar, strings.Join(allowed, ","))
	engine, err := worker.NewProcessEngine(
		[]string{os.Args[0], "-test.run=^TestDisposableWorkerLoadsConfiguredAgentRegistry$", "--"},
		target, filepath.Join(target, ".ai-team", "web.db"), worker.WithAgentRegistryPaths(registryPaths),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(context.Background(), worker.Job{
		SchemaVersion: worker.SchemaVersion, Operation: worker.OperationStart,
		RunID: "registry-run", TargetDir: target, Feature: "registry", Task: "load configured agent",
	}); err != nil {
		t.Fatalf("disposable worker could not load the configured agent: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var workerAgent map[string]string
	if err := json.Unmarshal(data, &workerAgent); err != nil {
		t.Fatal(err)
	}
	if workerAgent["name"] != preflightAgent.Name || workerAgent["description"] != preflightAgent.Description ||
		workerAgent["source"] != preflightAgent.Source || workerAgent["prompt"] != preflightAgent.Prompt {
		t.Fatalf("preflight and worker loaded different agent definitions: preflight=%+v worker=%v", preflightAgent, workerAgent)
	}
}
