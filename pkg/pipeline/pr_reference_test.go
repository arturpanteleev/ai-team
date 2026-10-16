package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

func TestPRReferenceFromOutputIsStrict(t *testing.T) {
	accepted := map[string]string{
		"https://github.com/o/r/pull/42\n": "https://github.com/o/r/pull/42",
		"  17 ":                            "17",
		`{"pr_url": "https://github.com/o/r/pull/7"}`:                   "https://github.com/o/r/pull/7",
		`{"url": "https://github.com/o/r/pull/8", "title": "x"}`:        "https://github.com/o/r/pull/8",
		`{"pr_url": "https://github.com/o/r/pull/9", "url": "ignored"}`: "https://github.com/o/r/pull/9",
	}
	for input, want := range accepted {
		if got, ok := prReferenceFromOutput([]byte(input)); !ok || got != want {
			t.Errorf("%q: got %q ok=%v, want %q", input, got, ok, want)
		}
	}
	rejected := []string{
		"diff --git a/x b/x\n+++ b/x\n",
		"https://github.com/o/r/pull/42 and more text",
		"https://github.com/o/r/issues/42",
		"https://github.com/o/r",
		"0",
		"-3",
		`{"pr_url": 5}`,
		`{"title": "no link"}`,
		`{"pr_url": "https://github.com/o/r/pull/1"} trailing`,
		`["https://github.com/o/r/pull/1"]`,
		"not a url",
		"https://evil.example/o/r/pull/1",
		"https://github.com.evil.example/o/r/pull/1",
		`{"pr_url": "https://evil.example/o/r/pull/1"}`,
	}
	for _, input := range rejected {
		if got, ok := prReferenceFromOutput([]byte(input)); ok {
			t.Errorf("%q must not be a PR reference, got %q", input, got)
		}
	}
}

// Дополнительный не-Markdown выход агента на PR-этапе (diff) не принимается за PR.
func TestStageCheckInputsPRStageSkipsNonPRTextOutputs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) runtime.Artifact {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return runtime.Artifact{Name: name, Path: path}
	}
	diff := write("change.diff", "diff --git a/a b/a\n+x\n")
	link := write("pr.txt", "https://github.com/o/r/pull/5\n")
	report := write("report.md", "# Report\n")
	rs := &runState{}
	prStage := config.TemplateStage{ID: "implementation", Result: "link", LinkKind: "pr"}

	content, reference := rs.stageCheckInputs(prStage, notifier.StageResult{Outputs: []runtime.Artifact{diff, report, link}})
	if reference != "https://github.com/o/r/pull/5" || content == "" {
		t.Fatalf("diff before the PR link must be ignored: reference=%q content=%q", reference, content)
	}
	_, reference = rs.stageCheckInputs(prStage, notifier.StageResult{Outputs: []runtime.Artifact{diff}})
	if reference != "" {
		t.Fatalf("a diff alone is not a PR reference, got %q", reference)
	}
	// Остальные виды ссылок сохраняют прежнее поведение.
	buildStage := config.TemplateStage{ID: "deploy", Result: "link", LinkKind: "build"}
	_, reference = rs.stageCheckInputs(buildStage, notifier.StageResult{Outputs: []runtime.Artifact{diff}})
	if reference == "" {
		t.Fatal("non-PR link stages keep the lenient reference")
	}
}

func TestPRReferenceHostFollowsGHHost(t *testing.T) {
	const ghe = "https://ghe.corp.example/o/r/pull/3"
	t.Setenv("GH_HOST", "")
	if _, ok := prReferenceFromOutput([]byte(ghe)); ok {
		t.Fatal("чужой хост без GH_HOST должен отвергаться")
	}
	t.Setenv("GH_HOST", "ghe.corp.example")
	if got, ok := prReferenceFromOutput([]byte(ghe)); !ok || got != ghe {
		t.Fatalf("хост из GH_HOST должен приниматься: %q %v", got, ok)
	}
}
