package notifier

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type memoryLog struct {
	outcomes map[string]string
	failRead error
	failSave error
}

func newMemoryLog() *memoryLog { return &memoryLog{outcomes: map[string]string{}} }

func logKey(channel string, eventID int64) string {
	return fmt.Sprintf("%s/%d", channel, eventID)
}

func (l *memoryLog) NotificationRecorded(channel string, eventID int64) (bool, error) {
	if l.failRead != nil {
		return false, l.failRead
	}
	_, ok := l.outcomes[logKey(channel, eventID)]
	return ok, nil
}

func (l *memoryLog) RecordNotification(channel string, eventID int64, outcome string) error {
	if l.failSave != nil {
		return l.failSave
	}
	if _, ok := l.outcomes[logKey(channel, eventID)]; !ok {
		l.outcomes[logKey(channel, eventID)] = outcome
	}
	return nil
}

type fakeChannel struct {
	name string
	sent []Notification
	err  error
}

func (c *fakeChannel) Name() string { return c.name }

func (c *fakeChannel) Send(_ context.Context, n Notification) error {
	c.sent = append(c.sent, n)
	return c.err
}

func noFailures(t *testing.T) FailureRecorder {
	return func(channel, reason string) error {
		t.Fatalf("unexpected failure on %s: %s", channel, reason)
		return nil
	}
}

func TestSubscriptionMatchesTypeAndFunction(t *testing.T) {
	anyStage := Subscription{Events: []string{"stage_stalled"}}
	scoped := Subscription{Events: []string{"stage_stalled"}, Functions: []string{"qa"}}
	cases := []struct {
		subscription Subscription
		n            Notification
		want         bool
	}{
		{anyStage, Notification{Type: "stage_stalled"}, true},
		{anyStage, Notification{Type: "check_error"}, false},
		{scoped, Notification{Type: "stage_stalled", Function: "qa"}, true},
		{scoped, Notification{Type: "stage_stalled", Function: "developer"}, false},
		{scoped, Notification{Type: "stage_stalled"}, false},
	}
	for index, tc := range cases {
		if got := tc.subscription.Matches(tc.n); got != tc.want {
			t.Fatalf("case %d: Matches=%v want %v", index, got, tc.want)
		}
	}
}

func TestKnownEventTypes(t *testing.T) {
	for _, eventType := range []string{"stage_stalled", "check_overridden", "integration_error", "attempt_finished"} {
		if !IsKnownEventType(eventType) {
			t.Fatalf("%s should be known", eventType)
		}
	}
	for _, eventType := range []string{"", "stage_exploded", "STAGE_STALLED"} {
		if IsKnownEventType(eventType) {
			t.Fatalf("%q should be unknown", eventType)
		}
	}
}

func TestDispatcherDeliversOncePerChannelAndEvent(t *testing.T) {
	hook := &fakeChannel{name: "hook"}
	slack := &fakeChannel{name: "slack"}
	dispatcher := NewDispatcher([]Route{
		{Channel: hook, Subscription: Subscription{Events: []string{"stage_stalled"}}},
		{Channel: slack, Subscription: Subscription{Events: []string{"stage_stalled"}, Functions: []string{"qa"}}},
	}, map[string]string{"qa": "qa", "implementation": "developer"}, "https://ai-team.example/")
	log := newMemoryLog()
	event := Notification{EventID: 7, RunID: "run/7", Type: "stage_stalled", Stage: "qa", Function: dispatcher.StageFunction("qa")}
	for range 3 { // live delivery, then two reconnect replays of the same event
		if err := dispatcher.Dispatch(context.Background(), event, log, noFailures(t)); err != nil {
			t.Fatal(err)
		}
	}
	if len(hook.sent) != 1 || len(slack.sent) != 1 {
		t.Fatalf("duplicate deliveries: hook=%d slack=%d", len(hook.sent), len(slack.sent))
	}
	if got := dispatcher.TaskURL("run/7"); got != "https://ai-team.example/tasks/run%2F7" {
		t.Fatalf("task url=%q", got)
	}
	other := Notification{EventID: 8, Type: "stage_stalled", Stage: "implementation", Function: dispatcher.StageFunction("implementation")}
	if err := dispatcher.Dispatch(context.Background(), other, log, noFailures(t)); err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Wants(other) || dispatcher.Wants(Notification{Type: "run_started"}) || (*Dispatcher)(nil).Wants(other) {
		t.Fatal("Wants must mirror subscriptions")
	}
	if len(hook.sent) != 2 || len(slack.sent) != 1 {
		t.Fatalf("function filter ignored: hook=%d slack=%d", len(hook.sent), len(slack.sent))
	}
}

func TestDispatcherRecordsFailureOnceAndIsolatesChannels(t *testing.T) {
	broken := &fakeChannel{name: "broken", err: errors.New("webhook: HTTP 503")}
	healthy := &fakeChannel{name: "healthy"}
	dispatcher := NewDispatcher([]Route{
		{Channel: broken, Subscription: Subscription{Events: []string{"check_error"}}},
		{Channel: healthy, Subscription: Subscription{Events: []string{"check_error"}}},
	}, nil, "")
	log := newMemoryLog()
	var failures []string
	record := func(channel, reason string) error {
		failures = append(failures, channel+": "+reason)
		return nil
	}
	event := Notification{EventID: 3, Type: "check_error"}
	for range 2 {
		if err := dispatcher.Dispatch(context.Background(), event, log, record); err != nil {
			t.Fatal(err)
		}
	}
	if len(broken.sent) != 1 || len(healthy.sent) != 1 || len(failures) != 1 || failures[0] != "broken: webhook: HTTP 503" {
		t.Fatalf("broken=%d healthy=%d failures=%v", len(broken.sent), len(healthy.sent), failures)
	}
	if log.outcomes[logKey("broken", 3)] != DeliveryFailed || log.outcomes[logKey("healthy", 3)] != DeliveryDelivered {
		t.Fatalf("delivery log=%v", log.outcomes)
	}
}

func TestDispatcherKeepsEventPendingWhenFailureCannotBePersisted(t *testing.T) {
	broken := &fakeChannel{name: "broken", err: errors.New("webhook: request failed")}
	dispatcher := NewDispatcher([]Route{{Channel: broken, Subscription: Subscription{Events: []string{"check_error"}}}}, nil, "")
	log := newMemoryLog()
	err := dispatcher.Dispatch(context.Background(), Notification{EventID: 4, Type: "check_error"}, log, func(string, string) error {
		return errors.New("database is locked")
	})
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("want pending error, got %v", err)
	}
	if len(log.outcomes) != 0 {
		t.Fatalf("unpersisted failure must not be marked handled: %v", log.outcomes)
	}
	log.failRead = errors.New("log unavailable")
	if err := dispatcher.Dispatch(context.Background(), Notification{EventID: 4, Type: "check_error"}, log, noFailures(t)); err == nil {
		t.Fatal("unreadable delivery log must keep the event pending")
	}
	if len(broken.sent) != 1 {
		t.Fatalf("channel must not be called without a readable delivery log, sent=%d", len(broken.sent))
	}
}

func TestDispatcherShutdownKeepsEventPending(t *testing.T) {
	broken := &fakeChannel{name: "hook", err: context.Canceled}
	dispatcher := NewDispatcher([]Route{{Channel: broken, Subscription: Subscription{Events: []string{"check_error"}}}}, nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log := newMemoryLog()
	if err := dispatcher.Dispatch(ctx, Notification{EventID: 5, Type: "check_error"}, log, noFailures(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(log.outcomes) != 0 {
		t.Fatalf("interrupted delivery must stay pending: %v", log.outcomes)
	}
}

func TestNotificationSummaryHasNoControlCharacters(t *testing.T) {
	n := Notification{Type: "stage_stalled", TaskID: 12, Stage: "qa\r\nBcc: attacker@example.test", Function: "qa", Severity: "warning", TaskURL: "https://ai-team.example/tasks/r"}
	if strings.ContainsAny(n.Summary(), "\r\n") {
		t.Fatalf("summary contains line breaks: %q", n.Summary())
	}
	if !strings.HasPrefix(n.Summary(), "[ai-team] stage_stalled · task #12 · stage qa") || !strings.HasSuffix(n.Text(), "\nhttps://ai-team.example/tasks/r") {
		t.Fatalf("text=%q", n.Text())
	}
	if (Notification{Type: "run_started", RunID: "r1"}).Text() != "[ai-team] run_started · run r1" {
		t.Fatalf("run summary=%q", (Notification{Type: "run_started", RunID: "r1"}).Text())
	}
}
