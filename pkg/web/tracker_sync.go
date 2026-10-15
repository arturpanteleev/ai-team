package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// trackerSync is the outbound synchronization of tasks with the configured
// task tracker. The link between a task and a card lives in SQLite
// (tracker_links); every delivery outcome is a durable row in
// tracker_deliveries, so a tracker failure never rolls a run back but also
// never disappears.
type trackerSync struct {
	tracker tracker.Tracker
	maps    tracker.Maps
	taskURL func(taskID int64) string
}

// WithTracker enables outbound synchronization with t. onEnter and onPass map
// a stage ID to the tracker status requested when the stage is entered and
// when it passes. webBaseURL is the public address of this dashboard, used for
// the task link in comments. Tracker work runs in a separate worker, so
// tracker failures cannot change a task's pipeline result.
func WithTracker(t tracker.Tracker, maps tracker.Maps, webBaseURL string) ServerOption {
	return func(server *Server) {
		if t == nil {
			return
		}
		base := strings.TrimRight(strings.TrimSpace(webBaseURL), "/")
		server.trackerSync = &trackerSync{
			tracker: t, maps: maps,
			taskURL: func(taskID int64) string { return fmt.Sprintf("%s/pipelines/%d", base, taskID) },
		}
	}
}

// importLegacyTrackerLinks moves unambiguous issue keys from the old free-form
// external_links into explicit links once per tracker; ambiguous ones become
// visible conflicts.
func (s *Server) importLegacyTrackerLinks() {
	parser, ok := s.trackerSync.tracker.(tracker.LinkParser)
	if !ok {
		return
	}
	extract := func(snapshot string) []string {
		var task startRunCommand
		if json.Unmarshal([]byte(snapshot), &task) != nil {
			return nil
		}
		var keys []string
		for _, link := range task.ExternalLinks {
			if key, found := parser.KeyFromLink(link); found {
				keys = append(keys, key)
			}
		}
		return keys
	}
	result, err := s.store.ImportTrackerLinks(s.trackerSync.tracker.Name(), extract, s.clock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracker link import failed: %v\n", err)
		return
	}
	if !result.Skipped && (result.Linked > 0 || result.Conflicts > 0) {
		fmt.Fprintf(os.Stderr, "tracker link import: %d linked, %d conflicts (see /api/tracker/conflicts)\n", result.Linked, result.Conflicts)
	}
}

// deliverTrackerEvent handles one stored event for the integration consumer.
// It returns false only to keep the event pending; a tracker failure is
// persisted as a failed delivery plus an integration_error event and the
// cursor moves on.
func (s *Server) deliverTrackerEvent(ctx context.Context, stored store.Event) bool {
	if !tracker.Relevant(stored.Type) {
		return true
	}
	run, err := s.store.GetPipelineRunByRunID(stored.RunID)
	if err != nil || run == nil {
		return s.recordIntegrationError(stored, trackerIntegrationName, "tracker task lookup failed")
	}
	link, err := s.store.GetTrackerLink(run.ID)
	if err != nil {
		return s.recordIntegrationError(stored, trackerIntegrationName, "tracker link lookup failed")
	}
	if link == nil || link.Tracker != s.trackerSync.tracker.Name() {
		return true
	}
	ops, err := s.planTrackerEvent(stored, run.ID)
	if err != nil {
		return s.recordIntegrationError(stored, trackerIntegrationName, err.Error())
	}
	if len(ops) == 0 {
		return true
	}
	_, ok := s.runTrackerDelivery(ctx, stored, *link, ops)
	return ok
}

func (s *Server) planTrackerEvent(stored store.Event, taskID int64) ([]tracker.Operation, error) {
	data := map[string]any{}
	if stored.DataJSON != "" {
		if err := json.Unmarshal([]byte(stored.DataJSON), &data); err != nil {
			return nil, errors.New("task event payload is invalid")
		}
		if data == nil {
			data = map[string]any{}
		}
	}
	event := tracker.TaskEvent{
		Cursor: stored.ID, RunID: stored.RunID, TaskID: taskID, Type: stored.Type,
		Stage: stored.Stage, AttemptID: stored.AttemptID, Timestamp: stored.Timestamp, Data: data,
	}
	return tracker.Plan(event, s.trackerSync.maps, s.trackerSync.taskURL(taskID)), nil
}

// runTrackerDelivery executes the operations, persists the outcome and, on
// failure, a visible warning event. The bool is false when the outcome could
// not be persisted at all, which keeps the source event pending.
func (s *Server) runTrackerDelivery(ctx context.Context, stored store.Event, link store.TrackerLink, ops []tracker.Operation) (store.TrackerDelivery, bool) {
	execErr := tracker.Execute(ctx, s.trackerSync.tracker, link.IssueKey, ops)
	delivery := store.TrackerDelivery{
		EventID: stored.ID, TaskID: link.TaskID, Tracker: link.Tracker, IssueKey: link.IssueKey,
		EventType: stored.Type, Stage: stored.Stage, Status: store.TrackerDeliveryDelivered,
	}
	if execErr != nil {
		delivery.Status = store.TrackerDeliveryFailed
		delivery.LastError = execErr.Error()
	}
	recorded, recordErr := s.store.RecordTrackerDelivery(delivery, s.clock())
	if recordErr != nil {
		fmt.Fprintf(os.Stderr, "tracker delivery for event %d could not be recorded: %v\n", stored.ID, recordErr)
		if execErr == nil {
			// The tracker accepted everything; replaying is idempotent, so
			// keeping the event pending is safe and loses nothing.
			return delivery, false
		}
		return delivery, s.recordIntegrationError(stored, trackerIntegrationName, delivery.LastError)
	}
	if execErr != nil {
		details := map[string]any{"tracker": link.Tracker, "issue_key": link.IssueKey, "delivery_event_id": stored.ID}
		if err := s.recordIntegrationErrorDetails(stored, trackerIntegrationName, delivery.LastError, details); err != nil {
			return recorded, true // the failed delivery row is already durable and visible
		}
	}
	return recorded, true
}

var (
	errTrackerDeliveryNotFailed = errors.New("tracker delivery is not failed")
	errTrackerLinkMissing       = errors.New("task has no tracker link")
)

// retryTrackerDelivery re-runs a failed delivery against the task's current
// link. The comment idempotency key comes from the event ID, so a retry never
// duplicates what was already delivered.
func (s *Server) retryTrackerDelivery(ctx context.Context, eventID int64) (store.TrackerDelivery, error) {
	delivery, err := s.store.GetTrackerDelivery(eventID)
	if err != nil {
		return store.TrackerDelivery{}, err
	}
	if delivery.Status != store.TrackerDeliveryFailed {
		return delivery, errTrackerDeliveryNotFailed
	}
	link, err := s.store.GetTrackerLink(delivery.TaskID)
	if err != nil {
		return delivery, err
	}
	if link == nil {
		return delivery, errTrackerLinkMissing
	}
	if link.Tracker != delivery.Tracker || link.IssueKey != delivery.IssueKey {
		// The delivery belonged to a card that is no longer linked; sending it
		// to the new card needs an explicit decision, not a silent retry.
		return s.markTrackerDelivery(delivery, store.TrackerDeliveryAbandoned, "link changed after the failure")
	}
	stored, err := s.store.GetEventByID(eventID)
	if err != nil {
		return delivery, err
	}
	if stored == nil {
		return delivery, store.ErrDeliveryNotFound
	}
	ops, err := s.planTrackerEvent(*stored, delivery.TaskID)
	if err != nil {
		return delivery, err
	}
	ops, stale, err := s.dropSupersededTransitions(delivery, ops)
	if err != nil {
		return delivery, err
	}
	if len(ops) == 0 {
		return s.markTrackerDelivery(delivery, store.TrackerDeliverySuperseded, stale)
	}
	updated, ok := s.runTrackerDelivery(ctx, *stored, *link, ops)
	if !ok {
		return delivery, errors.New("tracker delivery outcome could not be recorded")
	}
	return updated, nil
}

// dropSupersededTransitions removes transitions from a retried delivery when
// a newer stage event of the same task is already delivered to the same card:
// the old transition would move the card back. Comments are idempotent and
// stay. The returned text explains the skip.
func (s *Server) dropSupersededTransitions(delivery store.TrackerDelivery, ops []tracker.Operation) ([]tracker.Operation, string, error) {
	hasTransition := false
	for _, op := range ops {
		hasTransition = hasTransition || op.Kind == tracker.OpTransition
	}
	if !hasTransition {
		return ops, "", nil
	}
	newer, err := s.store.HasNewerDeliveredTrackerEvent(delivery.TaskID, delivery.Tracker, delivery.IssueKey, delivery.EventID)
	if err != nil || !newer {
		return ops, "", err
	}
	kept := ops[:0:0]
	for _, op := range ops {
		if op.Kind != tracker.OpTransition {
			kept = append(kept, op)
		}
	}
	return kept, "superseded by a newer delivered event; the stale transition was not sent", nil
}

// markTrackerDelivery closes a failed delivery without calling the tracker.
func (s *Server) markTrackerDelivery(delivery store.TrackerDelivery, status, reason string) (store.TrackerDelivery, error) {
	delivery.Status, delivery.LastError = status, reason
	return s.store.SetTrackerDeliveryStatus(delivery.EventID, status, reason, s.clock())
}

// trackerSyncStatus summarizes the synchronization state of one task.
func trackerSyncStatus(link *store.TrackerLink, conflicts []store.TrackerLinkConflict, deliveries []store.TrackerDelivery) string {
	switch {
	case link == nil && len(conflicts) > 0:
		return "conflict"
	case link == nil:
		return "not_linked"
	}
	for _, delivery := range deliveries {
		if delivery.Status == store.TrackerDeliveryFailed {
			return "failed"
		}
	}
	return "ok"
}
