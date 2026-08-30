package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// V0-1: provenance и политика на мутации тестового флота.

// mutationRegistry — registry с одним source-агентом и заданной политикой
// test_modify_policy (пустая строка — default fail-closed required).
func mutationRegistry(t *testing.T, policy string) *agent.Registry {
	t.Helper()
	return agent.NewFS(fstest.MapFS{
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: source
allowed_paths: ['**']
require_diff: true
` + policyLine(policy)),
		"coder/prompt.md": def("test"),
	})
}

func policyLine(policy string) string {
	if policy == "" {
		return ""
	}
	return "test_modify_policy: " + policy + "\n"
}

// writeFileOnCoder настраивает scripted runtime на создание repository-relative
// файла в target workspace на этапе coder.
func writeFileOnCoder(rt *scriptedRuntime, rel string) {
	rt.onExec = func(agentName string, _ []runtime.Artifact) {
		if agentName != "coder" {
			return
		}
		full := filepath.Join(rt.targetDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			panic(err)
		}
		content := "package change\n"
		if strings.HasSuffix(rel, "_test.go") {
			content = "package change\nimport \"testing\"\nfunc TestCoderWrote(t *testing.T) {}\n"
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			panic(err)
		}
	}
}

func runIDSingleStage(t *testing.T, policy string, rel string) (dir string, runID string, rt *scriptedRuntime) {
	t.Helper()
	dir = env(t)
	rt = newScripted()
	writeFileOnCoder(rt, rel)
	p := New(cfgFor(config.AgentConfig{Name: "coder"}), mutationRegistry(t, policy),
		WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	if err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir}); err != nil {
		t.Fatalf("run не должен останавливаться на deferred test-mutation гейте: %v", err)
	}
	runID = filepath.Base(onlyRunDir(t, dir))
	return dir, runID, rt
}

func loadGates(t *testing.T, dir, runID string) []approval.PendingApproval {
	t.Helper()
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func runEventsPath(dir, runID string) string {
	return filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl")
}

func findTestMutationGate(t *testing.T, gates []approval.PendingApproval) *approval.PendingApproval {
	t.Helper()
	for i := range gates {
		if gates[i].Deferred && gates[i].Trigger == "test_mutation" {
			return &gates[i]
		}
	}
	return nil
}

func TestRun_TestMutationPolicyRequiredCreatesDeferredExactSubjectGate(t *testing.T) {
	dir, runID, rt := runIDSingleStage(t, "required", "src/extra_test.go")
	if strings.Join(rt.executed, ",") != "coder" {
		t.Fatalf("deferred-гейт не должен паузить coder: %s", strings.Join(rt.executed, ","))
	}
	gate := findTestMutationGate(t, loadGates(t, dir, runID))
	if gate == nil {
		t.Fatal("deferred test_mutation approval не создан")
	}
	if gate.Status != approval.StatusPending || gate.FromStage != "coder" || gate.ToStage != "coder" {
		t.Fatalf("неожиданный test_mutation gate: %+v", gate)
	}
	if gate.SubjectHash == "" || len(gate.SubjectHash) != 64 {
		t.Fatalf("exact subject отсутствует: %q", gate.SubjectHash)
	}
	if len(gate.Actions) != 2 || !containsString(gate.Actions, "approve") || !containsString(gate.Actions, "reject") {
		t.Fatalf("actions гейта: %v", gate.Actions)
	}

	// typed evidence в events и attempt manifest-е.
	data, err := os.ReadFile(runEventsPath(dir, runID))
	if err != nil {
		t.Fatalf("events.jsonl: %v", err)
	}
	if !strings.Contains(string(data), `"type":"test_mutations"`) || !strings.Contains(string(data), `"policy":"required"`) {
		t.Fatalf("typed evidence test_mutations отсутствует в событиях")
	}
	if !strings.Contains(string(data), `"type":"approval_requested"`) {
		t.Fatalf("approval_requested не записан для test_mutation гейта")
	}

	manifestPaths, globErr := filepath.Glob(filepath.Join(dir, ".ai-team", "runs", runID, "attempts", "*", "manifest.json"))
	if globErr != nil || len(manifestPaths) != 1 {
		t.Fatalf("attempt manifest не найден: %v (%v)", manifestPaths, globErr)
	}
	manifest, err := os.ReadFile(manifestPaths[0])
	if err != nil {
		t.Fatalf("attempt manifest: %v", err)
	}
	manifestRE := regexp.MustCompile(`"mutation_changes":\s*\[\s*\{\s*"path":\s*"src/extra_test.go",\s*"kind":\s*"added",\s*"class":\s*"tests"\s*\}`)
	if !manifestRE.Match(manifest) {
		t.Fatalf("typed mutation_changes отсутствует в attempt manifest:\n%s", manifest)
	}
}

func TestRun_TestMutationPolicyDefaultFailClosedForSource(t *testing.T) {
	dir, runID, _ := runIDSingleStage(t, "", "cmd/main_test.go")
	gate := findTestMutationGate(t, loadGates(t, dir, runID))
	if gate == nil {
		t.Fatal("source default policy должен быть fail-closed (required)")
	}
}

func TestRun_TestMutationPolicyWarnDoesNotGate(t *testing.T) {
	dir, runID, rt := runIDSingleStage(t, "warn", "src/extra_test.go")
	if strings.Join(rt.executed, ",") != "coder" {
		t.Fatalf("странный порядок исполнения: %s", strings.Join(rt.executed, ","))
	}
	if findTestMutationGate(t, loadGates(t, dir, runID)) != nil {
		t.Fatal("warn-политика не должна создавать гейт")
	}
	events, err := os.ReadFile(runEventsPath(dir, runID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"type":"test_mutations"`) || !strings.Contains(string(events), `"policy":"warn"`) {
		t.Fatalf("warn-политика должна оставлять typed evidence, но events не содержат test_mutations/warn")
	}
}

func TestRun_TestMutationPolicyExplicitOff(t *testing.T) {
	dir, runID, _ := runIDSingleStage(t, "off", "src/extra_test.go")
	if findTestMutationGate(t, loadGates(t, dir, runID)) != nil {
		t.Fatal("off-политика не должна создавать гейт")
	}
	events, err := os.ReadFile(runEventsPath(dir, runID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"policy":"off"`) {
		t.Fatalf("off-политика должна оставлять typed evidence")
	}
}

func TestRun_TestMutationPolicyIgnoresNonTestMutations(t *testing.T) {
	// source-агент меняет исходник, а не тест — гейта быть не должно.
	dir, runID, _ := runIDSingleStage(t, "required", "src/feature.go")
	if findTestMutationGate(t, loadGates(t, dir, runID)) != nil {
		t.Fatal("невыход на тестовый флот не должен создавать test_mutation гейт")
	}
}

func TestTestMutationSubjectHashDeterministic(t *testing.T) {
	changes := []workflow.MutationChange{
		{Path: "a/a_test.go", Kind: workflow.MutationAdded, Class: "tests"},
		{Path: "b/c_test.go", Kind: workflow.MutationModified, Class: "tests"},
	}
	first, err := testMutationSubjectHash("run-1", "coder", changes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := testMutationSubjectHash("run-1", "coder", changes)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("exact subject должен быть детерминирован: %s != %s", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("subject должен быть SHA-256: %q", first)
	}
	other, err := testMutationSubjectHash("run-1", "coder", append([]workflow.MutationChange(nil), changes[:1]...))
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("другой набор тест-мутаций должен давать другой subject")
	}
}
