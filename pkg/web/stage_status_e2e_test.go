package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing/fstest"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
)

// stage_status_e2e_test.go — fixtures of the S11 end-to-end test (the flow is
// in stage_status_flow_test.go). One real task of the idea-to-prod shape:
// human spec, internal agent design with a checker, internal agent
// implementation, external agent code review, human QA.

// s11Hook is called from inside the agents while the pipeline is mid-flight,
// so the API is read at the moment the stage really works or is checked.
type s11Hook func(agentName string)

type s11Runtime struct {
	mu      sync.Mutex
	hook    s11Hook
	reviews int
	coders  int
}

func (r *s11Runtime) Execute(_ context.Context, a *runtime.Agent, task *runtime.Task, _ []runtime.Artifact) error {
	r.mu.Lock()
	hook := r.hook
	if a.Name == "coder" {
		r.coders++
	}
	r.mu.Unlock()
	if hook != nil {
		hook(a.Name)
	}
	content := map[string]string{
		"architect": "# Дизайн\n\nрешение\n",
		"verifier":  "# Отчёт проверки\n\n**Verdict:** PASSED\n",
		"coder":     "# Реализация\n\nготово\n",
	}[a.Name]
	for _, outPath := range a.Outputs {
		full := filepath.Join(task.ArtifactRoot, runtime.ReplaceVars(outPath, task.Feature))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// s11External is the external code reviewer: the first review asks for changes,
// the second approves.
type s11External struct {
	rt *s11Runtime
}

func (e *s11External) Identity() stageexec.Identity {
	return stageexec.Identity{Kind: stageexec.KindExternal, Name: "container-review"}
}

func (e *s11External) Execute(_ context.Context, request stageexec.Request) (stageexec.Result, error) {
	e.rt.mu.Lock()
	e.rt.reviews++
	n := e.rt.reviews
	hook := e.rt.hook
	e.rt.mu.Unlock()
	if hook != nil {
		hook("code-reviewer")
	}
	verdictValue := "CHANGES_REQUESTED"
	if n > 1 {
		verdictValue = "APPROVED"
	}
	for _, output := range request.Outputs {
		if err := os.MkdirAll(filepath.Dir(output.Path), 0o755); err != nil {
			return stageexec.Result{}, err
		}
		if err := os.WriteFile(output.Path, []byte("# Ревью\n\n**Verdict:** "+verdictValue+"\n"), 0o644); err != nil {
			return stageexec.Result{}, err
		}
	}
	return stageexec.Result{Identity: e.Identity(), Summary: "reviewed", Verdict: strings.ToUpper(verdictValue)}, nil
}

func s11Registry() *agent.Registry {
	files := fstest.MapFS{}
	add := func(name, outputs string, inputs string) {
		files[name+"/def.yaml"] = &fstest.MapFile{Data: []byte("name: " + name + "\nruntime: agentcli\nprompt_file: prompt.md\nmutation: none\n" + inputs + outputs)}
		files[name+"/prompt.md"] = &fstest.MapFile{Data: []byte("test")}
	}
	add("architect", "outputs:\n  design: '{feature}/design.md'\n", "inputs:\n  task: tasks/{feature}/task.md\n")
	add("verifier", "outputs:\n  verification: '{feature}/verification.md'\n", "")
	add("coder", "outputs:\n  patch: '{feature}/change.md'\n", "inputs:\n  task: tasks/{feature}/task.md\n")
	files["code-reviewer/def.yaml"] = &fstest.MapFile{Data: []byte(`name: code-reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs:
  diff: '{feature}/change.md'
outputs:
  review: '{feature}/review.md'
`)}
	files["code-reviewer/prompt.md"] = &fstest.MapFile{Data: []byte("test")}
	return agent.NewFS(files)
}

func s11Config() *config.Config {
	return &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "s11-idea-to-prod",
		Title:         "S11 idea to prod",
		PipelineAgents: []config.AgentConfig{
			{Name: "architect"}, {Name: "verifier"}, {Name: "coder"}, {Name: "code-reviewer"},
		},
		Stages: []config.TemplateStage{
			{ID: "product_spec", Title: "Продуктовая спецификация", Function: "po", Result: "md", Executor: "human", Confirm: "auto"},
			{ID: "tech_design", Title: "Техническая архитектура", Function: "architect", ApproverFunction: "developer", Result: "md", Executor: "agent", Agent: "architect",
				Check: &config.TemplateCheck{Kind: "agent", Agent: "verifier"}, Confirm: "required"},
			{ID: "implementation", Title: "Техническая реализация", Function: "developer", Result: "md", Executor: "agent", Agent: "coder", Confirm: "auto"},
			{ID: "code_review", Title: "Код-ревью", Function: "reviewer", Result: "md", Executor: "agent", Agent: "code-reviewer", Adapter: "container-review", Confirm: "required"},
			{ID: "qa", Title: "Тестирование", Function: "qa", Result: "md", Executor: "human", Confirm: "auto"},
		},
		Returns: []config.TemplateReturn{{From: "code_review", To: "implementation", MaxVisits: 3}},
		ExecutorAdapters: map[string]stageexec.AdapterSpec{
			"container-review": {Argv: []string{"/bin/true"}, Timeout: "1m"},
		},
		CLI: "opencode",
	}
}

var s11Roles = []cloudidentity.Role{
	cloudidentity.RoleProductOwner, cloudidentity.RoleArchitect, cloudidentity.RoleDeveloper,
	cloudidentity.RoleReviewer, cloudidentity.RoleQA, cloudidentity.RoleReleaseManager,
}
