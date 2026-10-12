package notifier

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultDeliveryTimeout bounds one external notification delivery. External
// channels run outside the task pipeline, but a hung endpoint must still not
// stall the notification worker indefinitely.
const DefaultDeliveryTimeout = 10 * time.Second

// KnownEventTypes lists the task event types a notification subscription may
// name. A subscription with any other type is rejected by configuration
// validation instead of silently never matching.
var KnownEventTypes = []string{
	"approval_decided", "approval_requested",
	"attempt_finished", "attempt_started", "attempts_invalidated",
	"check_error", "check_overridden",
	"delivery_deferred", "delivery_plan_approved",
	"delivery_retry_failed", "delivery_retry_requested", "delivery_retry_succeeded",
	"description_missing", "executor_changed", "grill_completed",
	"human_result_edited_agent", "integration_error",
	"queue_updated",
	"run_canceled", "run_cancel_requested", "run_finished", "run_paused",
	"run_queued", "run_resumed", "run_started",
	"stage_returned", "stage_skipped", "stage_stalled", "stage_taken",
	"transition_selected",
}

// IsKnownEventType reports whether eventType may be used in a subscription.
func IsKnownEventType(eventType string) bool {
	index := sort.SearchStrings(sortedKnownEventTypes, eventType)
	return index < len(sortedKnownEventTypes) && sortedKnownEventTypes[index] == eventType
}

var sortedKnownEventTypes = func() []string {
	sorted := append([]string(nil), KnownEventTypes...)
	sort.Strings(sorted)
	return sorted
}()

// Notification is one durable task event prepared for an external channel.
// EventID is the global durable event cursor and is the deduplication key.
type Notification struct {
	EventID   int64
	RunID     string
	TaskID    int64
	Type      string
	Stage     string
	Function  string
	Severity  string
	Timestamp time.Time
	TaskURL   string
	Data      map[string]any
}

// Summary renders a short single-line human summary without control
// characters, suitable for chat text and e-mail subjects.
func (n Notification) Summary() string {
	parts := []string{"[ai-team] " + n.Type}
	if n.TaskID > 0 {
		parts = append(parts, "task #"+strconv.FormatInt(n.TaskID, 10))
	} else if n.RunID != "" {
		parts = append(parts, "run "+n.RunID)
	}
	if n.Stage != "" {
		stage := "stage " + n.Stage
		if n.Function != "" && n.Function != n.Stage {
			stage += " (" + n.Function + ")"
		}
		parts = append(parts, stage)
	}
	if n.Severity != "" {
		parts = append(parts, n.Severity)
	}
	return singleLine(strings.Join(parts, " · "))
}

// Text renders the summary followed by the task link when one is known.
func (n Notification) Text() string {
	if n.TaskURL == "" {
		return n.Summary()
	}
	return n.Summary() + "\n" + n.TaskURL
}

func singleLine(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
}

// Channel delivers notifications to one external destination. Errors returned
// by implementations MUST NOT contain destination URLs or credentials: they are
// persisted as task warning events visible in the web UI.
type Channel interface {
	Name() string
	Send(context.Context, Notification) error
}

// Subscription selects events by type and, optionally, by the template
// function of the event's stage. An empty Functions list matches any stage and
// events without a stage.
type Subscription struct {
	Events    []string
	Functions []string
}

// Matches reports whether n is selected by the subscription.
func (s Subscription) Matches(n Notification) bool {
	if !containsString(s.Events, n.Type) {
		return false
	}
	if len(s.Functions) == 0 {
		return true
	}
	return n.Function != "" && containsString(s.Functions, n.Function)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// Route binds a channel to its subscription.
type Route struct {
	Channel      Channel
	Subscription Subscription
}

// Delivery outcome markers persisted in the delivery log.
const (
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// DeliveryLog durably records which channel already handled which event so a
// replay after reconnect or restart does not notify twice.
type DeliveryLog interface {
	NotificationRecorded(channel string, eventID int64) (bool, error)
	RecordNotification(channel string, eventID int64, outcome string) error
}

// FailureRecorder durably records a sanitized delivery failure. Returning an
// error keeps the source event pending so the failure is not lost.
type FailureRecorder func(channel, reason string) error

// Dispatcher routes notifications to subscribed channels exactly once per
// channel and event, as far as the delivery log allows.
type Dispatcher struct {
	routes         []Route
	stageFunctions map[string]string
	webBaseURL     string
}

// NewDispatcher creates a dispatcher. stageFunctions maps template stage IDs
// to their function, used for function subscriptions. webBaseURL, when set,
// produces task links in notifications.
func NewDispatcher(routes []Route, stageFunctions map[string]string, webBaseURL string) *Dispatcher {
	functions := make(map[string]string, len(stageFunctions))
	for stage, function := range stageFunctions {
		functions[stage] = function
	}
	return &Dispatcher{routes: append([]Route(nil), routes...), stageFunctions: functions, webBaseURL: strings.TrimRight(webBaseURL, "/")}
}

// StageFunction returns the template function of a stage ID.
func (d *Dispatcher) StageFunction(stage string) string {
	if d == nil {
		return ""
	}
	return d.stageFunctions[stage]
}

// Wants reports whether any channel subscribes to n, so callers can skip
// enrichment work for events nobody receives.
func (d *Dispatcher) Wants(n Notification) bool {
	if d == nil {
		return false
	}
	for _, route := range d.routes {
		if route.Channel != nil && route.Subscription.Matches(n) {
			return true
		}
	}
	return false
}

// TaskURL returns the web link of a task run, or "" without a base URL.
func (d *Dispatcher) TaskURL(runID string) string {
	if d == nil || d.webBaseURL == "" || runID == "" {
		return ""
	}
	return d.webBaseURL + "/tasks/" + url.PathEscape(runID)
}

// Dispatch sends n to every subscribed channel that has not already handled
// it. A channel failure is passed to recordFailure and logged as handled, so
// one broken destination neither blocks other channels nor retries forever.
// A returned error means the delivery log or failure record could not be
// persisted; the caller must keep the event pending and replay it later.
func (d *Dispatcher) Dispatch(ctx context.Context, n Notification, log DeliveryLog, recordFailure FailureRecorder) error {
	if d == nil {
		return nil
	}
	var pending []error
	for _, route := range d.routes {
		if route.Channel == nil || !route.Subscription.Matches(n) {
			continue
		}
		name := route.Channel.Name()
		done, err := log.NotificationRecorded(name, n.EventID)
		if err != nil {
			pending = append(pending, fmt.Errorf("notification log for %s: %w", name, err))
			continue
		}
		if done {
			continue
		}
		outcome := DeliveryDelivered
		if sendErr := route.Channel.Send(ctx, n); sendErr != nil {
			if ctx.Err() != nil {
				// Shutdown interrupted delivery; keep the event pending.
				return ctx.Err()
			}
			if err := recordFailure(name, sendErr.Error()); err != nil {
				pending = append(pending, fmt.Errorf("notification failure for %s: %w", name, err))
				continue
			}
			outcome = DeliveryFailed
		}
		if err := log.RecordNotification(name, n.EventID, outcome); err != nil {
			pending = append(pending, fmt.Errorf("notification log for %s: %w", name, err))
		}
	}
	return errors.Join(pending...)
}
