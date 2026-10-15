package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// Inbound synchronization (S15): a status change made in the tracker becomes a
// durable request, never a graph mutation of its own. The only thing a request
// can do automatically is ask the controller to continue a stage that is ready
// (the same service path as the "run-agent" action). Everything that needs a
// person (a confirmation, a decision on a failed gate, a return to an earlier
// stage) stays a visible pending request or conflict with the reason and the
// actions the operator may take through the normal stage routes. A tracker
// status therefore never confirms a stage for a human, never skips a gate and
// never bypasses a visit limit, because none of those code paths is called.

const (
	trackerInboundBatch     = 100
	trackerInboundErrorSize = 300
)

// trackerInboundPolicy is the configured status map and poll period.
type trackerInboundPolicy struct {
	rules    []tracker.InboundRule
	interval time.Duration
}

// WithTrackerInbound enables inbound synchronization: rules maps external
// statuses to stage requests and interval is the poll period. It needs
// WithTracker; without it nothing is polled.
func WithTrackerInbound(rules []tracker.InboundRule, interval time.Duration) ServerOption {
	return func(server *Server) {
		if interval <= 0 {
			interval = time.Minute
		}
		server.trackerInbound = &trackerInboundPolicy{rules: append([]tracker.InboundRule(nil), rules...), interval: interval}
	}
}

// trackerSyncResult is what one pass did.
type trackerSyncResult struct {
	Tracker    string `json:"tracker"`
	Trigger    string `json:"trigger"`
	Changes    int    `json:"changes"`
	Applied    int    `json:"applied"`
	Pending    int    `json:"pending"`
	Conflicts  int    `json:"conflicts"`
	Ignored    int    `json:"ignored"`
	Duplicates int    `json:"duplicates"`
	Cursor     string `json:"cursor"`
}

var errTrackerInboundOff = errors.New("tracker inbound synchronization is not configured")

// monitorTrackerInbound polls the tracker until the server stops. The first
// pass runs at once: on first enable it only records the tracker tail.
func (s *Server) monitorTrackerInbound(ctx context.Context) {
	defer s.eventWorkers.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := s.syncTrackerInbound(ctx, "poll"); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "tracker inbound poll failed: %v\n", err)
		}
		timer.Reset(s.trackerInbound.interval)
	}
}

func trackerErrorText(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	if len(text) > trackerInboundErrorSize {
		text = text[:trackerInboundErrorSize] + "..."
	}
	return text
}

// syncTrackerInbound reads tracker changes after the stored cursor, decides
// each one exactly once and advances the cursor after the outcomes are
// persisted. A tracker or storage failure leaves the cursor where it was, so
// the next pass repeats the read: nothing is lost and nothing is decided twice
// (the change ID is the idempotency key).
func (s *Server) syncTrackerInbound(ctx context.Context, trigger string) (trackerSyncResult, error) {
	result := trackerSyncResult{Trigger: trigger}
	if s.trackerSync == nil || s.trackerInbound == nil {
		return result, errTrackerInboundOff
	}
	s.trackerSyncMu.Lock()
	defer s.trackerSyncMu.Unlock()
	t := s.trackerSync.tracker
	name := t.Name()
	result.Tracker = name
	fail := func(err error) (trackerSyncResult, error) {
		if recordErr := s.store.RecordTrackerSyncFailure(name, trackerErrorText(err), s.clock()); recordErr != nil {
			fmt.Fprintf(os.Stderr, "tracker sync failure could not be recorded: %v\n", recordErr)
		}
		return result, err
	}
	state, err := s.store.GetTrackerSyncState(name)
	if err != nil {
		return fail(err)
	}
	cursor := ""
	if state != nil {
		cursor = state.Cursor
	}
	links, err := s.store.ListTrackerLinks()
	if err != nil {
		return fail(err)
	}
	byKey := map[string]store.TrackerLink{}
	var keys []string
	for _, link := range links {
		if link.Tracker == name {
			byKey[link.IssueKey] = link
			keys = append(keys, link.IssueKey)
		}
	}
	if cursor == "" || len(keys) == 0 {
		// Nothing to compare with yet: follow the tail instead of replaying
		// history that happened before any task was linked.
		page, err := t.Changes(ctx, tracker.ChangeQuery{})
		if err != nil {
			return fail(err)
		}
		result.Cursor = page.Cursor
		if err := s.store.SaveTrackerSyncCursor(name, page.Cursor, 0, s.clock()); err != nil {
			return fail(err)
		}
		s.refreshPendingInbound(name)
		return result, nil
	}
	for {
		page, err := t.Changes(ctx, tracker.ChangeQuery{Cursor: cursor, Keys: keys, Limit: trackerInboundBatch})
		if err != nil {
			s.refreshPendingInbound(name)
			return fail(err)
		}
		for _, change := range page.Changes {
			link, ok := byKey[change.IssueKey]
			if !ok {
				continue
			}
			result.Changes++
			if err := s.processInboundChange(ctx, change, link, &result); err != nil {
				return fail(err)
			}
		}
		if page.Cursor != "" && page.Cursor != cursor {
			if err := s.store.SaveTrackerSyncCursor(name, page.Cursor, result.Changes, s.clock()); err != nil {
				return fail(err)
			}
			cursor = page.Cursor
		}
		if len(page.Changes) < trackerInboundBatch {
			break
		}
	}
	result.Cursor = cursor
	if err := s.store.SaveTrackerSyncCursor(name, cursor, result.Changes, s.clock()); err != nil {
		return fail(err)
	}
	s.refreshPendingInbound(name)
	return result, nil
}

func (s *Server) processInboundChange(ctx context.Context, change tracker.Change, link store.TrackerLink, result *trackerSyncResult) error {
	id := change.ID
	if id == "" {
		id = fmt.Sprintf("%s:%s>%s@%d", change.IssueKey, change.From, change.To, change.At.UnixNano())
	}
	now := s.clock()
	record, created, err := s.store.BeginTrackerInbound(store.TrackerInbound{
		Tracker: link.Tracker, ChangeID: id, TaskID: link.TaskID, IssueKey: link.IssueKey,
		FromStatus: change.From, ToStatus: change.To, Origin: change.Origin, Author: change.Author, ChangedAt: change.At,
	}, now)
	if err != nil {
		return err
	}
	if !created && record.State != store.TrackerInboundReceived {
		result.Duplicates++
		return nil
	}
	decided := s.decideInbound(ctx, record, true)
	stored, err := s.store.ResolveTrackerInbound(decided, s.clock())
	if err != nil {
		return err
	}
	switch stored.State {
	case store.TrackerInboundApplied:
		result.Applied++
	case store.TrackerInboundPending:
		result.Pending++
	case store.TrackerInboundConflict:
		result.Conflicts++
	default:
		result.Ignored++
	}
	return nil
}

// decideInbound turns one change into an outcome. It never writes to the
// store. mayApply is false when an old pending request is re-read: only a
// fresh change may start a stage.
func (s *Server) decideInbound(ctx context.Context, rec store.TrackerInbound, mayApply bool) store.TrackerInbound {
	prior := rec.Reason
	rec.Reason, rec.SuggestedAction, rec.ResolvedBy = "", "", ""
	if tracker.IsOwnEcho(tracker.Change{Origin: rec.Origin}) {
		rec.State = store.TrackerInboundIgnored
		rec.Reason = "the change was made by ai-team itself (echo of an outbound transition)"
		return rec
	}
	run, err := s.store.GetPipelineRunByID(rec.TaskID)
	if err != nil || run == nil || run.RunID == "" {
		rec.State, rec.Reason = store.TrackerInboundIgnored, "the linked task was not found"
		return rec
	}
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		return pendingInbound(rec, "the task lifecycle state is unavailable", nil)
	}
	state, err := stateStore.Load(run.RunID)
	if err != nil {
		return pendingInbound(rec, "the task lifecycle state is unavailable", nil)
	}
	rec.Stage = state.NextStage
	if state.Phase == lifecycle.PhaseTerminal {
		rec.State, rec.Reason = store.TrackerInboundIgnored, "the task is finished; the graph is not moved by a tracker status"
		return rec
	}
	rule, resolution, matched := tracker.ResolveInbound(s.trackerInbound.rules, rec.ToStatus, state.NextStage)
	switch resolution {
	case tracker.ResolutionUnmapped:
		rec.State, rec.Reason = store.TrackerInboundIgnored, fmt.Sprintf("status %q is not mapped in tracker.inbound", rec.ToStatus)
		return rec
	case tracker.ResolutionAmbiguous:
		options := make([]string, 0, len(matched))
		for _, candidate := range matched {
			options = append(options, describeInboundRule(candidate))
		}
		rec.State = store.TrackerInboundConflict
		rec.Reason = fmt.Sprintf("status %q is ambiguous at stage %q: %s; the graph was not moved. Fix tracker.inbound.rules or move the issue to an unambiguous status",
			rec.ToStatus, state.NextStage, strings.Join(options, " | "))
		rec.SuggestedAction = "dismiss"
		return rec
	}
	rec.Action, rec.ToStage = rule.Action, rule.ToStage
	allowed := s.taskAllowedActions(run.RunID, state.NextStage, state)
	switch rule.Action {
	case tracker.ActionAdvance:
		return s.decideInboundAdvance(ctx, rec, run.RunID, state, allowed, mayApply, prior)
	case tracker.ActionReturn:
		if !s.taskReturnAllowed(run.RunID, state.NextStage, rule.ToStage) {
			rec.State = store.TrackerInboundConflict
			rec.Reason = fmt.Sprintf("there is no configured return route from %q to %q for this task", state.NextStage, rule.ToStage)
			rec.SuggestedAction = "dismiss"
			return rec
		}
		if state.Phase != lifecycle.PhaseWaiting || !actionAllowed(allowed, "return") {
			return pendingInbound(rec, fmt.Sprintf("a return to %q is possible only while the stage waits for a decision (phase is %s)", rule.ToStage, state.Phase), allowed)
		}
		// A return is a decision of the stage approver and is recorded under
		// that person's identity; a tracker status must not make it for them.
		return pendingInbound(rec, fmt.Sprintf("the tracker asks to return the task to %q; a return is a human decision of the stage approver and is not made from a tracker status", rule.ToStage), allowed)
	}
	rec.State, rec.Reason = store.TrackerInboundConflict, fmt.Sprintf("unknown inbound action %q", rule.Action)
	return rec
}

func (s *Server) decideInboundAdvance(ctx context.Context, rec store.TrackerInbound, runID string, state lifecycle.State, allowed []string, mayApply bool, prior string) store.TrackerInbound {
	switch state.Phase {
	case lifecycle.PhaseResumable:
		if !actionAllowed(allowed, "run-agent") {
			return pendingInbound(rec, fmt.Sprintf("stage %q has no agent to continue; a person must submit its result", state.NextStage), allowed)
		}
		controller, ok := s.controller.(StageAgentController)
		if !ok {
			return pendingInbound(rec, "the task controller does not support continuing a stage", allowed)
		}
		if !mayApply {
			// Keep the earlier reason (for example a controller refusal).
			if prior == "" {
				prior = fmt.Sprintf("stage %q is ready to continue; use the run-agent action", state.NextStage)
			}
			return pendingInbound(rec, prior, allowed)
		}
		actor := trackerInboundActor(rec)
		if err := controller.RunStageAgent(ctx, runID, state.NextStage, actor, false); err != nil {
			return pendingInbound(rec, "the controller refused to continue the stage: "+trackerErrorText(err), allowed)
		}
		rec.State, rec.ResolvedBy = store.TrackerInboundApplied, actor
		return rec
	case lifecycle.PhaseWaiting:
		return pendingInbound(rec, s.waitingReason(runID, state), allowed)
	default:
		return pendingInbound(rec, fmt.Sprintf("stage %q is %s; the request waits until it can move", state.NextStage, state.Phase), allowed)
	}
}

// waitingReason says what a waiting stage is waiting for. A tracker status
// resolves none of these.
func (s *Server) waitingReason(runID string, state lifecycle.State) string {
	const tail = "; a tracker status does not replace it"
	if s.controller == nil || state.PendingApprovalID == "" {
		return "the stage waits for a human decision" + tail
	}
	approvals, err := s.controller.Approvals(runID)
	if err != nil {
		return "the stage waits for a human decision" + tail
	}
	for _, pending := range approvals {
		if pending.ID != state.PendingApprovalID || pending.Status != approval.StatusPending {
			continue
		}
		switch {
		case pending.Kind == approval.KindInput && pending.Trigger == humanInputTrigger:
			return "the stage waits for a person to take it and submit the result" + tail
		case pending.Kind == approval.KindGrillQuestions:
			return "the stage waits for answers to the grill questions" + tail
		case pending.Trigger == "delivery_plan":
			return "the delivery plan waits for approval of its exact hash" + tail
		case pending.Kind == approval.KindCheckDecision && pending.Trigger == approval.StageCheckTrigger:
			return "the stage checks did not pass and the stage waits for a decision" + tail
		}
		return "the stage waits for a human confirmation" + tail
	}
	return "the stage waits for a human decision" + tail
}

func pendingInbound(rec store.TrackerInbound, reason string, allowed []string) store.TrackerInbound {
	rec.State, rec.Reason = store.TrackerInboundPending, reason
	rec.SuggestedAction = strings.Join(allowed, ",")
	return rec
}

func describeInboundRule(rule tracker.InboundRule) string {
	text := rule.Action
	if rule.ToStage != "" {
		text += " to " + rule.ToStage
	}
	if rule.Stage != "" {
		text += " (stage " + rule.Stage + ")"
	} else {
		text += " (any stage)"
	}
	return text
}

func trackerInboundActor(rec store.TrackerInbound) string {
	actor := "tracker:" + rec.Tracker
	if rec.Author != "" {
		actor += ":" + rec.Author
	}
	return actor
}

// refreshPendingInbound re-reads open requests: one whose task has moved to
// another stage is superseded; the others get an up-to-date reason and list of
// allowed actions. It never applies anything.
func (s *Server) refreshPendingInbound(name string) {
	open, err := s.store.ListTrackerInbound(0, []string{store.TrackerInboundPending, store.TrackerInboundConflict}, 500)
	if err != nil {
		return
	}
	for _, rec := range open {
		if rec.Tracker != name {
			continue
		}
		run, err := s.store.GetPipelineRunByID(rec.TaskID)
		if err != nil || run == nil {
			continue
		}
		stateStore, err := lifecycle.NewStore(s.targetDir)
		if err != nil {
			continue
		}
		state, err := stateStore.Load(run.RunID)
		if err != nil {
			continue
		}
		if state.Phase == lifecycle.PhaseTerminal || (rec.Stage != "" && state.NextStage != rec.Stage) {
			reason := fmt.Sprintf("the task moved on to stage %q", state.NextStage)
			if state.Phase == lifecycle.PhaseTerminal {
				reason = "the task is finished"
			}
			_ = s.store.SupersedeTrackerInbound(rec.Tracker, rec.ChangeID, reason, s.clock())
			continue
		}
		refreshed := s.decideInbound(context.Background(), rec, false)
		if refreshed.State == store.TrackerInboundPending || refreshed.State == store.TrackerInboundConflict {
			_, _ = s.store.ResolveTrackerInbound(refreshed, s.clock())
		}
	}
}
