package e2etest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

func TestE2E_HumanExecutorInputFlowThroughCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
	dir := t.TempDir()
	bin := buildBinary(t)
	pathEnv := setupMock(t)
	if code, out := runAI(t, bin, dir, []string{pathEnv}, "init"); code != 0 {
		t.Fatalf("init failed (%d):\n%s", code, out)
	}
	configYAML := `schema_version: 5
template: human-input-e2e
title: Human input E2E
stages:
  - id: intent
    title: Intent
    function: business_owner
    result: approve
    executor: human
    confirm: auto
  - id: product_spec
    title: Product specification
    function: product_owner
    result: md
    executor: human
    agent: analyst
    required_sections:
      - Acceptance criteria
    confirm: required
  - id: implementation
    title: Implementation
    function: developer
    result: link
    link_kind: pr
    executor: human
    agent: human-implementer
    confirm: auto
`
	if err := os.WriteFile(filepath.Join(dir, ".ai-team", "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}
	customAgentDir := filepath.Join(dir, ".ai-team", "agents", "human-implementer")
	if err := os.MkdirAll(customAgentDir, 0755); err != nil {
		t.Fatal(err)
	}
	definition := `name: human-implementer
description: Human implementation result contract
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  spec: '{feature}/specs/product/spec.md'
outputs: {}
`
	if err := os.WriteFile(filepath.Join(customAgentDir, "def.yaml"), []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customAgentDir, "prompt.md"), []byte("Human result contract fixture."), 0644); err != nil {
		t.Fatal(err)
	}

	code, out := runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir,
		"--feature", "human-flow", "--task", "Build the approved feature", "--approve-gates")
	if code != 3 {
		t.Fatalf("run should wait for intent input (exit 3), got %d:\n%s", code, out)
	}
	runID, pending := readHumanPendingApproval(t, dir)
	if pending.Kind != approval.KindInput || pending.FromStage != "intent" || pending.Trigger != "human_input" {
		t.Fatalf("first wait is not intent input approval: %+v", pending)
	}
	initialEvents, err := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"))
	if err != nil || strings.Contains(string(initialEvents), `"type":"attempt_started"`) {
		t.Fatalf("waiting human input must not leave an unfinished attempt: err=%v\n%s", err, initialEvents)
	}
	decideHumanInput(t, bin, dir, pathEnv, runID, pending, "alice", "business_owner", "approve", "Approved intent")

	code, out = runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir, "--resume", runID)
	if code != 3 {
		t.Fatalf("resume should wait for product_spec markdown (exit 3), got %d:\n%s", code, out)
	}
	runID, pending = readHumanPendingApproval(t, dir)
	if pending.Kind != approval.KindInput || pending.FromStage != "product_spec" {
		t.Fatalf("second wait is not product_spec input approval: %+v", pending)
	}
	markdown := "# Product specification\n\n## Acceptance criteria\n\n- The feature is ready for implementation.\n"
	decideHumanInput(t, bin, dir, pathEnv, runID, pending, "bob", "product_owner", "submit", markdown)
	decideHumanInput(t, bin, dir, pathEnv, runID, pending, "bob", "product_owner", "submit", markdown)

	code, out = runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir, "--resume", runID)
	if code != 3 {
		t.Fatalf("product spec must stop at its independent graph confirmation (exit 3), got %d:\n%s", code, out)
	}
	runID, pending = readHumanPendingApproval(t, dir)
	if pending.Kind != approval.KindApprove || pending.FromStage != "product_spec" || pending.Trigger != "graph_outcome:passed" {
		t.Fatalf("product spec graph confirmation missing: %+v", pending)
	}
	decideHumanInput(t, bin, dir, pathEnv, runID, pending, "carol", "product_owner", "approve", "Specification accepted")

	code, out = runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir, "--resume", runID)
	if code != 3 {
		t.Fatalf("resume should wait for implementation link (exit 3), got %d:\n%s", code, out)
	}
	runID, pending = readHumanPendingApproval(t, dir)
	if pending.Kind != approval.KindInput || pending.FromStage != "implementation" {
		t.Fatalf("third wait is not implementation input approval: %+v", pending)
	}
	decideHumanInput(t, bin, dir, pathEnv, runID, pending, "dave", "developer", "submit", "https://example.test/org/repo/pull/42")

	code, out = runHumanCLI(t, bin, dir, pathEnv, "run", "--target", dir, "--resume", runID)
	if code != 0 {
		t.Fatalf("human input flow should complete (exit 0), got %d:\n%s", code, out)
	}

	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	inputApprovals := make(map[string]approval.PendingApproval)
	for _, value := range approvals {
		if value.Kind == approval.KindInput {
			inputApprovals[value.FromStage] = value
		}
	}
	for _, stageID := range []string{"intent", "product_spec", "implementation"} {
		value, ok := inputApprovals[stageID]
		if !ok {
			t.Fatalf("missing durable human input approval for %s", stageID)
		}
		_, manifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(),
			filepath.Join(dir, ".ai-team", "runs", runID), runID, value.AttemptID)
		if err != nil {
			t.Fatalf("read %s attempt manifest: %v", stageID, err)
		}
		if manifest.Executor != "human" || manifest.ActorID == "" || manifest.ActorRole == "" || manifest.HumanInputApprovalID != value.ID {
			t.Fatalf("%s manifest lacks human executor/actor: %+v", stageID, manifest)
		}
		if stageID == "intent" && (manifest.ActorID != "alice" || manifest.ActorRole != "business_owner") {
			t.Fatalf("intent actor was not recorded: %+v", manifest)
		}
		if stageID == "product_spec" && (manifest.ActorID != "bob" || manifest.ActorRole != "product_owner") {
			t.Fatalf("product spec actor was not recorded: %+v", manifest)
		}
		if stageID == "implementation" && (manifest.ActorID != "dave" || manifest.ActorRole != "developer") {
			t.Fatalf("implementation actor was not recorded: %+v", manifest)
		}
		if stageID == "product_spec" {
			if len(manifest.Outputs) != 1 || !strings.Contains(manifest.Outputs[0].SourcePath, "specs/product/spec.md") {
				t.Fatalf("product_spec markdown is not its human attempt output: %+v", manifest.Outputs)
			}
		}
		if stageID == "implementation" {
			foundSpec := false
			for _, input := range manifest.Inputs {
				if input.Name == "spec" && strings.Contains(input.SourcePath, "specs/product/spec.md") {
					foundSpec = true
				}
			}
			if !foundSpec {
				t.Fatalf("implementation attempt did not consume product_spec markdown: %+v", manifest.Inputs)
			}
			if len(manifest.Outputs) != 1 {
				t.Fatalf("implementation link missing from human attempt: %+v", manifest.Outputs)
			}
		}
	}

	if code, out := runHumanCLI(t, bin, dir, pathEnv, "verify", "--target", dir, runID); code != 0 {
		t.Fatalf("completed human input evidence did not verify (%d):\n%s", code, out)
	}
}

func readHumanPendingApproval(t *testing.T, dir string) (string, approval.PendingApproval) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".ai-team", "state", "runs", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one lifecycle state, paths=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		RunID             string `json:"run_id"`
		Phase             string `json:"phase"`
		PendingApprovalID string `json:"pending_approval_id"`
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Phase != "waiting" || state.PendingApprovalID == "" {
		t.Fatalf("lifecycle is not waiting for an approval: %v\n%s", err, data)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(state.RunID, state.PendingApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	return state.RunID, pending
}

func decideHumanInput(t *testing.T, bin, dir, pathEnv, runID string, value approval.PendingApproval, actorID, role, action, comment string) {
	t.Helper()
	args := []string{"decision", "--target", dir, "--run", runID, "--approval", value.ID,
		"--actor", actorID, "--role", role, "--action", action, "--subject", value.SubjectHash}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	if code, out := runHumanCLI(t, bin, dir, pathEnv, args...); code != 0 {
		t.Fatalf("decision %s for %s failed (%d):\n%s", action, value.FromStage, code, out)
	}
}

func runHumanCLI(t *testing.T, bin, dir, pathEnv string, args ...string) (int, string) {
	t.Helper()
	command := exec.Command(bin, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), pathEnv)
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err == nil {
		return 0, output.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), output.String()
	}
	t.Fatalf("run %v: %v", args, err)
	return -1, output.String()
}
