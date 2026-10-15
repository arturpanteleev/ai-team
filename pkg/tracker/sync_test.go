package tracker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

var testMaps = Maps{
	OnEnter: map[string]string{"implementation": "In Progress"},
	OnPass:  map[string]string{"implementation": "In Review", "qa": "Done"},
}

func finished(stage string, data map[string]any) TaskEvent {
	return TaskEvent{Cursor: 7, RunID: "run-1", TaskID: 3, Type: EventAttemptFinished, Stage: stage, AttemptID: "att-1", Data: data}
}

func TestPlanOnEnterTransitionsOnlyMappedStages(t *testing.T) {
	ops := Plan(TaskEvent{Type: EventAttemptStarted, Stage: "implementation", Cursor: 1, RunID: "r"}, testMaps, "https://x/pipelines/3")
	if len(ops) != 1 || ops[0].Kind != OpTransition || ops[0].Target != "In Progress" {
		t.Fatalf("ops=%+v", ops)
	}
	if ops := Plan(TaskEvent{Type: EventAttemptStarted, Stage: "qa"}, testMaps, ""); len(ops) != 0 {
		t.Fatalf("unmapped stage entry must plan nothing, got %+v", ops)
	}
}

func TestPlanOnPassTransitionsOnlyAfterRealPass(t *testing.T) {
	passed := map[string]any{"status": "passed", "outcome": "passed", "decision": "approved"}
	ops := Plan(finished("qa", passed), testMaps, "u")
	if len(ops) != 2 || ops[0].Kind != OpTransition || ops[0].Target != "Done" || ops[1].Kind != OpComment {
		t.Fatalf("passed ops=%+v", ops)
	}
	for name, data := range map[string]map[string]any{
		"failed":       {"status": "failed", "outcome": "failed"},
		"rejected":     {"status": "passed", "outcome": "passed", "decision": "rejected"},
		"skipped":      {"status": "skipped", "outcome": "skipped"},
		"warning":      {"status": "passed", "outcome": "warning", "decision": "waived"},
		"missing":      {},
		"outcome only": {"outcome": "passed"},
	} {
		ops := Plan(finished("qa", data), testMaps, "u")
		if len(ops) != 1 || ops[0].Kind != OpComment {
			t.Errorf("%s: must only comment, got %+v", name, ops)
			continue
		}
		body := strings.Join(ops[0].Comment.Lines, "\n")
		if strings.Contains(body, "outcome=passed") && name != "rejected" && name != "outcome only" {
			t.Errorf("%s: comment presents the stage as passed: %s", name, body)
		}
		if !strings.Contains(body, "did not pass") {
			t.Errorf("%s: comment must say the stage did not pass: %s", name, body)
		}
	}
}

func TestPlanOverrideIsCommentOnlyAndNeverPass(t *testing.T) {
	event := TaskEvent{Type: EventCheckOverridden, Stage: "qa", Cursor: 9, RunID: "r", Data: map[string]any{"reason": "hotfix", "check_status": "failed"}}
	ops := Plan(event, testMaps, "u")
	if len(ops) != 1 || ops[0].Kind != OpComment {
		t.Fatalf("ops=%+v", ops)
	}
	body := strings.Join(ops[0].Comment.Lines, "\n")
	if !strings.Contains(body, "outcome=overridden") || strings.Contains(body, "outcome=passed") || !strings.Contains(body, "reason=hotfix") {
		t.Fatalf("override comment=%s", body)
	}
}

func TestClassifyOutcomeIsExtensible(t *testing.T) {
	if got := ClassifyOutcome(EventAttemptFinished, map[string]any{"status": "failed", "outcome": "advisory_failed"}); got != "advisory_failed" || got == OutcomePassed {
		t.Fatalf("unknown future outcome must pass through, not become a pass: %q", got)
	}
	if got := ClassifyOutcome("something_else", nil); got != OutcomeUnknown {
		t.Fatalf("got %q", got)
	}
}

func TestEventKeyIsStableAcrossReplays(t *testing.T) {
	a := Plan(finished("qa", map[string]any{"status": "passed", "outcome": "passed"}), testMaps, "u")
	b := Plan(finished("qa", map[string]any{"status": "passed", "outcome": "passed"}), testMaps, "u")
	if a[1].Comment.Key != "run-1#7" || !reflect.DeepEqual(a[1].Comment, b[1].Comment) {
		t.Fatalf("comment key=%q", a[1].Comment.Key)
	}
}

func TestExecuteAppliesEventOnceOnFakeAndDoesNotDuplicateOnReplay(t *testing.T) {
	fake := NewFake(nil, FakeIssueSeed{Key: "APP-1"})
	ctx := context.Background()
	enter := Plan(TaskEvent{Type: EventAttemptStarted, Stage: "implementation", Cursor: 1, RunID: "r"}, testMaps, "u")
	pass := Plan(finished("implementation", map[string]any{"status": "passed", "outcome": "passed"}), testMaps, "u")
	for round := 0; round < 2; round++ {
		if err := Execute(ctx, fake, "APP-1", enter); err != nil {
			t.Fatal(err)
		}
		if err := Execute(ctx, fake, "APP-1", pass); err != nil {
			t.Fatal(err)
		}
	}
	if status, _ := fake.Status("APP-1"); status != "In Review" {
		t.Fatalf("status=%s", status)
	}
	if comments := fake.Comments("APP-1"); len(comments) != 1 {
		t.Fatalf("replay duplicated the comment: %v", comments)
	}
	var transitions []string
	for _, call := range fake.Calls() {
		if call.Op == "transition" {
			transitions = append(transitions, call.Arg)
		}
	}
	if want := []string{"In Progress", "In Review", "In Progress", "In Review"}; !reflect.DeepEqual(transitions, want) {
		t.Fatalf("transition calls=%v", transitions)
	}
}

func TestExecuteStillCommentsWhenTransitionFailsAndReportsBoth(t *testing.T) {
	fake := NewFake(nil, FakeIssueSeed{Key: "APP-1"})
	boom := errors.New("tracker is down")
	fake.FailNext("transition", boom, 1)
	pass := Plan(finished("qa", map[string]any{"status": "passed", "outcome": "passed"}), testMaps, "u")
	err := Execute(context.Background(), fake, "APP-1", pass)
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if len(fake.Comments("APP-1")) != 1 {
		t.Fatal("the outcome comment must still be posted when the transition fails")
	}
	if err := Execute(context.Background(), fake, "APP-1", pass); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if status, _ := fake.Status("APP-1"); status != "Done" || len(fake.Comments("APP-1")) != 1 {
		t.Fatalf("after retry status=%s comments=%d", status, len(fake.Comments("APP-1")))
	}
}

func TestRegistryOpensConfiguredAdapterByName(t *testing.T) {
	registry := NewRegistry()
	fake, err := registry.Open(&config.TrackerConfig{Fake: &config.FakeTrackerConfig{Issues: []config.FakeIssueConfig{{Key: "APP-5", Status: "Done"}}}}, Deps{})
	if err != nil || fake.Name() != "fake" {
		t.Fatalf("fake=%v err=%v", fake, err)
	}
	if issue, err := fake.GetIssue(context.Background(), "APP-5"); err != nil || issue.Status != "Done" {
		t.Fatalf("seeded issue=%+v err=%v", issue, err)
	}
	jira, err := registry.Open(&config.TrackerConfig{Adapter: "jira", Jira: &config.JiraConfig{BaseURL: "https://j.example", TokenEnv: "T"}, Fake: &config.FakeTrackerConfig{}},
		Deps{Getenv: func(string) string { return "tok" }})
	if err != nil || jira.Name() != "jira" {
		t.Fatalf("jira=%v err=%v", jira, err)
	}
	if _, err := registry.Open(&config.TrackerConfig{Adapter: "nope"}, Deps{}); err == nil {
		t.Fatal("unknown adapter must be an error")
	}
	if none, err := registry.Open(nil, Deps{}); err != nil || none != nil {
		t.Fatalf("no config must give no tracker, got %v %v", none, err)
	}
	custom := NewRegistry()
	custom.Register("memo", func(*config.TrackerConfig, Deps) (Tracker, error) { return NewFake(nil), nil })
	if names := custom.Names(); !reflect.DeepEqual(names, []string{"fake", "jira", "memo"}) {
		t.Fatalf("names=%v", names)
	}
}

func TestFakeSimulatesExternalChangesWithCursor(t *testing.T) {
	fake := NewFake(nil, FakeIssueSeed{Key: "APP-1"}, FakeIssueSeed{Key: "APP-2"})
	ctx := context.Background()
	start, _ := fake.Changes(ctx, ChangeQuery{})
	_ = fake.ExternalTransition("APP-1", "Done", "alice")
	_ = fake.ExternalTransition("APP-2", "In Review", "bob")
	page, err := fake.Changes(ctx, ChangeQuery{Cursor: start.Cursor, Keys: []string{"APP-1"}})
	if err != nil || len(page.Changes) != 1 || page.Changes[0].Origin != OriginExternal || page.Changes[0].Author != "alice" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	all, _ := fake.Changes(ctx, ChangeQuery{Cursor: start.Cursor, Limit: 1})
	rest, _ := fake.Changes(ctx, ChangeQuery{Cursor: all.Cursor})
	if len(all.Changes) != 1 || len(rest.Changes) != 1 || all.Changes[0].ID == rest.Changes[0].ID {
		t.Fatalf("limit paging all=%+v rest=%+v", all, rest)
	}
}
