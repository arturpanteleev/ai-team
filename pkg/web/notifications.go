package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

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

// deliverNotification sends one durable event to subscribed channels for the
// web server's own notifier cursor.
func (s *Server) deliverNotification(ctx context.Context, stored store.Event) bool {
	return s.notificationWorker.Deliver(ctx, stored)
}

// NotificationWorker delivers durable store events to the configured channels.
// It is the single delivery path for every process that writes task events
// into the shared SQLite store: `ai-team web`, a local `ai-team run` and
// `ai-team scheduler-worker`. Each process keeps its own cursor, while the
// channel/event claim in notification_deliveries is shared, so processes on
// one database never notify the same channel about the same event twice.
type NotificationWorker struct {
	store      *store.Store
	dispatcher *notifier.Dispatcher
	include    func(store.Event) bool
	ack        func(int64) error
	poll       time.Duration

	mu     sync.Mutex // serializes drains and guards cursor
	cursor int64
}

// NotificationWorkerOption configures a NotificationWorker.
type NotificationWorkerOption func(*NotificationWorker)

// WithNotificationFilter restricts delivery to events for which include
// returns true; other events are skipped and acknowledged.
func WithNotificationFilter(include func(store.Event) bool) NotificationWorkerOption {
	return func(w *NotificationWorker) { w.include = include }
}

// WithDurableNotificationCursor persists the worker's position under the
// given integration name. On first enable the position starts at the current
// event tail, as for the web server.
func WithDurableNotificationCursor(integration string) NotificationWorkerOption {
	return func(w *NotificationWorker) {
		tail, err := w.store.LatestEventCursor()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s cursor unavailable; starting at the beginning: %v\n", integration, err)
		}
		w.cursor = integrationStartCursor(w.store, integration, tail)
		w.ack = func(cursor int64) error { return w.store.SetIntegrationCursor(integration, cursor) }
	}
}

// NewNotificationWorker creates a worker over s. Without a durable cursor the
// worker starts at the current event tail and keeps its position in memory,
// which suits a single `ai-team run` process. It returns nil when dispatcher
// is nil, i.e. notifications are not configured.
func NewNotificationWorker(s *store.Store, dispatcher *notifier.Dispatcher, options ...NotificationWorkerOption) (*NotificationWorker, error) {
	if s == nil || dispatcher == nil {
		return nil, nil
	}
	tail, err := s.LatestEventCursor()
	if err != nil {
		return nil, fmt.Errorf("notification cursor: %w", err)
	}
	worker := &NotificationWorker{store: s, dispatcher: dispatcher, cursor: tail, poll: 250 * time.Millisecond}
	for _, option := range options {
		option(worker)
	}
	return worker, nil
}

// Run drains new events until ctx is canceled. It wakes on events committed
// through this process's store and polls for events written by others.
func (w *NotificationWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	updates := w.store.SubscribeEvents(ctx, 256)
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-updates:
			if !open {
				return
			}
		case <-ticker.C:
		}
		w.Drain(ctx)
	}
}

// Drain delivers every event after the cursor. It returns true when it caught
// up with the store and false when an event stays pending (a channel/event
// claimed by another process, an unwritable delivery log, or shutdown).
func (w *NotificationWorker) Drain(ctx context.Context) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	const pageSize = 100
	for {
		events, err := w.store.GetEventsAfter(w.cursor, pageSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "notification event replay: %v\n", err)
			return false
		}
		for _, event := range events {
			// Events outside the filter are acknowledged without delivery.
			if (w.include == nil || w.include(event)) && !w.Deliver(ctx, event) {
				return false
			}
			if w.ack != nil {
				if err := w.ack(event.ID); err != nil {
					fmt.Fprintf(os.Stderr, "notification cursor update: %v\n", err)
					return false
				}
			}
			w.cursor = event.ID
		}
		if len(events) < pageSize {
			return true
		}
	}
}

// Flush retries Drain until the worker caught up or ctx ends. A process that
// is about to exit calls it so events of the finished run are not left behind.
func (w *NotificationWorker) Flush(ctx context.Context) bool {
	if w == nil {
		return true
	}
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		if w.Drain(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// Deliver sends one durable event to subscribed channels. The shared delivery
// log makes replays after reconnect or cursor rewind, and the same event seen
// by another process, idempotent. It returns false to keep the event pending.
func (w *NotificationWorker) Deliver(ctx context.Context, stored store.Event) bool {
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
		Function: w.dispatcher.StageFunction(stage), Severity: stored.Severity,
		Timestamp: stored.Timestamp, TaskURL: w.dispatcher.TaskURL(stored.RunID), Data: data,
	}
	if !w.dispatcher.Wants(notification) {
		return true
	}
	if run, err := w.store.GetPipelineRunByRunID(stored.RunID); err == nil && run != nil {
		notification.TaskID = run.ID
	}
	err := w.dispatcher.Dispatch(ctx, notification, w.store, func(channel, reason string) error {
		return recordIntegrationError(w.store, stored, notifierIntegrationName, reason, map[string]any{"channel": channel})
	})
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, notifier.ErrDeliveryInProgress) {
			fmt.Fprintf(os.Stderr, "notification delivery for event %d stays pending: %v\n", stored.ID, err)
		}
		return false
	}
	return true
}
