package agent

import (
	"strings"
	"testing"
	"testing/fstest"
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

func TestLayeredRegistryUsesExplicitPrecedenceAndSameLayerPrompt(t *testing.T) {
	builtin := fstest.MapFS{
		"sample/def.yaml":   &fstest.MapFile{Data: []byte("name: sample\nruntime: agentcli\nmutation: none\nprompt_file: prompt.md\ndescription: builtin\n")},
		"sample/prompt.md":  &fstest.MapFile{Data: []byte("builtin prompt")},
		"builtin/def.yaml":  &fstest.MapFile{Data: []byte("name: builtin\nruntime: agentcli\nmutation: none\nprompt_file: prompt.md\n")},
		"builtin/prompt.md": &fstest.MapFile{Data: []byte("builtin")},
	}
	project := fstest.MapFS{
		"sample/def.yaml":   &fstest.MapFile{Data: []byte("name: sample\nruntime: agentcli\nmutation: none\nprompt_file: prompt.md\ndescription: project\n")},
		"sample/prompt.md":  &fstest.MapFile{Data: []byte("project prompt")},
		"project/def.yaml":  &fstest.MapFile{Data: []byte("name: project\nruntime: agentcli\nmutation: none\nprompt_file: prompt.md\n")},
		"project/prompt.md": &fstest.MapFile{Data: []byte("project")},
	}
	registry := NewLayered(
		Layer{Name: "project", FS: project},
		Layer{Name: "builtin", FS: builtin},
	)
	agent, err := registry.Load("sample")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Description != "project" || agent.Prompt != "project prompt" || agent.Source != "project" {
		t.Fatalf("override должен целиком происходить из project layer: %+v", agent)
	}
	if listed := registry.List(); len(listed) != 3 {
		t.Fatalf("List должен объединять namespaces без дублей: %+v", listed)
	}
}

func TestLayeredRegistryInvalidOverrideFailsClosed(t *testing.T) {
	registry := NewLayered(
		Layer{Name: "project", FS: fstest.MapFS{"sample/def.yaml": &fstest.MapFile{Data: []byte("name: wrong\nruntime: agentcli\nmutation: none\n")}}},
		Layer{Name: "builtin", FS: fstest.MapFS{"sample/def.yaml": &fstest.MapFile{Data: []byte("name: sample\nruntime: agentcli\nmutation: none\n")}}},
	)
	if _, err := registry.Load("sample"); err == nil || !strings.Contains(err.Error(), "должно совпадать") {
		t.Fatalf("невалидный override не должен fallback-иться: %v", err)
	}
}

func TestRegistry_DefaultPipeline(t *testing.T) {
	r := NewRegistry("../../e2etest/agents")
	p := r.DefaultPipeline()
	if len(p) != 7 {
		t.Errorf("expected 7 agents, got %d", len(p))
	}
}

func TestRegistry_RejectsUnsafeOrInconsistentDefinitions(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"unknown field", "name: sample\nruntime: agentcli\nunknown: true\n", "field unknown not found"},
		{"missing mutation", "name: sample\nruntime: agentcli\n", "mutation обязателен"},
		{"path traversal", "name: sample\nruntime: agentcli\nmutation: none\noutputs:\n  report: ../../outside.md\n", "artifact root"},
		{"artifact root itself", "name: sample\nruntime: agentcli\nmutation: none\noutputs:\n  report: .\n", "artifact root"},
		{"non canonical path", "name: sample\nruntime: agentcli\nmutation: none\noutputs:\n  report: feature//report.md\n", "каноническим"},
		{"input output overlap", "name: sample\nruntime: agentcli\nmutation: none\ninputs:\n  source: feature/data\noutputs:\n  report: feature/data/report.md\n", "пересекаются"},
		{"delivery without external", "name: sample\nruntime: agentcli\nkind: delivery\nmutation: none\n", "требует mutation external"},
		{"delivery without preconditions", "name: sample\nruntime: agentcli\nkind: delivery\nmutation: external\n", "требует declarative preconditions"},
		{"external non-delivery", "name: sample\nruntime: agentcli\nmutation: external\n", "только для kind delivery"},
		{"reserved ai-team scope", "name: sample\nruntime: agentcli\nmutation: source\nallowed_paths: ['.ai-team/config.yaml']\n", "reserved controller"},
		{"reserved git scope", "name: sample\nruntime: agentcli\nmutation: source\nallowed_paths: ['.git/config']\n", "reserved controller"},
		{"diff without mutation", "name: sample\nruntime: agentcli\nmutation: none\nrequire_diff: true\n", "require_diff требует"},
		{"verdict without markdown", "name: sample\nruntime: agentcli\nmutation: none\nverdict:\n  required: true\n  marker: Result\n  values: [PASS, FAIL]\noutputs: {}\n", "markdown output"},
		{"optional verdict", "name: sample\nruntime: agentcli\nmutation: none\nverdict:\n  marker: Verdict\n  values: [APPROVED]\noutputs:\n  report: report.md\n", "required должен быть true"},
		{"marker values mismatch", "name: sample\nruntime: agentcli\nmutation: none\nverdict:\n  required: true\n  marker: Result\n  values: [APPROVED]\noutputs:\n  report: report.md\n", "несовместимо"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewFS(fstest.MapFS{"sample/def.yaml": &fstest.MapFile{Data: []byte(tc.yaml)}})
			_, err := registry.Load("sample")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ожидалась ошибка %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestRegistryValidatePipelineRejectsConflictingAndReservedOutputs(t *testing.T) {
	base := func(output string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("name: first\nruntime: agentcli\nmutation: none\nprompt_file: prompt.md\noutputs:\n  result: " + output + "\n")}
	}
	t.Run("overlap", func(t *testing.T) {
		registry := NewFS(fstest.MapFS{
			"first/def.yaml":   base("'{feature}/specs'"),
			"first/prompt.md":  &fstest.MapFile{Data: []byte("prompt")},
			"second/def.yaml":  &fstest.MapFile{Data: []byte("name: second\nruntime: agentcli\nmutation: none\nprompt_file: prompt.md\noutputs:\n  result: '{feature}/specs/product.md'\n")},
			"second/prompt.md": &fstest.MapFile{Data: []byte("prompt")},
		})
		if err := registry.ValidatePipeline([]string{"first", "second"}); err == nil || !strings.Contains(err.Error(), "пересекается") {
			t.Fatalf("overlapping ownership must fail: %v", err)
		}
	})
	t.Run("reserved", func(t *testing.T) {
		registry := NewFS(fstest.MapFS{
			"first/def.yaml":  base("'{feature}/status/forged.md'"),
			"first/prompt.md": &fstest.MapFile{Data: []byte("prompt")},
		})
		if err := registry.ValidatePipeline([]string{"first"}); err == nil || !strings.Contains(err.Error(), "controller-owned") {
			t.Fatalf("reserved output must fail: %v", err)
		}
	})
}
