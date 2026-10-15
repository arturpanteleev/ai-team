package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// code_gates_test.go — every built-in code gate (S04) executes as a gate of the
// group scheduler: it leaves an attempt with a separate execution and verdict
// in the run journal, and pass, fail and error are three different outcomes.

const codeGatePRURL = "https://github.com/example/project/pull/9"

// ghRunner answers `gh pr view` for the pr_exists / pr_open / pr_base_branch
// gates.
type ghRunner struct {
	stdout, stderr string
	exit           int
	calls          int
}

func (g *ghRunner) Run(_ context.Context, _ string, _ string, _ ...string) delivery.StepResult {
	g.calls++
	if g.exit != 0 {
		return delivery.StepResult{Status: delivery.StepFailed, ExitCode: g.exit, Stderr: g.stderr}
	}
	return delivery.StepResult{Status: delivery.StepPassed, Stdout: g.stdout}
}

func ghView(state, base string) string {
	return `{"url":"` + codeGatePRURL + `","state":"` + state + `","baseRefName":"` + base + `","headRefName":"feature"}`
}

type codeGateOutcome struct {
	err       error
	execution workflow.GateExecution
	verdict   workflow.GateVerdict
	reason    string
	attempts  int
}

// runCodeGate runs a one-stage template whose only exit gate is ref and returns
// the recorded attempt of that gate.
func runCodeGate(t *testing.T, ref workflow.GateRef, result, content string, gh *ghRunner, checks *fakePRStageChecks) codeGateOutcome {
	t.Helper()
	dir := env(t)
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "master")
	git("commit", "-q", "--allow-empty", "-m", "init")

	cfg := stageCheckConfig(nil)
	cfg.Stages[0].Confirm = ""
	cfg.Stages[0].ExitGateGroups = workflow.GateGroups{{ref}}
	rt := newScripted()
	registry := stageCheckRegistry(false)
	if result == "link" {
		cfg = &config.Config{
			SchemaVersion:  config.CurrentSchemaVersion,
			Template:       "code-gate-test",
			Title:          "Code gate test",
			PipelineAgents: []config.AgentConfig{{Name: "prauthor"}},
			Stages: []config.TemplateStage{{
				ID: "implementation", Title: "Реализация", Function: "developer", Result: "link", LinkKind: "pr",
				Executor: "agent", Agent: "prauthor", ExitGateGroups: workflow.GateGroups{{ref}},
			}},
			CLI: "opencode",
		}
		registry = prReviewRegistry()
		rt.content["prauthor"] = map[string]string{"pr-url": content}
	} else {
		rt.content["tech"] = map[string]string{"result": content}
	}
	if checks == nil {
		checks = &fakePRStageChecks{}
	}
	p := New(cfg, registry, WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}),
		WithPRStageChecks(checks), WithPullRequestRunner(gh))
	err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "тест гейта", TargetDir: dir, ApproveGates: true})
	stageID := cfg.Stages[0].ID
	history, histErr := gatesched.AllAttempts(gateEvents(t, dir), stageID)
	if histErr != nil {
		t.Fatal(histErr)
	}
	out := codeGateOutcome{err: err, attempts: len(history)}
	for _, attempt := range history {
		if attempt.GateName == ref.InstanceName() {
			out.execution, out.verdict, out.reason = attempt.Execution, attempt.Verdict, attempt.Reason
			if err := attempt.Validate(); err != nil {
				t.Fatalf("attempt record invalid: %v", err)
			}
		}
	}
	return out
}

func requireOutcome(t *testing.T, got codeGateOutcome, execution workflow.GateExecution, verdict workflow.GateVerdict) {
	t.Helper()
	if got.attempts != 1 {
		t.Fatalf("exactly one gate attempt must be recorded by the scheduler, got %d", got.attempts)
	}
	if got.execution != execution || got.verdict != verdict {
		t.Fatalf("execution/verdict = %s/%s, want %s/%s (reason %q)", got.execution, got.verdict, execution, verdict, got.reason)
	}
	passed := execution == workflow.GateExecutionSucceeded && verdict == workflow.GateVerdictPass
	if passed && got.err != nil {
		t.Fatalf("a passed gate must let the stage through: %v", got.err)
	}
	if !passed && !errors.Is(got.err, ErrUserStopped) {
		t.Fatalf("a gate that did not pass must block the transition: %v", got.err)
	}
}

func TestCodeGates_MarkdownGatesPassFailAndError(t *testing.T) {
	sections := codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}})
	marker := codeGate(gatelib.GateVerdictMarker, nil)
	phrases := codeGate(gatelib.GateRequiredPhrases, map[string]any{
		"phrases": []any{"метрика"}, "case": "insensitive", "scope": "result", "match": "all",
	})
	cases := []struct {
		name    string
		ref     workflow.GateRef
		content string
		exec    workflow.GateExecution
		verdict workflow.GateVerdict
	}{
		{"required_sections pass", sections, "# Спека\n\n## Критерии приёмки\n\nок\n", "succeeded", "pass"},
		{"required_sections fail", sections, "# Спека\n\nбез раздела\n", "succeeded", "fail"},
		{"verdict_marker pass", marker, "# Ревью\n\n**Verdict:** APPROVED\n", "succeeded", "pass"},
		{"verdict_marker fail", marker, "# Ревью\n\nбез вердикта\n", "succeeded", "fail"},
		{"required_phrases pass", phrases, "# Спека\n\nМетрика успеха определена\n", "succeeded", "pass"},
		{"required_phrases fail", phrases, "# Спека\n\nничего нужного\n", "succeeded", "fail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runCodeGate(t, tc.ref, "md", tc.content, &ghRunner{}, nil)
			requireOutcome(t, got, tc.exec, tc.verdict)
		})
	}
}

func TestCodeGates_UnreadableMarkdownResultIsErrorNotPass(t *testing.T) {
	missing := notifier.StageResult{Outputs: []runtime.Artifact{{Name: "result", Path: filepath.Join(t.TempDir(), "absent.md")}}}
	directory := notifier.StageResult{Outputs: []runtime.Artifact{{Name: "result", Path: t.TempDir() + "/dir.md"}}}
	if err := os.Mkdir(directory.Outputs[0].Path, 0o755); err != nil {
		t.Fatal(err)
	}
	noMarkdown := notifier.StageResult{Outputs: []runtime.Artifact{{Name: "pr-url", Path: filepath.Join(t.TempDir(), "x.txt")}}}
	refs := []workflow.GateRef{
		codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Итог"}}),
		codeGate(gatelib.GateVerdictMarker, nil),
		codeGate(gatelib.GateRequiredPhrases, map[string]any{"phrases": []any{"x"}, "case": "sensitive", "scope": "result", "match": "any"}),
	}
	for _, ref := range refs {
		for name, result := range map[string]notifier.StageResult{"missing": missing, "directory": directory, "no markdown output": noMarkdown} {
			t.Run(ref.Gate+"/"+name, func(t *testing.T) {
				rs := &runState{p: New(&config.Config{}, nil), runCfg: RunConfig{TargetDir: t.TempDir()}}
				runner := rs.builtinGateRunner(config.TemplateStage{ID: "s", Result: "md"}, result, &waveState{hard: &hardWaveState{}, reports: map[string]stagecheck.Report{}})
				outcome := runner.Run(context.Background(), gatesched.Invocation{Ref: ref})
				if outcome.Execution != workflow.GateExecutionError || outcome.Verdict != "" {
					t.Fatalf("an unreadable result must be a technical error, got %+v", outcome)
				}
			})
		}
	}
}

func TestCodeGates_PullRequestGatesPassFailAndError(t *testing.T) {
	const wrongBase = "release"
	cases := []struct {
		name    string
		gate    string
		gh      *ghRunner
		exec    workflow.GateExecution
		verdict workflow.GateVerdict
	}{
		{"pr_exists pass", gatelib.GatePRExists, &ghRunner{stdout: ghView("OPEN", "master")}, "succeeded", "pass"},
		{"pr_exists fail", gatelib.GatePRExists, &ghRunner{exit: 1, stderr: "no pull requests found for branch"}, "succeeded", "fail"},
		{"pr_exists error", gatelib.GatePRExists, &ghRunner{exit: 4, stderr: "gh: network unreachable"}, "error", ""},
		{"pr_open pass", gatelib.GatePROpen, &ghRunner{stdout: ghView("OPEN", "master")}, "succeeded", "pass"},
		{"pr_open fail", gatelib.GatePROpen, &ghRunner{stdout: ghView("MERGED", "master")}, "succeeded", "fail"},
		{"pr_open error", gatelib.GatePROpen, &ghRunner{stdout: "not json"}, "error", ""},
		{"pr_base_branch pass", gatelib.GatePRBaseBranch, &ghRunner{stdout: ghView("OPEN", "master")}, "succeeded", "pass"},
		{"pr_base_branch fail", gatelib.GatePRBaseBranch, &ghRunner{stdout: ghView("OPEN", wrongBase)}, "succeeded", "fail"},
		{"pr_base_branch error", gatelib.GatePRBaseBranch, &ghRunner{exit: 2, stderr: "gh: auth required"}, "error", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runCodeGate(t, codeGate(tc.gate, nil), "link", codeGatePRURL, tc.gh, nil)
			requireOutcome(t, got, tc.exec, tc.verdict)
			if tc.gh.calls != 1 {
				t.Fatalf("gh pr view must be asked once, got %d", tc.gh.calls)
			}
		})
	}
}

func TestCodeGates_PullRequestGateWithoutLinkFails(t *testing.T) {
	// The result file has no PR reference (blank): the rule fails, never passes.
	got := runCodeGate(t, codeGate(gatelib.GatePRExists, nil), "link", " \n", &ghRunner{stdout: ghView("OPEN", "master")}, nil)
	if got.execution == workflow.GateExecutionSucceeded && got.verdict == workflow.GateVerdictPass {
		t.Fatalf("a blank PR reference must not pass: %+v", got)
	}
}

func TestCodeGates_ProjectRuleGatesPassFailAndError(t *testing.T) {
	for _, gate := range []string{gatelib.GateProjectChecks, gatelib.GatePRChecksGreen} {
		cases := []struct {
			name    string
			checks  *fakePRStageChecks
			exec    workflow.GateExecution
			verdict workflow.GateVerdict
		}{
			{"pass", &fakePRStageChecks{}, "succeeded", "pass"},
			{"fail", &fakePRStageChecks{status: stagecheck.RuleFailed, reason: "красный CI"}, "succeeded", "fail"},
			{"error", &fakePRStageChecks{err: errors.New("gh unavailable")}, "error", ""},
		}
		for _, tc := range cases {
			t.Run(gate+" "+tc.name, func(t *testing.T) {
				got := runCodeGate(t, codeGate(gate, nil), "link", codeGatePRURL, &ghRunner{}, tc.checks)
				requireOutcome(t, got, tc.exec, tc.verdict)
				if tc.checks.called[gate] != 1 {
					t.Fatalf("the controller-side check %s must run once, got %v", gate, tc.checks.called)
				}
				if tc.name == "fail" && !strings.Contains(got.reason, "красный CI") {
					t.Fatalf("the reason must reach the attempt record: %q", got.reason)
				}
			})
		}
	}
}

func TestCodeGates_PullRequestGatesShareOneLookupPerWave(t *testing.T) {
	groups := workflow.GateGroups{{
		codeGate(gatelib.GatePRExists, nil), codeGate(gatelib.GatePROpen, nil), codeGate(gatelib.GatePRBaseBranch, nil),
	}}
	dir := env(t)
	for _, args := range [][]string{{"init", "-q", "-b", "master"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "i"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	rt := newScripted()
	rt.content["prauthor"] = map[string]string{"pr-url": codeGatePRURL}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, Template: "t", Title: "t",
		PipelineAgents: []config.AgentConfig{{Name: "prauthor"}},
		Stages: []config.TemplateStage{{
			ID: "implementation", Title: "Реализация", Function: "developer", Result: "link", LinkKind: "pr",
			Executor: "agent", Agent: "prauthor", ExitGateGroups: groups,
		}},
		CLI: "opencode",
	}
	gh := &ghRunner{stdout: ghView("OPEN", "master")}
	p := New(cfg, prReviewRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}),
		WithPRStageChecks(&fakePRStageChecks{}), WithPullRequestRunner(gh))
	if err := p.Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "t", TargetDir: dir, ApproveGates: true}); err != nil {
		t.Fatalf("three passing pr gates: %v", err)
	}
	if gh.calls != 1 {
		t.Fatalf("the three pr_* gates of one wave must read one pull request answer, got %d lookups", gh.calls)
	}
	history, err := gatesched.AllAttempts(gateEvents(t, dir), "implementation")
	if err != nil || len(history) != 3 {
		t.Fatalf("each gate must have its own attempt: %d, %v", len(history), err)
	}
}

func TestCodeGates_RequiredPhrasesFailReasonNamesMissingPhrase(t *testing.T) {
	ref := codeGate(gatelib.GateRequiredPhrases, map[string]any{
		"phrases": []any{"Критерии приёмки", "Метрики"}, "case": "insensitive", "scope": "result", "match": "all",
	})
	got := runCodeGate(t, ref, "md", "# Спека\n\nМетрики есть\n", &ghRunner{}, nil)
	requireOutcome(t, got, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	if !strings.Contains(got.reason, "Критерии приёмки") || strings.Contains(got.reason, "Метрики") {
		t.Fatalf("the reason must name exactly the missing phrase: %q", got.reason)
	}
}

// Acceptance: a specification without "Критерии приёмки" is blocked by the code
// gate before the LLM gate and before the human; adding the section makes a new
// version, all groups run again from the first and the chain reaches the human.
func TestCodeGates_SpecWithoutAcceptanceCriteriaIsBlockedBeforeLLMAndHuman(t *testing.T) {
	dir := env(t)
	rt := newScripted()
	rt.content["tech"] = map[string]string{"result": "# Спека\n\nЦели без критериев\n"}
	rt.content["spec-checker"] = map[string]string{"spec-check": "# Проверка\n\n**Verdict:** PASSED\n"}
	cfg := stageCheckConfig(nil)
	cfg.Stages[0].Confirm = ""
	cfg.Stages[0].ExitGateGroups = workflow.GateGroups{
		{codeGate(gatelib.GateRequiredSections, map[string]any{"sections": []any{"Критерии приёмки"}})},
		{codeGate(gatelib.GateAgentReview, map[string]any{"agent": "spec-checker"})},
		{codeGate(gatelib.GateHumanApproval, nil)},
	}
	newPipeline := func() *Pipeline {
		return New(cfg, stageCheckRegistry(true), WithRuntimeFactory(rt.factory), WithNotifier(&captureNotifier{}), WithPrompter(&scriptedPrompter{}))
	}
	err := newPipeline().Run(context.Background(), RunConfig{Feature: "feat", TaskDesc: "спека", TargetDir: dir})
	if err == nil || !errors.Is(err, ErrUserStopped) {
		t.Fatalf("a spec without the section must be blocked: %v", err)
	}
	if strings.Join(rt.executed, ",") != "tech" {
		t.Fatalf("the LLM gate must not start before the code gate passed: %v", rt.executed)
	}
	firstEvents := gateEvents(t, dir)
	if countEvents(firstEvents, gatesched.EventAttemptStarted, gatelib.GateAgentReview) != 0 || countEvents(firstEvents, gatesched.EventAttemptStarted, gatelib.GateHumanApproval) != 0 {
		t.Fatal("neither the LLM nor the human gate may have an attempt")
	}
	first, _ := gatesched.AllAttempts(firstEvents, "tech")
	if len(first) != 1 || first[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("the code gate must have failed: %+v", first)
	}

	runID := filepath.Base(onlyRunDir(t, dir))
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending := findCheckDecision(t, dir, approval.StageCheckTrigger)
	if _, err := store.Decide(runID, pending.ID, approval.Decision{
		ActorID: "local-user", ActorRole: "architect", Action: "new_version",
		SubjectHash: pending.SubjectHash, DecidedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("new_version: %v", err)
	}

	rt.content["tech"] = map[string]string{"result": "# Спека\n\nЦели\n\n## Критерии приёмки\n\n- метрика X\n"}
	err = newPipeline().Run(context.Background(), RunConfig{TargetDir: dir, ResumeRunID: runID})
	var needsHuman *ApprovalRequiredError
	if err != nil && !errors.As(err, &needsHuman) {
		t.Fatalf("the new version must pass the code gate and reach the human: %v", err)
	}
	if !strings.Contains(strings.Join(rt.executed, ","), "spec-checker") {
		t.Fatalf("the LLM gate must run on the new version: %v", rt.executed)
	}
	all, _ := gatesched.AllAttempts(gateEvents(t, dir), "tech")
	byGate := map[string][]workflow.GateAttempt{}
	for _, attempt := range all {
		byGate[attempt.GateName] = append(byGate[attempt.GateName], attempt)
	}
	sections := byGate[gatelib.GateRequiredSections]
	if len(sections) != 2 || sections[0].Verdict != workflow.GateVerdictFail || sections[1].Verdict != workflow.GateVerdictPass {
		t.Fatalf("the code gate must have a failed and then a passed attempt: %+v", sections)
	}
	if sections[0].ArtifactSHA256 == sections[1].ArtifactSHA256 {
		t.Fatal("adding the section must create a new result version")
	}
	if len(byGate[gatelib.GateAgentReview]) != 1 || byGate[gatelib.GateAgentReview][0].ArtifactSHA256 != sections[1].ArtifactSHA256 {
		t.Fatalf("the LLM gate must check the new version: %+v", byGate[gatelib.GateAgentReview])
	}
}
