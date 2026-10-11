package stagecheck

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunHardRequiredSections(t *testing.T) {
	pass := RunHard(context.Background(), HardInput{
		Stage: "product_spec", ResultType: "md",
		Content:          "# Спека\n\n## Критерии приёмки\n\n- один\n",
		RequiredSections: []string{"Критерии приёмки"},
	})
	if pass.Status != StatusPassed {
		t.Fatalf("expected passed, got %+v", pass)
	}

	fail := RunHard(context.Background(), HardInput{
		Stage: "product_spec", ResultType: "md",
		Content:          "# Спека\n\nбез нужного заголовка\n",
		RequiredSections: []string{"Критерии приёмки"},
	})
	if fail.Status != StatusFailed {
		t.Fatalf("expected failed, got %+v", fail)
	}
}

func TestRunHardVerdictMarker(t *testing.T) {
	pass := RunHard(context.Background(), HardInput{
		Stage: "code_review", ResultType: "md",
		Content: "## Ревью\n\n**Verdict:** APPROVED\n",
		Rules:   []string{"verdict_marker"},
	})
	if pass.Status != StatusPassed {
		t.Fatalf("expected passed, got %+v", pass)
	}

	fail := RunHard(context.Background(), HardInput{
		Stage: "code_review", ResultType: "md",
		Content: "## Ревью\n\nнет маркера\n",
		Rules:   []string{"verdict_marker"},
	})
	if fail.Status != StatusFailed {
		t.Fatalf("expected failed, got %+v", fail)
	}
}

func TestRunHardUnimplementedRuleIsError(t *testing.T) {
	report := RunHard(context.Background(), HardInput{
		Stage: "implementation", ResultType: "link",
		Reference: "https://github.com/example/repo/pull/1",
		Rules:     []string{"project_checks"},
	})
	if report.Status != StatusError {
		t.Fatalf("expected error for unimplemented rule, got %+v", report)
	}
}

func TestRunHardPullRequestRules(t *testing.T) {
	var gotReference, gotBase string
	report := RunHard(context.Background(), HardInput{
		Stage: "implementation", ResultType: "link",
		Reference:          "https://github.com/example/repo/pull/7",
		ExpectedBaseBranch: "main",
		Rules:              []string{"pr_exists", "pr_open", "pr_base_branch"},
		PRCheck: func(_ context.Context, reference, base string) ([]RuleResult, error) {
			gotReference, gotBase = reference, base
			return []RuleResult{
				{Rule: "pr_exists", Status: RulePassed},
				{Rule: "pr_open", Status: RulePassed},
				{Rule: "pr_base_branch", Status: RuleFailed, Reason: "base is develop"},
			}, nil
		},
	})
	if gotReference == "" || gotBase != "main" {
		t.Fatalf("pr check received reference=%q base=%q", gotReference, gotBase)
	}
	if report.Status != StatusFailed {
		t.Fatalf("expected failed on pr_base_branch, got %+v", report)
	}
}

func TestRunHardPullRequestWithoutReference(t *testing.T) {
	report := RunHard(context.Background(), HardInput{
		Stage: "implementation", ResultType: "link",
		Rules: []string{"pr_exists"},
		PRCheck: func(context.Context, string, string) ([]RuleResult, error) {
			t.Fatal("pr check must not run without a reference")
			return nil, nil
		},
	})
	if report.Status != StatusFailed {
		t.Fatalf("expected failed without reference, got %+v", report)
	}
}

func TestRunHardPullRequestError(t *testing.T) {
	report := RunHard(context.Background(), HardInput{
		Stage: "implementation", ResultType: "link",
		Reference: "https://github.com/example/repo/pull/7",
		Rules:     []string{"pr_exists"},
		PRCheck: func(context.Context, string, string) ([]RuleResult, error) {
			return nil, errors.New("gh unavailable")
		},
	})
	if report.Status != StatusError {
		t.Fatalf("expected error when pr check fails, got %+v", report)
	}
}

func TestRunAgentParsesCanonicalMarkers(t *testing.T) {
	cases := map[string]string{
		"**Verdict:** PASSED\n":            StatusPassed,
		"**Verdict:** FAILED\n":            StatusFailed,
		"Verdict: PASSED\n":                StatusPassed,
		"Verdict: FAILED\n":                StatusFailed,
		"**Verdict:** APPROVED\n":          StatusPassed,
		"**Verdict:** CHANGES_REQUESTED\n": StatusFailed,
		"## Отчёт\n\n**Result:** PASS\n":   StatusPassed,
		"## Отчёт\n\n**Result:** FAIL\n":   StatusFailed,
	}
	for content, want := range cases {
		got := RunAgent("tech_design", "verifier", content)
		if got.Status != want {
			t.Fatalf("RunAgent(%q) status=%q want=%q", content, got.Status, want)
		}
	}
}

func TestRunAgentRejectsMissingVerdict(t *testing.T) {
	report := RunAgent("tech_design", "verifier", "# Отчёт\n\nбез вердикта\n")
	if report.Status != StatusError {
		t.Fatalf("expected error for missing verdict, got %+v", report)
	}
}

func TestParseVerdictIgnoresDataRegions(t *testing.T) {
	content := "```\nVerdict: PASSED\n```\n\nVerdict: FAILED\n"
	if value, ok := ParseVerdict(content); !ok || value != "FAILED" {
		t.Fatalf("expected FAILED outside fenced block, got %q ok=%t", value, ok)
	}
}

func TestReportMarkdown(t *testing.T) {
	report := Report{
		Stage: "implementation", Kind: KindHard, Status: StatusFailed,
		Rules: []RuleResult{{Rule: "pr_open", Status: RuleFailed, Reason: "closed"}},
	}
	rendered := string(report.Markdown())
	for _, want := range []string{"# Проверка этапа implementation", "- Итог: failed", "pr_open: failed — closed"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, rendered)
		}
	}
}
