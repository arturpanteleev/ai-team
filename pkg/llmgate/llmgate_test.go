package llmgate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseMarker(t *testing.T) {
	if v, err := Parse("", "текст\n\n**Verdict:** PASSED\n"); err != nil || !v.Pass {
		t.Fatalf("pass: %+v %v", v, err)
	}
	if v, err := Parse(SchemaMarker, "Verdict: FAILED"); err != nil || v.Pass {
		t.Fatalf("fail: %+v %v", v, err)
	}
	// A marker inside a code block is data, not a verdict.
	if _, err := Parse(SchemaMarker, "```\nVerdict: PASSED\n```\n"); !errors.Is(err, ErrSchema) {
		t.Fatalf("marker in a code block must violate the schema: %v", err)
	}
	if _, err := Parse(SchemaMarker, "нет вердикта"); !errors.Is(err, ErrSchema) {
		t.Fatalf("missing marker: %v", err)
	}
}

func TestParseJSON(t *testing.T) {
	cases := []struct {
		name, report string
		wantErr      string
		pass         bool
	}{
		{name: "bare pass", report: `{"verdict":"pass"}`, pass: true},
		{name: "fenced fail", report: "Итог:\n```json\n{\"verdict\":\"fail\",\"reason\":\"нет критериев\"}\n```\n", pass: false},
		{name: "fail with findings only", report: `{"verdict":"fail","findings":[{"severity":"major","message":"пробел"}]}`},
		{name: "empty", report: "", wantErr: "не найден"},
		{name: "prose", report: "всё хорошо", wantErr: "не найден"},
		{name: "unknown field", report: `{"verdict":"pass","score":5}`, wantErr: "unknown field"},
		{name: "missing verdict", report: `{"reason":"x"}`, wantErr: "verdict обязательно"},
		{name: "bad verdict", report: `{"verdict":"maybe"}`, wantErr: "ожидается pass или fail"},
		{name: "fail without reason", report: `{"verdict":"fail"}`, wantErr: "требует reason"},
		{name: "pass with blocker", report: `{"verdict":"pass","findings":[{"severity":"blocker","message":"x"}]}`, wantErr: "противоречит"},
		{name: "bad severity", report: `{"verdict":"fail","findings":[{"severity":"huge","message":"x"}]}`, wantErr: "severity"},
		{name: "two blocks", report: "```json\n{\"verdict\":\"pass\"}\n```\n```json\n{\"verdict\":\"fail\",\"reason\":\"x\"}\n```", wantErr: "несколько"},
		{name: "trailing data", report: `{"verdict":"pass"} {"verdict":"fail"}`, wantErr: "лишние"},
		{name: "unclosed block", report: "```json\n{\"verdict\":\"pass\"}", wantErr: "не закрыт"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Parse(SchemaJSON, tc.report)
			if tc.wantErr != "" {
				if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want schema error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || v.Pass != tc.pass {
				t.Fatalf("verdict %+v err %v", v, err)
			}
		})
	}
}

func TestRunRetriesTechnicalFailuresOnly(t *testing.T) {
	calls := 0
	res := Run(context.Background(), Spec{Schema: SchemaJSON, MaxAttempts: 3}, func(_ context.Context, n int) (string, error) {
		calls++
		switch n {
		case 1:
			return "", errors.New("runtime refused")
		case 2:
			return "не json", nil
		}
		return `{"verdict":"pass"}`, nil
	})
	if res.Status != StatusPass || calls != 3 || len(res.Trace) != 3 {
		t.Fatalf("status %s calls %d trace %+v", res.Status, calls, res.Trace)
	}
	if res.Trace[0].Class != ClassRuntime || res.Trace[1].Class != ClassSchema || res.Trace[2].Class != ClassVerdict {
		t.Fatalf("trace classes: %+v", res.Trace)
	}
}

func TestRunNegativeVerdictIsNotRetried(t *testing.T) {
	calls := 0
	res := Run(context.Background(), Spec{Schema: SchemaMarker, MaxAttempts: 5}, func(context.Context, int) (string, error) {
		calls++
		return "Verdict: FAILED", nil
	})
	if res.Status != StatusFail || calls != 1 {
		t.Fatalf("a reviewer's negative opinion must end the gate: %s, %d calls", res.Status, calls)
	}
}

func TestRunNoValidVerdictIsError(t *testing.T) {
	res := Run(context.Background(), Spec{MaxAttempts: 2}, func(context.Context, int) (string, error) {
		return "ничего определённого", nil
	})
	if res.Status != StatusError || len(res.Trace) != 2 || !strings.Contains(res.Reason, "попытка 2/2") {
		t.Fatalf("status %s reason %q", res.Status, res.Reason)
	}
}

func TestRunRuntimeRefusalIsError(t *testing.T) {
	res := Run(context.Background(), Spec{}, func(context.Context, int) (string, error) {
		return "Verdict: PASSED", errors.New("exit status 1")
	})
	if res.Status != StatusError {
		t.Fatalf("a failed call must not pass even with a passing report: %s", res.Status)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	res := Run(ctx, Spec{MaxAttempts: 3}, func(context.Context, int) (string, error) {
		cancel()
		return "", context.Canceled
	})
	if res.Status != StatusCanceled || len(res.Trace) != 1 {
		t.Fatalf("status %s trace %+v", res.Status, res.Trace)
	}
}

func TestRunClampsAttempts(t *testing.T) {
	calls := 0
	Run(context.Background(), Spec{MaxAttempts: 99}, func(context.Context, int) (string, error) { calls++; return "", errors.New("x") })
	if calls != MaxAttempts {
		t.Fatalf("calls = %d, want %d", calls, MaxAttempts)
	}
}
