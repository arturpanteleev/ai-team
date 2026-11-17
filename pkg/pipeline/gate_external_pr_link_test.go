package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_external_pr_link_test.go — external/docker гейты на PR-этапе получают
// PR-ссылку, а не первый попавшийся output (diff/отчёт). COD-02 добавил к
// результату coder-а diff и unit-test-report перед controller-ссылкой; внешний
// гейт должен выбирать ссылку через prReferenceFromOutput, как встроенные
// PR-гейты.

const prLinkURL = "https://github.com/example/project/pull/9"

func TestExternalGateArtifact_PRResultSelectsReferenceNotFirstOutput(t *testing.T) {
	dir := t.TempDir()
	diffPath := filepath.Join(dir, "coder.diff")
	reportPath := filepath.Join(dir, "unit-test-report.md")
	linkPath := filepath.Join(dir, "result.link")
	for path, content := range map[string]string{
		diffPath:   coderContractDiff,
		reportPath: "**Result:** PASS\n",
		linkPath:   prLinkURL + "\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stage := config.TemplateStage{Result: "link", LinkKind: "pr"}
	result := notifier.StageResult{Outputs: []runtime.Artifact{
		{Name: "diff", Path: diffPath},
		{Name: "unit-test-report", Path: reportPath},
		{Name: "link", Path: linkPath},
	}}

	info, err := externalGateArtifact(stage, result, strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("externalGateArtifact: %v", err)
	}
	if info.LinkKind != "pr" {
		t.Fatalf("link_kind = %q, want pr", info.LinkKind)
	}
	if info.URL != prLinkURL {
		t.Fatalf("гейт должен получить PR-ссылку %q, а не %q", prLinkURL, info.URL)
	}
}

// externalProgramGate on implementation: the program reads the JSON input and
// passes only when artifact.url is the PR URL. With a diff listed first among
// the outputs, the old first-output selection handed the patch to the program
// and the gate failed.
func TestExternalProgramGate_PRResultReceivesURL(t *testing.T) {
	requireProgramHost(t)
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitCmd("add", ".")
	gitCmd("commit", "-qm", "init")

	script := `input=$(cat);` +
		` url=$(printf '%s' "$input" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p' | head -n 1);` +
		` if [ "$url" = "` + prLinkURL + `" ]; then v=pass; else v=fail; fi;` +
		` printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"url=%s"}\n' "$v" "$url"`

	ref := externalProgramGate(script)
	cfg := &config.Config{
		SchemaVersion:  config.CurrentSchemaVersion,
		Template:       "pr-external-test",
		Title:          "PR external test",
		PipelineAgents: []config.AgentConfig{{Name: "coder"}},
		Stages: []config.TemplateStage{{
			ID: "implementation", Title: "Реализация", Function: "developer",
			Result: "link", LinkKind: "pr", Executor: "agent", Agent: "coder",
			ExitGateGroups: workflow.GateGroups{{ref}},
		}},
		CLI: "opencode",
	}
	registry := agent.NewFS(fstest.MapFS{
		"coder/def.yaml": def(`name: coder
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs: {}
outputs:
  diff: '{feature}/coder.diff'
  unit-test-report: '{feature}/unit-test-report.md'
  link: '{feature}/result.link'
`),
		"coder/prompt.md": def("create"),
	})
	rt := newScripted()
	rt.content["coder"] = map[string]string{
		"diff":             coderContractDiff,
		"unit-test-report": "**Result:** PASS\n",
		"link":             prLinkURL + "\n",
	}

	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "тест", TargetDir: dir, ApproveGates: true})
	if err != nil {
		t.Fatalf("run с external-гейтом на PR-этапе должен пройти: %v", err)
	}

	history, histErr := gatesched.AllAttempts(gateEvents(t, dir), "implementation")
	if histErr != nil {
		t.Fatal(histErr)
	}
	found := false
	for _, attempt := range history {
		if attempt.GateName != ref.InstanceName() {
			continue
		}
		found = true
		if attempt.Execution != workflow.GateExecutionSucceeded || attempt.Verdict != workflow.GateVerdictPass {
			t.Fatalf("external-гейт получил неверный artifact.url: execution/verdict = %s/%s, reason %q",
				attempt.Execution, attempt.Verdict, attempt.Reason)
		}
	}
	if !found {
		t.Fatal("запись external-гейта не найдена в журнале")
	}
}
