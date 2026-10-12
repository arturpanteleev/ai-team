package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const notificationSecretPath = "/hooks/SECRET-TOKEN"

type webhookHit struct {
	EventID  int64  `json:"event_id"`
	Type     string `json:"type"`
	Stage    string `json:"stage"`
	Function string `json:"function"`
	TaskID   int64  `json:"task_id"`
	TaskURL  string `json:"task_url"`
}

func notificationConfig(events, functions []string) *config.Config {
	return &config.Config{
		Stages: []config.TemplateStage{{ID: "implementation", Function: "developer"}, {ID: "qa", Function: "qa"}},
		Notifications: &config.NotificationsConfig{
			WebBaseURL: "https://ai-team.example",
			Channels: []config.NotificationChannelConfig{{
				Name: "ops-hook", Type: config.NotificationWebhook, URLEnv: "TEST_NOTIFY_URL",
				Events: events, Functions: functions,
			}},
		},
	}
}

func notificationDispatcher(cfg *config.Config, server *httptest.Server) *notifier.Dispatcher {
	return NotificationDispatcherFromConfig(cfg, func(name string) string {
		if name == "TEST_NOTIFY_URL" {
			return server.URL + notificationSecretPath
		}
		return ""
	}, server.Client())
}

func appendStageEvent(t *testing.T, srv *Server, runID, eventType, stage string) int64 {
	t.Helper()
	event := &store.Event{RunID: runID, Type: eventType, Stage: stage, Severity: "warning", Timestamp: time.Now().UTC(), DataJSON: `{"reason":"test"}`}
	if err := srv.store.AppendEventNext(event); err != nil {
		t.Fatal(err)
	}
	return event.ID
}

func nextHit(t *testing.T, hits <-chan webhookHit) webhookHit {
	t.Helper()
	select {
	case hit := <-hits:
		return hit
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for webhook delivery")
		return webhookHit{}
	}
}

// waitNotifierCursor waits until the notifier acknowledged eventID, so a
// shutdown cannot interrupt the delivery bookkeeping under test.
func waitNotifierCursor(t *testing.T, s *store.Store, eventID int64) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		cursor, _, err := s.IntegrationCursor(notifierIntegrationName)
		if err == nil && cursor >= eventID {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("notifier cursor did not reach %d", eventID)
		}
	}
}

func TestNotificationsDeliverSubscribedEventsOnceAcrossReconnect(t *testing.T) {
	hits := make(chan webhookHit, 16)
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hit webhookHit
		_ = json.NewDecoder(r.Body).Decode(&hit)
		hits <- hit
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	cfg := notificationConfig([]string{"stage_stalled"}, []string{"qa"})
	database := filepath.Join(t.TempDir(), "web.db")
	target := t.TempDir()

	first, err := NewServer(database, "", target, WithTargetDir(target), WithNotifications(notificationDispatcher(cfg, hook)))
	if err != nil {
		t.Fatal(err)
	}
	run := createJiraTask(t, first, "notify-run", nil)
	delivered := appendStageEvent(t, first, run.RunID, "stage_stalled", "qa")
	hit := nextHit(t, hits)
	if hit.EventID != delivered || hit.Type != "stage_stalled" || hit.Function != "qa" || hit.TaskID != run.ID ||
		hit.TaskURL != "https://ai-team.example/tasks/notify-run" {
		t.Fatalf("delivered=%+v", hit)
	}
	appendStageEvent(t, first, run.RunID, "stage_stalled", "implementation") // function not subscribed
	appendStageEvent(t, first, run.RunID, "check_error", "qa")               // type not subscribed
	second := appendStageEvent(t, first, run.RunID, "stage_stalled", "qa")
	if hit := nextHit(t, hits); hit.EventID != second {
		t.Fatalf("unsubscribed event was delivered: %+v", hit)
	}
	waitNotifierCursor(t, first.store, second)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a reconnect that rewinds the durable cursor: every event is
	// replayed, but the delivery log must suppress already-sent notifications.
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE integration_cursors SET cursor = 0 WHERE integration = 'notifier'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	restarted, err := NewServer(database, "", target, WithTargetDir(target), WithNotifications(notificationDispatcher(cfg, hook)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	third := appendStageEvent(t, restarted, run.RunID, "stage_stalled", "qa")
	if hit := nextHit(t, hits); hit.EventID != third {
		t.Fatalf("replay after reconnect duplicated event %d", hit.EventID)
	}
	// A repeated live delivery of the same stored event is a no-op as well.
	// Wait for the worker's bookkeeping: the single production worker never
	// handles one event concurrently with itself.
	waitNotifierCursor(t, restarted.store, third)
	stored, err := restarted.store.GetEventsAfter(third-1, 1)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
	if !restarted.deliverNotification(context.Background(), stored[0]) {
		t.Fatal("duplicate delivery should be acknowledged")
	}
	fourth := appendStageEvent(t, restarted, run.RunID, "stage_stalled", "qa")
	if hit := nextHit(t, hits); hit.EventID != fourth {
		t.Fatalf("duplicate live delivery of event %d", hit.EventID)
	}
}

func TestNotificationFailureRecordsSanitizedIntegrationErrorWithoutLoop(t *testing.T) {
	var calls atomic.Int32
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "down "+notificationSecretPath, http.StatusServiceUnavailable)
	}))
	defer hook.Close()
	// Subscribing to integration_error must not make a failing channel notify
	// about its own failures.
	cfg := notificationConfig([]string{"stage_stalled", "integration_error"}, nil)
	target := t.TempDir()
	srv, err := NewServer(":memory:", "", target, WithTargetDir(target), WithNotifications(notificationDispatcher(cfg, hook)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	run := createJiraTask(t, srv, "notify-failure-run", nil)
	completedAt := time.Now().UTC()
	run.Status, run.CompletedAt = "completed", &completedAt
	if err := srv.store.UpdatePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	events := srv.store.SubscribeEvents(context.Background(), 16)

	waitIntegrationErrors := func(want int) []map[string]any {
		t.Helper()
		var found []map[string]any
		deadline := time.After(5 * time.Second)
		for len(found) < want {
			select {
			case event := <-events:
				if event.Type == "integration_error" {
					found = append(found, event.Data)
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %d integration_error events, got %d", want, len(found))
			}
		}
		return found
	}
	source := appendStageEvent(t, srv, run.RunID, "stage_stalled", "qa")
	data := waitIntegrationErrors(1)[0]
	if data["integration"] != "notifier" || data["channel"] != "ops-hook" || data["error"] != "webhook: HTTP 503" ||
		data["source_event"] != "stage_stalled" || data["source_cursor"] != float64(source) {
		t.Fatalf("integration error=%v", data)
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "SECRET-TOKEN") || strings.Contains(string(encoded), strings.TrimPrefix(hook.URL, "https://")) {
		t.Fatalf("integration error leaks destination: %s", encoded)
	}
	appendStageEvent(t, srv, run.RunID, "stage_stalled", "qa")
	waitIntegrationErrors(1)
	if got := calls.Load(); got != 2 {
		t.Fatalf("webhook calls=%d, want 2 (no retry storm, no self-notification)", got)
	}
	unchanged, err := srv.store.GetPipelineRunByRunID(run.RunID)
	if err != nil || unchanged.Status != "completed" {
		t.Fatalf("notification failure changed task: %+v err=%v", unchanged, err)
	}
}

func TestNotificationDispatcherFromConfig(t *testing.T) {
	if NotificationDispatcherFromConfig(&config.Config{}, func(string) string { return "" }, nil) != nil {
		t.Fatal("no notifications config must not create a dispatcher")
	}
	cfg := notificationConfig([]string{"stage_stalled"}, nil)
	cfg.Notifications.Channels = append(cfg.Notifications.Channels,
		config.NotificationChannelConfig{Name: "slack", Type: config.NotificationSlack, URLEnv: "SLACK", Events: []string{"stage_stalled"}},
		config.NotificationChannelConfig{Name: "mail", Type: config.NotificationEmail, SMTPURLEnv: "SMTP", From: "bot@example.test", To: []string{"a@example.test"}, Events: []string{"stage_stalled"}},
	)
	dispatcher := NotificationDispatcherFromConfig(cfg, func(string) string { return "" }, nil)
	if dispatcher == nil || dispatcher.StageFunction("qa") != "qa" || dispatcher.TaskURL("r") != "https://ai-team.example/tasks/r" {
		t.Fatalf("dispatcher=%+v", dispatcher)
	}
}
