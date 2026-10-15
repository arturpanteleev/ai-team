package workflow

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func validAttempt() GateAttempt {
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	return GateAttempt{
		TaskID: "task-1", Stage: "product_spec", Visit: 1, ArtifactSHA256: strings.Repeat("a", 64),
		Gate: "required_sections", GateVersion: 1, GateName: "required_sections", Mode: GateModeRequired,
		AttemptID: "att-1", Executor: GateKindBuiltin, Group: 0,
		Execution: GateExecutionSucceeded, Verdict: GateVerdictPass,
		StartedAt: start, FinishedAt: start.Add(time.Second),
	}
}

func TestGateAttemptKeepsExecutionAndVerdictApart(t *testing.T) {
	if err := validAttempt().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*GateAttempt){
		"succeeded without verdict":   func(a *GateAttempt) { a.Verdict = "" },
		"verdict on error":            func(a *GateAttempt) { a.Execution = GateExecutionError },
		"verdict on timeout":          func(a *GateAttempt) { a.Execution = GateExecutionTimeout },
		"verdict while running":       func(a *GateAttempt) { a.Execution = GateExecutionRunning; a.FinishedAt = time.Time{} },
		"finished without finish":     func(a *GateAttempt) { a.FinishedAt = time.Time{} },
		"unknown execution":           func(a *GateAttempt) { a.Execution = "done" },
		"unknown verdict":             func(a *GateAttempt) { a.Verdict = "ok" },
		"missing artifact hash":       func(a *GateAttempt) { a.ArtifactSHA256 = "" },
		"missing attempt id":          func(a *GateAttempt) { a.AttemptID = "" },
		"unknown executor":            func(a *GateAttempt) { a.Executor = "shell" },
		"zero visit":                  func(a *GateAttempt) { a.Visit = 0 },
		"unfinished with finish time": func(a *GateAttempt) { a.Execution = GateExecutionPending; a.Verdict = "" },
	}
	for name, mutate := range cases {
		attempt := validAttempt()
		mutate(&attempt)
		if err := attempt.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	technical := validAttempt()
	technical.Execution, technical.Verdict = GateExecutionTimeout, ""
	if err := technical.Validate(); err != nil {
		t.Fatalf("timeout without verdict is valid: %v", err)
	}
	if !technical.Execution.Technical() || validAttempt().Execution.Technical() {
		t.Fatal("only error and timeout are retryable technical failures")
	}
}

func TestGateAttemptBlockingMatrix(t *testing.T) {
	type row struct {
		execution GateExecution
		verdict   GateVerdict
		mode      GateMode
		blocking  bool
	}
	for _, r := range []row{
		{GateExecutionSucceeded, GateVerdictPass, GateModeRequired, false},
		{GateExecutionSucceeded, GateVerdictNotApplicable, "", false},
		{GateExecutionSucceeded, GateVerdictFail, GateModeRequired, true},
		{GateExecutionError, "", GateModeRequired, true},
		{GateExecutionTimeout, "", "", true},
		{GateExecutionCanceled, "", GateModeRequired, true},
		{GateExecutionRunning, "", GateModeRequired, true},
		{GateExecutionSucceeded, GateVerdictFail, GateModeAdvisory, false},
		{GateExecutionError, "", GateModeAdvisory, false},
	} {
		attempt := validAttempt()
		attempt.Execution, attempt.Verdict, attempt.Mode = r.execution, r.verdict, r.mode
		if got := attempt.Blocking(); got != r.blocking {
			t.Errorf("%+v: Blocking() = %v", r, got)
		}
	}
}

func TestGateRefYAMLForms(t *testing.T) {
	var groups GateGroups
	doc := `
- [required_sections@1]
- - gate: agent_review
    version: 2
    name: spec_review
    mode: advisory
    params: {agent: reviewer}
  - verdict_marker@3
`
	if err := yaml.Unmarshal([]byte(doc), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0][0].Key() != "required_sections@1" || groups[1][0].Name != "spec_review" || groups[1][0].Mode != GateModeAdvisory || groups[1][1].Version != 3 {
		t.Fatalf("decoded %+v", groups)
	}
	out, err := yaml.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	var again GateGroups
	if err := yaml.Unmarshal(out, &again); err != nil || len(again) != 2 || again[0][0].Key() != "required_sections@1" || again[1][0].Params["agent"] != "reviewer" {
		t.Fatalf("round trip failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "- required_sections@1") {
		t.Fatalf("plain refs must stay compact:\n%s", out)
	}
}

func TestGateGroupsShapeValidation(t *testing.T) {
	groups := GateGroups{
		{{Gate: "a_gate", Version: 1}},
		{},
		{{Gate: "Bad-ID", Version: 1}, {Gate: "a_gate", Version: 1}, {Gate: "b", Version: 0}},
	}
	errs := groups.ValidateShape()
	joined := ""
	for _, err := range errs {
		joined += err.Error() + "\n"
	}
	for _, want := range []string{"exit_gate_groups[1]: группа не может быть пустой", "exit_gate_groups[2][0]", "exit_gate_groups[2][1]: имя гейта \"a_gate\" повторяется", "exit_gate_groups[2][2]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}
