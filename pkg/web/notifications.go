package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const notifierIntegrationName = "notifier"

// WithNotifications registers outbound notification channels. Delivery runs in
// a separate worker over the durable event cursor, so a failing channel cannot
// change a task's pipeline result.
func WithNotifications(dispatcher *notifier.Dispatcher) ServerOption {
	return func(server *Server) { server.notifications = dispatcher }
}

// NotificationDispatcherFromConfig builds the configured channels. Destination
// URLs and SMTP credentials are read through getenv by the variable names in
// the template; a missing value becomes a per-delivery integration_error
// rather than a startup failure. client may be nil.
func NotificationDispatcherFromConfig(cfg *config.Config, getenv func(string) string, client *http.Client) *notifier.Dispatcher {
	if cfg == nil || cfg.Notifications == nil || len(cfg.Notifications.Channels) == 0 {
		return nil
	}
	routes := make([]notifier.Route, 0, len(cfg.Notifications.Channels))
	for _, channel := range cfg.Notifications.Channels {
		var destination notifier.Channel
		switch channel.Type {
		case config.NotificationWebhook:
			destination = notifier.NewWebhookChannel(channel.Name, getenv(channel.URLEnv), client)
		case config.NotificationSlack:
			destination = notifier.NewSlackChannel(channel.Name, getenv(channel.URLEnv), client)
		case config.NotificationEmail:
			destination = notifier.NewEmailChannel(channel.Name, getenv(channel.SMTPURLEnv), channel.From, channel.To)
		default:
			continue
		}
		routes = append(routes, notifier.Route{
			Channel:      destination,
			Subscription: notifier.Subscription{Events: channel.Events, Functions: channel.Functions},
		})
	}
	functions := make(map[string]string, len(cfg.Stages))
	for _, stage := range cfg.Stages {
		functions[stage.ID] = stage.Function
	}
	return notifier.NewDispatcher(routes, functions, cfg.Notifications.WebBaseURL)
}

// integrationStartCursor returns where an integration resumes. On first enable
// the cursor starts at the current tail so old history is not replayed.
func integrationStartCursor(s *store.Store, integration string, tail int64) int64 {
	storedCursor, found, err := s.IntegrationCursor(integration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s cursor unavailable; replaying from the beginning: %v\n", integration, err)
		return 0
	}
	if found {
		return storedCursor
	}
	if err := s.SetIntegrationCursor(integration, tail); err != nil {
		fmt.Fprintf(os.Stderr, "%s cursor could not be initialized: %v\n", integration, err)
		// Until the initial position is durable, replay from the beginning.
		// Starting at the tail here would permanently skip events if the
		// process stopped before it could establish the subscription cursor.
		return 0
	}
	return tail
}

// deliverNotification sends one durable event to subscribed channels. The
// per-channel delivery log makes replays after reconnect or cursor rewind
// idempotent. It returns false to keep the event pending.
func (s *Server) deliverNotification(ctx context.Context, stored store.Event) bool {
	var data map[string]any
	if stored.DataJSON != "" {
		if err := json.Unmarshal([]byte(stored.DataJSON), &data); err != nil {
			data = nil
		}
	}
	if stored.Type == "integration_error" && data["integration"] == notifierIntegrationName {
		// Never notify about notification failures: a broken channel would
		// otherwise feed itself.
		return true
	}
	stage := stored.Stage
	if stage == "" {
		stage, _ = data["stage"].(string)
	}
	notification := notifier.Notification{
		EventID: stored.ID, RunID: stored.RunID, Type: stored.Type, Stage: stage,
		Function: s.notifications.StageFunction(stage), Severity: stored.Severity,
		Timestamp: stored.Timestamp, TaskURL: s.notifications.TaskURL(stored.RunID), Data: data,
	}
	if !s.notifications.Wants(notification) {
		return true
	}
	if run, err := s.store.GetPipelineRunByRunID(stored.RunID); err == nil && run != nil {
		notification.TaskID = run.ID
	}
	err := s.notifications.Dispatch(ctx, notification, s.store, func(channel, reason string) error {
		return s.recordIntegrationErrorDetails(stored, notifierIntegrationName, reason, map[string]any{"channel": channel})
	})
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "notification delivery for event %d stays pending: %v\n", stored.ID, err)
		}
		return false
	}
	return true
}
