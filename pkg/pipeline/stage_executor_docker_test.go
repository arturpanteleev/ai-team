package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// S10: code_review is executed by an external container agent (the fixture in
// e2etest/container-review-agent) through the ordinary stage executor contract.
// Verdicts take the existing routes; container failures are technical errors.

const dockerReviewImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

func requireDockerForReview(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("container fixture requires a POSIX host")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "pull", "-q", dockerReviewImage).CombinedOutput(); err != nil {
		t.Skipf("pinned test image cannot be pulled: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func dockerReviewRegistry() *agent.Registry {
	return agent.NewLayered(agent.Layer{Name: "test", FS: fstest.MapFS{
		"implementer/def.yaml": def(`name: implementer
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  patch: '{feature}/change.patch'
`),
		"implementer/prompt.md": def("test"),
		"code-reviewer/def.yaml": def(`name: code-reviewer
runtime: agentcli
prompt_file: prompt.md
mutation: none
verdict:
  required: true
  marker: Verdict
  values: [APPROVED, CHANGES_REQUESTED, REJECTED]
inputs:
  diff: '{feature}/change.patch'
outputs:
  review: '{feature}/review.md'
`),
		"code-reviewer/prompt.md": def("test"),
	}})
}

func dockerReviewConfig(t *testing.T, registry *agent.Registry, flags ...string) *config.Config {
	t.Helper()
	cfg := &config.Config{
		SchemaVersion:  config.CurrentSchemaVersion,
		Template:       "docker-review-test",
		Title:          "Docker review test",
		PipelineAgents: []config.AgentConfig{{Name: "implementer"}, {Name: "code-reviewer"}},
		Stages: []config.TemplateStage{
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "md", Executor: "agent", Agent: "implementer", Confirm: "auto"},
			{ID: "code_review", Title: "Code review", Function: "reviewer", Result: "md", Executor: "agent", Agent: "code-reviewer", Adapter: "container-review", Confirm: "auto"},
		},
		Returns: []config.TemplateReturn{{From: "code_review", To: "implementation", MaxVisits: 2}},
		ExecutorAdapters: map[string]stageexec.AdapterSpec{
			"container-review": {
				Argv:    append([]string{"/bin/sh", "/agent/review.sh"}, flags...),
				Timeout: "2m",
				Docker:  &stageexec.DockerSpec{Image: dockerReviewImage, Mounts: []string{"agent:/agent"}},
			},
		},
		CLI: "opencode",
	}
	if err := cfg.Validate(registry); err != nil {
		t.Fatalf("конфиг с контейнерным исполнителем должен валидироваться: %v", err)
	}
	return cfg
}

func installReviewFixture(t *testing.T, dir string) {
	t.Helper()
	script, err := os.ReadFile("../../e2etest/container-review-agent/review.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "review.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
}

func dockerReviewEngine(t *testing.T, dir string, rt *scriptedRuntime, capture *captureNotifier, flags ...string) *RunEngine {
	t.Helper()
	installReviewFixture(t, dir)
	registry := dockerReviewRegistry()
	p := New(dockerReviewConfig(t, registry, flags...), registry,
		WithRuntimeFactory(rt.factory), WithNotifier(capture), WithPrompter(&scriptedPrompter{}))
	return NewRunEngine(p)
}

func TestRun_DockerCodeReviewRoutesApproveAndChangesRequested(t *testing.T) {
	requireDockerForReview(t)
	dir := env(t)
	rt := newScripted()
	rt.contentFn["implementer"] = func(call int) map[string]string {
		if call == 1 {
			return map[string]string{"patch": "+func a() {} // FIXME handle the empty response\n"}
		}
		return map[string]string{"patch": "+func a() { handle() }\n"}
	}
	capture := &captureNotifier{}
	engine := dockerReviewEngine(t, dir, rt, capture)
	started, startErr := engine.Start(context.Background(), RunConfig{Feature: "feat", TaskDesc: "review in a container", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(startErr, &required) || started.RunID == "" {
		t.Fatalf("changes requested must pause for the return decision: result=%+v err=%v", started, startErr)
	}
	review, err := os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md"))
	if err != nil || !strings.Contains(string(review), "CHANGES_REQUESTED") {
		t.Fatalf("review = %q, %v", review, err)
	}
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Load(started.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(pending.Actions, "return_to_implementation") || pending.Targets["return_to_implementation"] != "implementation" {
		t.Fatalf("the existing return route must be offered: %+v", pending)
	}
	if _, err := store.Decide(started.RunID, pending.ID, approval.Decision{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "return_to_implementation",
		Comment: "Handle the empty response as the container review asked.", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Resume(context.Background(), ResumeConfig{RunID: started.RunID, TargetDir: dir})
	var finalGate *ApprovalRequiredError
	if !errors.As(err, &finalGate) {
		t.Fatalf("the approved review must reach its decision gate: result=%+v err=%v", result, err)
	}
	if rt.calls["implementer"] != 2 {
		t.Fatalf("the return must rerun implementation: calls=%+v", rt.calls)
	}
	review, _ = os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md"))
	if !strings.Contains(string(review), "**Verdict:** APPROVED") {
		t.Fatalf("the second review must approve the fixed diff: %q", review)
	}
	finalDecision, err := store.Load(started.RunID, finalGate.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(started.RunID, finalDecision.ID, approval.Decision{
		ActorID: "reviewer-1", ActorRole: "reviewer", Action: "approve",
		Comment: "Container review approved the fix.", SubjectHash: finalDecision.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	result, err = engine.Resume(context.Background(), ResumeConfig{RunID: started.RunID, TargetDir: dir})
	if err != nil || (result.Outcome != workflow.RunCompleted && result.Outcome != workflow.RunCompletedWithWarnings) {
		t.Fatalf("approve must complete the run: result=%+v err=%v", result, err)
	}

	// Evidence: both reviews name the container executor with its image digest
	// and a saved log; the container itself is gone.
	reviews := 0
	for _, call := range capture.calls {
		if call.Name != "code_review" {
			continue
		}
		info := call.ExecutorInfo
		if info == nil || info.Kind != stageexec.KindExternal || info.Name != "container-review" ||
			info.Metadata["container.image"] != dockerReviewImage || info.Metadata["container.network"] != "none" ||
			info.Metadata["input_writable"] != "false" || info.LogFile == "" {
			t.Fatalf("executor info = %+v", info)
		}
		reviews++
	}
	if reviews != 2 {
		t.Fatalf("expected two container reviews, got %d: %+v", reviews, capture.calls)
	}
	evidenceHasImage := false
	_ = filepath.WalkDir(filepath.Join(dir, ".ai-team", "runs", started.RunID), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() {
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), dockerReviewImage) {
				evidenceHasImage = true
			}
		}
		return nil
	})
	if !evidenceHasImage {
		t.Fatal("the run evidence must record the image digest")
	}
	out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "label="+stageexec.StageAttemptLabel+"="+capture.calls[len(capture.calls)-1].AttemptID).Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("container left behind: %s", out)
	}
}

func TestRun_DockerCodeReviewFailuresAreTechnicalErrors(t *testing.T) {
	requireDockerForReview(t)
	cases := map[string]struct {
		flag    string
		message string
	}{
		"error status":     {"--fail", "сообщил об ошибке"},
		"container crash":  {"--crash", "завершился с ошибкой"},
		"not json":         {"--garbage", "валидным JSON"},
		"verdict mismatch": {"--mismatch", "заявил verdict"},
		"ambiguous output": {"--both", "неоднозначный"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := env(t)
			rt := newScripted()
			rt.contentFn["implementer"] = func(int) map[string]string { return map[string]string{"patch": "+ok\n"} }
			capture := &captureNotifier{}
			engine := dockerReviewEngine(t, dir, rt, capture, tc.flag)
			_, err := engine.Start(context.Background(), RunConfig{Feature: "feat", TaskDesc: "review in a container", TargetDir: dir})
			var required *ApprovalRequiredError
			if err == nil || errors.As(err, &required) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("a failed container must be a technical error containing %q: %v", tc.message, err)
			}
			last := capture.calls[len(capture.calls)-1]
			if last.Status == "passed" || last.Err == nil || last.ExecutorInfo == nil ||
				last.ExecutorInfo.Metadata["container.image"] != dockerReviewImage {
				t.Fatalf("failed attempt = %+v", last)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md")); statErr == nil {
				t.Fatal("no review may exist after a failed or rejected container response")
			}
		})
	}
}
