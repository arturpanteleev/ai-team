package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/web"
	webstore "github.com/arturpanteleev/ai-team/pkg/web/store"
)

type runHookHits struct {
	mu     sync.Mutex
	events []int64
	types  []string
}

func (h *runHookHits) snapshot() ([]int64, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.events...), append([]string(nil), h.types...)
}

func newRunHook(t *testing.T, status int) (*httptest.Server, *runHookHits) {
	t.Helper()
	hits := &runHookHits{}
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EventID int64  `json:"event_id"`
			Type    string `json:"type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits.mu.Lock()
		hits.events = append(hits.events, body.EventID)
		hits.types = append(hits.types, body.Type)
		hits.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(hook.Close)
	return hook, hits
}

func runNotificationConfig(events ...string) *config.Config {
	return &config.Config{
		Stages: []config.TemplateStage{{ID: "qa", Function: "qa"}},
		Notifications: &config.NotificationsConfig{Channels: []config.NotificationChannelConfig{{
			Name: "ops-hook", Type: config.NotificationWebhook, URLEnv: "RUN_NOTIFY_URL", Events: events,
		}}},
	}
}

func runHookEnv(hook *httptest.Server) func(string) string {
	return func(name string) string {
		if name == "RUN_NOTIFY_URL" {
			return hook.URL + "/hooks/SECRET"
		}
		return ""
	}
}

func openRunStore(t *testing.T, path string) *webstore.Store {
	t.Helper()
	s, err := webstore.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendRunEvent(t *testing.T, s *webstore.Store, runID, eventType string) int64 {
	t.Helper()
	event := &webstore.Event{RunID: runID, Type: eventType, Stage: "qa", Severity: "warning", Timestamp: time.Now().UTC(), DataJSON: `{}`}
	if err := s.AppendEventNext(event); err != nil {
		t.Fatal(err)
	}
	return event.ID
}

func TestRunNotificationsDeliverOwnRunEventsOnce(t *testing.T) {
	hook, hits := newRunHook(t, http.StatusNoContent)
	cfg := runNotificationConfig("stage_stalled")
	path := filepath.Join(t.TempDir(), "web.db")
	s := openRunStore(t, path)

	first := startRunNotifications(cfg, s, runHookEnv(hook), hook.Client())
	if first == nil {
		t.Fatal("configured notifications must start a worker")
	}
	first.recorder(web.NewStoreRecorder(s)).RunStarted("run-a", "feature", "{}", time.Now().UTC())
	delivered := appendRunEvent(t, s, "run-a", "stage_stalled")
	appendRunEvent(t, s, "run-a", "check_error")       // not subscribed
	appendRunEvent(t, s, "other-run", "stage_stalled") // another process's run
	first.finish(10 * time.Second)
	if events, _ := hits.snapshot(); len(events) != 1 || events[0] != delivered {
		t.Fatalf("first run hits=%v, want [%d]", events, delivered)
	}

	// A repeated `ai-team run --resume` is a new process over the same
	// database: replaying an already delivered event must not notify again.
	second := startRunNotifications(cfg, s, runHookEnv(hook), hook.Client())
	second.recorder(web.NewStoreRecorder(s)).RunResumed("run-a", time.Now().UTC())
	stored, err := s.GetEventsAfter(delivered-1, 1)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
	if !second.worker.Deliver(context.Background(), stored[0]) {
		t.Fatal("replayed delivered event must be acknowledged")
	}
	next := appendRunEvent(t, s, "run-a", "stage_stalled")
	second.finish(10 * time.Second)
	if events, _ := hits.snapshot(); len(events) != 2 || events[1] != next {
		t.Fatalf("resumed run hits=%v, want [%d %d]", events, delivered, next)
	}
}

func TestRunAndWebOnOneDatabaseDoNotDuplicate(t *testing.T) {
	hook, hits := newRunHook(t, http.StatusNoContent)
	cfg := runNotificationConfig("stage_stalled")
	dir := t.TempDir()
	path := filepath.Join(dir, "web.db")
	dispatcher := web.NotificationDispatcherFromConfig(cfg, runHookEnv(hook), hook.Client())
	server, err := web.NewServer(path, "", dir, web.WithTargetDir(dir), web.WithNotifications(dispatcher))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	runStore := openRunStore(t, path) // the run process opens its own handle
	run := startRunNotifications(cfg, runStore, runHookEnv(hook), hook.Client())
	run.recorder(web.NewStoreRecorder(runStore)).RunStarted("shared-run", "feature", "{}", time.Now().UTC())
	var last int64
	for range 5 {
		last = appendRunEvent(t, runStore, "shared-run", "stage_stalled")
	}
	run.finish(10 * time.Second)

	// Wait until the web worker acknowledged the same events: it either sent
	// them itself or saw them already handled by the run.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	for {
		cursor, _, err := server.Store().IntegrationCursor("notifier")
		if err == nil && cursor >= last {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("web notifier cursor did not reach %d", last)
		}
	}
	events, _ := hits.snapshot()
	seen := map[int64]int{}
	for _, id := range events {
		seen[id]++
	}
	if len(events) != 5 || len(seen) != 5 {
		t.Fatalf("hits=%v, want each of 5 events exactly once", events)
	}
}

func TestRunNotificationFailureDoesNotBlockRun(t *testing.T) {
	hook, hits := newRunHook(t, http.StatusServiceUnavailable)
	cfg := runNotificationConfig("stage_stalled", "integration_error", "run_finished")
	s := openRunStore(t, filepath.Join(t.TempDir(), "web.db"))
	n := startRunNotifications(cfg, s, runHookEnv(hook), hook.Client())
	recorder := n.recorder(web.NewStoreRecorder(s))
	recorder.RunStarted("broken-run", "feature", "{}", time.Now().UTC())
	source := appendRunEvent(t, s, "broken-run", "stage_stalled")
	recorder.RunFinished("broken-run", "completed", time.Now().UTC())
	n.finish(10 * time.Second)

	run, err := s.GetPipelineRunByRunID("broken-run")
	if err != nil || run == nil || run.Status != "completed" {
		t.Fatalf("channel failure changed the run: %+v err=%v", run, err)
	}
	events, err := s.GetEventsAfter(source, 100)
	if err != nil {
		t.Fatal(err)
	}
	var integrationErrors int
	for _, event := range events {
		if event.Type == "integration_error" {
			integrationErrors++
			var data map[string]any
			_ = json.Unmarshal([]byte(event.DataJSON), &data)
			if data["integration"] != "notifier" || data["channel"] != "ops-hook" {
				t.Fatalf("integration error=%v", data)
			}
		}
	}
	// stage_stalled and run_finished each fail once; the failures themselves
	// are never dispatched.
	_, types := hits.snapshot()
	if integrationErrors != 2 || len(types) != 2 {
		t.Fatalf("integration_errors=%d hits=%v", integrationErrors, types)
	}
}

func TestRunWithoutNotificationsConfigIsUnchanged(t *testing.T) {
	s := openRunStore(t, filepath.Join(t.TempDir(), "web.db"))
	if n := startRunNotifications(&config.Config{}, s, func(string) string { return "" }, nil); n != nil {
		t.Fatal("no notifications config must not start a worker")
	}
	if n := startRunNotifications(runNotificationConfig("stage_stalled"), nil, func(string) string { return "" }, nil); n != nil {
		t.Fatal("without the SQLite store there is no delivery log, so no worker")
	}
	var none *runNotifications
	none.finish(time.Second) // a nil worker is a no-op
	none.track("run")
}

func writeSchedulerNotificationConfig(t *testing.T, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "schema_version: 5\ntemplate: sched-test\ntitle: Scheduler notifications\nstages:\n" +
		"  - id: analyst\n    title: Analyst\n    function: po\n    result: md\n    executor: human\n" +
		"notifications:\n  channels:\n    - name: ops-hook\n      type: webhook\n      url_env: RUN_NOTIFY_URL\n      events: [stage_stalled]\n"
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scheduler-worker reads the project config and delivers events of any run in
// its database through its own durable cursor.
func TestSchedulerWorkerDeliversNotificationsFromProjectConfig(t *testing.T) {
	hook, hits := newRunHook(t, http.StatusNoContent)
	target := t.TempDir()
	writeSchedulerNotificationConfig(t, target)
	registry, err := newAgentRegistryWithPaths(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := openRunStore(t, filepath.Join(t.TempDir(), "web.db"))
	stopped := false
	cleanup := startSchedulerNotifications(context.Background(), target, registry, s, runHookEnv(hook), hook.Client(), func() { stopped = true })
	if _, found, err := s.IntegrationCursor(schedulerNotifierIntegration); err != nil || !found {
		t.Fatalf("scheduler-worker must establish its durable cursor at start: found=%v err=%v", found, err)
	}
	delivered := appendRunEvent(t, s, "queued-run", "stage_stalled")
	appendRunEvent(t, s, "queued-run", "check_error") // not subscribed
	cleanup()
	if !stopped {
		t.Fatal("cleanup must restore default signal handling")
	}
	if events, _ := hits.snapshot(); len(events) != 1 || events[0] != delivered {
		t.Fatalf("scheduler hits=%v, want [%d]", events, delivered)
	}
	if cursor, _, err := s.IntegrationCursor(schedulerNotifierIntegration); err != nil || cursor < delivered {
		t.Fatalf("cursor=%d err=%v, want at least %d", cursor, err, delivered)
	}
}

// The flush runs after the first Ctrl-C. Signal handling must be restored
// before it starts, so a second Ctrl-C can interrupt a slow flush: here the
// channel answers only once stopSignals has been called.
func TestSchedulerWorkerRestoresSignalsBeforeFlush(t *testing.T) {
	released := make(chan struct{})
	var deliveredOnce sync.Once
	delivered := make(chan struct{})
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-released:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusNoContent)
		deliveredOnce.Do(func() { close(delivered) })
	}))
	defer hook.Close()
	target := t.TempDir()
	writeSchedulerNotificationConfig(t, target)
	registry, err := newAgentRegistryWithPaths(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := openRunStore(t, filepath.Join(t.TempDir(), "web.db"))
	cleanup := startSchedulerNotifications(context.Background(), target, registry, s, runHookEnv(hook), hook.Client(), func() { close(released) })
	appendRunEvent(t, s, "queued-run", "stage_stalled")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		cleanup()
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("flush waited on the channel: signals were not restored before flushing")
	}
	select {
	case <-delivered:
	default:
		t.Fatal("flush must deliver the pending event")
	}
}

func TestSchedulerWorkerWithoutNotificationsStillRestoresSignals(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".ai-team"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := newAgentRegistryWithPaths(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := openRunStore(t, filepath.Join(t.TempDir(), "web.db"))
	stopped := false
	startSchedulerNotifications(context.Background(), target, registry, s, func(string) string { return "" }, nil, func() { stopped = true })()
	if !stopped {
		t.Fatal("cleanup must call stopSignals even when notifications are disabled")
	}
	if _, found, _ := s.IntegrationCursor(schedulerNotifierIntegration); found {
		t.Fatal("disabled notifications must not create a cursor")
	}
}
