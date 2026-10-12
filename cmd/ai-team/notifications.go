package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/web"
	webstore "github.com/arturpanteleev/ai-team/pkg/web/store"
)

// notificationFlushTimeout bounds how long a finishing process waits for the
// last notifications of its run. It covers a few deliveries at
// notifier.DefaultDeliveryTimeout; anything left is picked up by `ai-team web`
// if it runs on the same database.
const notificationFlushTimeout = 30 * time.Second

// schedulerNotifierIntegration is the durable cursor of scheduler-worker. It
// is separate from the web server's cursor: every process keeps its own
// position, and the shared channel/event claim prevents duplicates.
const schedulerNotifierIntegration = "notifier:scheduler-worker"

// runNotifications delivers the notifications of runs executed by this
// `ai-team run` process. The worker reads the shared SQLite event store the
// recorder writes to, so run and web use one delivery path and one
// deduplication key.
type runNotifications struct {
	worker *web.NotificationWorker
	mu     sync.Mutex
	runs   map[string]bool
	cancel context.CancelFunc
	done   chan struct{}
}

// startRunNotifications returns nil when notifications are not configured, in
// which case `ai-team run` behaves exactly as before.
func startRunNotifications(cfg *config.Config, s *webstore.Store, getenv func(string) string, client *http.Client) *runNotifications {
	dispatcher := web.NotificationDispatcherFromConfig(cfg, getenv, client)
	if dispatcher == nil || s == nil {
		return nil
	}
	n := &runNotifications{runs: map[string]bool{}, done: make(chan struct{})}
	worker, err := web.NewNotificationWorker(s, dispatcher, web.WithNotificationFilter(func(event webstore.Event) bool {
		return n.tracked(event.RunID)
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ уведомления отключены: %v\n", err)
		return nil
	}
	n.worker = worker
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	go func() {
		defer close(n.done)
		worker.Run(ctx)
	}()
	return n
}

func (n *runNotifications) track(runID string) {
	if n == nil || runID == "" {
		return
	}
	n.mu.Lock()
	n.runs[runID] = true
	n.mu.Unlock()
}

func (n *runNotifications) tracked(runID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.runs[runID]
}

// recorder wraps the store recorder so the worker learns which runs belong to
// this process. The concrete recorder is embedded, not the pipeline.Recorder
// interface, so its optional event-projection methods stay visible.
func (n *runNotifications) recorder(base *web.StoreRecorder) *trackingRecorder {
	return &trackingRecorder{StoreRecorder: base, notifications: n}
}

// finish stops background delivery and drains what the run produced, within
// timeout. Delivery failures never change the run outcome.
func (n *runNotifications) finish(timeout time.Duration) {
	if n == nil {
		return
	}
	n.cancel()
	<-n.done
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if !n.worker.Flush(ctx) {
		fmt.Fprintf(os.Stderr, "⚠ не все уведомления отправлены за %s; их доставит ai-team web на той же базе\n", timeout)
	}
}

type trackingRecorder struct {
	*web.StoreRecorder
	notifications *runNotifications
}

func (r *trackingRecorder) RunStarted(runID, feature, configSnapshot string, startedAt time.Time) {
	r.notifications.track(runID)
	r.StoreRecorder.RunStarted(runID, feature, configSnapshot, startedAt)
}

func (r *trackingRecorder) RunResumed(runID string, resumedAt time.Time) {
	r.notifications.track(runID)
	r.StoreRecorder.RunResumed(runID, resumedAt)
}

func (r *trackingRecorder) RunAttached(runID string) {
	r.notifications.track(runID)
	r.StoreRecorder.RunAttached(runID)
}

// startSchedulerNotifications delivers notifications for every run the
// scheduler-worker writes into db, with its own durable cursor. A missing or
// invalid config only disables notifications: the scheduler keeps working.
func startSchedulerNotifications(ctx context.Context, target string, reg config.AgentLookup, s *webstore.Store, getenv func(string) string) func() {
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if err := safeio.RejectSymlink(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ уведомления scheduler-worker отключены: %v\n", err)
		return func() {}
	}
	cfg, err := config.Load(cfgPath)
	if err == nil {
		err = cfg.Validate(reg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ уведомления scheduler-worker отключены: %v\n", err)
		return func() {}
	}
	dispatcher := web.NotificationDispatcherFromConfig(cfg, getenv, nil)
	if dispatcher == nil {
		return func() {}
	}
	worker, err := web.NewNotificationWorker(s, dispatcher, web.WithDurableNotificationCursor(schedulerNotifierIntegration))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ уведомления scheduler-worker отключены: %v\n", err)
		return func() {}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(runCtx)
	}()
	return func() {
		cancel()
		<-done
		flushCtx, flushCancel := context.WithTimeout(context.Background(), notificationFlushTimeout)
		defer flushCancel()
		worker.Flush(flushCtx)
	}
}
