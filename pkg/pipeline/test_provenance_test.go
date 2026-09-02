package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

// TestRun_ProvenanceCapturedAndStableResume проверяет, что authority-bearing
// provenance manifest v1 (V0-2) пишется в run.json, содержит все источники
// (runtime/config/agent_definition/prompt/check_suite/provider_model/base/
// candidate) и не блокирует стабильный resume.
func TestRun_ProvenanceCapturedAndStableResume(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package p\nconst R = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		command := commandIn(dir, "git", args...)
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}

	rt := newScripted()
	rt.onExec = func(name string, inputs []runtime.Artifact) {
		if name == "coder" {
			_ = os.WriteFile(filepath.Join(rt.targetDir, "coder.go"),
				[]byte(fmt.Sprintf("package p\nconst R = %d\n", rt.calls["coder"])), 0644)
		}
	}
	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	runCfg := RunConfig{Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var required *ApprovalRequiredError
	result, runErr := p.RunWithResult(context.Background(), runCfg)
	if !errors.As(runErr, &required) {
		t.Fatalf("ожидался pending approval, got %v", runErr)
	}
	runID := result.RunID

	runDir := onlyRunDir(t, dir)
	runJSON, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runJSON), `"provenance"`) {
		t.Fatalf("run.json не содержит provenance manifest")
	}

	var decoded struct {
		Provenance provenance.Manifest `json:"provenance"`
	}
	if err := json.Unmarshal(runJSON, &decoded); err != nil {
		t.Fatalf("provenance в run.json не десериализуется: %v", err)
	}
	if decoded.Provenance.SchemaVersion != provenance.SchemaVersion {
		t.Fatalf("schema_version provenance: %d", decoded.Provenance.SchemaVersion)
	}
	expectKinds := map[string]int{
		provenance.KindRuntime:       1,
		provenance.KindConfig:        1,
		provenance.KindBase:          1,
		provenance.KindCandidate:     1,
		provenance.KindAgent:         2,
		provenance.KindPrompt:        2,
		provenance.KindCheckSuite:    2,
		provenance.KindProviderModel: 2,
	}
	counts := make(map[string]int)
	for _, item := range decoded.Provenance.Items {
		counts[item.Kind]++
	}
	for kind, want := range expectKinds {
		if counts[kind] != want {
			t.Fatalf("kind %s: ожидалось %d источников, got %d", kind, want, counts[kind])
		}
	}
	runtimeDigest, found := decoded.Provenance.Find(provenance.KindRuntime, "")
	if !found || !runtimeDigest.Known() || len(runtimeDigest.Value) != 64 {
		t.Fatalf("runtime digest должен быть known 64-hex, got %+v", runtimeDigest)
	}
	for _, name := range []string{"analyst", "coder"} {
		if digest, ok := decoded.Provenance.Find(provenance.KindAgent, name); !ok || !digest.Known() {
			t.Fatalf("agent_definition %s должен быть known: %+v", name, digest)
		}
		// cfgForGraph задаёт глобальный CLI "opencode" — provider_model известен.
		if digest, ok := decoded.Provenance.Find(provenance.KindProviderModel, name); !ok || !digest.Known() {
			t.Fatalf("provider_model %s должен быть known (CLI opencode): %+v", name, digest)
		}
	}
	// Стабильный resume не должен дрифтить.
	if _, err := store.Decide(runID, required.ApprovalID, approval.Decision{
		ActorID: "human-1", ActorRole: "operator", Action: "approve", SubjectHash: required.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: runID, TargetDir: dir}); err != nil {
		t.Fatalf("стабильный resume не должен дрифтить: %v", err)
	}
}

// TestRun_ProvenanceDriftBlocksResume проверяет, что изменение
// authority-bearing поля в сохранённом manifest блокирует resume (fail-closed).
func TestRun_ProvenanceDriftBlocksResume(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coder.go"), []byte("package p\nconst R = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		command := commandIn(dir, "git", args...)
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("git %v: %v\n%s", args, commandErr, output)
		}
	}

	rt := newScripted()
	cfg := cfgFor(config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "coder"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))
	var required *ApprovalRequiredError
	result, runErr := p.RunWithResult(context.Background(),
		RunConfig{Feature: "feat", TaskDesc: "тестовая задача", TargetDir: dir})
	if !errors.As(runErr, &required) {
		t.Fatalf("ожидался pending approval, got %v", runErr)
	}
	// Сначала решаем переход, чтобы resume прошёл сквозь approval-фазу и дошёл
	// до drift-проверки; DоD — authority-bearing поле блокирует resume.
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(result.RunID, required.ApprovalID, approval.Decision{
		ActorID: "human-1", ActorRole: "operator", Action: "approve", SubjectHash: required.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	runDir := onlyRunDir(t, dir)
	runJSON, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var structure map[string]any
	if err := json.Unmarshal(runJSON, &structure); err != nil {
		t.Fatal(err)
	}
	items, ok := structure["provenance"].(map[string]any)["items"].([]any)
	if !ok {
		t.Fatalf("provenance.items отсутствует в run.json")
	}
	tampered := false
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["kind"] != provenance.KindRuntime {
			continue
		}
		digest := item["digest"].(map[string]any)
		if value, ok := digest["value"].(string); !ok || len(value) != 64 {
			continue
		}
		digest["value"] = strings.Repeat("0", 64)
		tampered = true
	}
	if !tampered {
		t.Fatalf("runtime источник не найден для подмены")
	}
	tamperedJSON, err := json.MarshalIndent(structure, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), append(tamperedJSON, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	_, resumeErr := p.RunWithResult(context.Background(),
		RunConfig{ResumeRunID: result.RunID, TargetDir: dir})
	if resumeErr == nil || !strings.Contains(resumeErr.Error(), "provenance drift") {
		t.Fatalf("ожидался provenance drift при resume, got %v", resumeErr)
	}
}

func commandIn(dir string, name string, args ...string) *exec.Cmd {
	command := exec.Command(name, args...)
	command.Dir = dir
	return command
}
