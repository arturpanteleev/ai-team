package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// trackerInboundView is an inbound request as the operator sees it: the
// stored record plus, for an open one, the actions the controller allows for
// the task right now.
type trackerInboundView struct {
	store.TrackerInbound
	AllowedActions []string `json:"allowed_actions,omitempty"`
}

func (s *Server) inboundViews(items []store.TrackerInbound) []trackerInboundView {
	views := make([]trackerInboundView, 0, len(items))
	for _, item := range items {
		view := trackerInboundView{TrackerInbound: item}
		if item.State == store.TrackerInboundPending || item.State == store.TrackerInboundConflict {
			view.AllowedActions = s.currentAllowedActions(item.TaskID)
		}
		views = append(views, view)
	}
	return views
}

func (s *Server) currentAllowedActions(taskID int64) []string {
	run, err := s.store.GetPipelineRunByID(taskID)
	if err != nil || run == nil {
		return nil
	}
	stateStore, err := lifecycle.NewStore(s.targetDir)
	if err != nil {
		return nil
	}
	state, err := stateStore.Load(run.RunID)
	if err != nil {
		return nil
	}
	return s.taskAllowedActions(run.RunID, state.NextStage, state)
}

// inboundSummary is the sync block of /api/tracker and the MCP status tool:
// configuration, poll health and what needs an operator.
func (s *Server) inboundSummary() (map[string]any, error) {
	if s.trackerInbound == nil {
		return map[string]any{"configured": false}, nil
	}
	name := s.trackerSync.tracker.Name()
	state, err := s.store.GetTrackerSyncState(name)
	if err != nil {
		return nil, err
	}
	open, err := s.store.ListTrackerInbound(0, []string{store.TrackerInboundPending, store.TrackerInboundConflict}, 500)
	if err != nil {
		return nil, err
	}
	pending, conflicts := 0, 0
	for _, item := range open {
		if item.State == store.TrackerInboundConflict {
			conflicts++
		} else {
			pending++
		}
	}
	rules := make([]map[string]string, 0, len(s.trackerInbound.rules))
	for _, rule := range s.trackerInbound.rules {
		entry := map[string]string{"status": rule.Status, "action": rule.Action}
		if rule.Stage != "" {
			entry["stage"] = rule.Stage
		}
		if rule.ToStage != "" {
			entry["to_stage"] = rule.ToStage
		}
		rules = append(rules, entry)
	}
	summary := map[string]any{
		"configured": true, "poll_interval_seconds": int64(s.trackerInbound.interval / time.Second),
		"rules": rules, "pending_requests": pending, "conflicts": conflicts, "status": "never_polled",
	}
	if state != nil {
		summary["sync"] = state
		summary["status"] = "ok"
		if state.LastError != "" {
			summary["status"] = "failed"
		}
		if state.LastSuccessAt != nil {
			summary["delay_seconds"] = int64(s.clock().Sub(*state.LastSuccessAt) / time.Second)
		}
	}
	return summary, nil
}

// handleTrackerSync reports the poll state: interval, cursor, delay, errors.
func (s *Server) handleTrackerSync(w http.ResponseWriter, _ *http.Request) {
	if s.trackerSync == nil {
		writeJSONResponse(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	summary, err := s.inboundSummary()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker sync state is unavailable", "tracker_unavailable")
		return
	}
	summary["tracker"] = s.trackerSync.tracker.Name()
	writeJSONResponse(w, http.StatusOK, summary)
}

// handleTrackerSyncNow runs one inbound pass immediately. It decides changes
// exactly as the periodic poll does; it is not a way to push the graph.
func (s *Server) handleTrackerSyncNow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trackerActor(w, r); !ok || !s.requireTracker(w) {
		return
	}
	if s.trackerInbound == nil {
		writeJSONAPIError(w, http.StatusConflict, "tracker inbound synchronization is not configured (tracker.inbound)", "inbound_not_configured")
		return
	}
	result, err := s.syncTrackerInbound(r.Context(), "manual")
	if err != nil {
		// The failure is stored with the cursor untouched; a retry resumes.
		writeJSONResponse(w, http.StatusBadGateway, map[string]any{
			"error": "the tracker could not be read; nothing was lost, repeat the sync later: " + trackerErrorText(err),
			"code":  "tracker_unavailable", "result": result,
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"result": result})
}

// handleTrackerInbound lists inbound requests, newest first. state narrows to
// one of the stored states; "open" means pending and conflict.
func (s *Server) handleTrackerInbound(w http.ResponseWriter, r *http.Request) {
	var states []string
	switch value := strings.TrimSpace(r.URL.Query().Get("state")); value {
	case "":
	case "open":
		states = []string{store.TrackerInboundPending, store.TrackerInboundConflict}
	case store.TrackerInboundApplied, store.TrackerInboundPending, store.TrackerInboundConflict, store.TrackerInboundIgnored,
		store.TrackerInboundSuperseded, store.TrackerInboundDismissed:
		states = []string{value}
	default:
		writeJSONAPIError(w, http.StatusBadRequest, "state must be open, pending, conflict, applied, ignored, superseded or dismissed", "bad_request")
		return
	}
	items, err := s.store.ListTrackerInbound(0, states, 200)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker inbound requests are unavailable", "tracker_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"requests": s.inboundViews(items)})
}

type trackerDismissCommand struct {
	Reason string `json:"reason"`
}

// handleDismissTrackerInbound closes an open request or conflict with a
// reason. It changes nothing in the task.
func (s *Server) handleDismissTrackerInbound(w http.ResponseWriter, r *http.Request) {
	actorID, ok := s.trackerActor(w, r)
	if !ok || !s.requireTracker(w) {
		return
	}
	run, ok := s.trackerRun(w, r)
	if !ok {
		return
	}
	var command trackerDismissCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if strings.TrimSpace(command.Reason) == "" {
		writeJSONAPIError(w, http.StatusBadRequest, "a reason is required to dismiss a tracker request", "reason_required")
		return
	}
	name := s.trackerSync.tracker.Name()
	changeID := chi.URLParam(r, "changeID")
	existing, err := s.store.GetTrackerInbound(name, changeID)
	if err == nil && existing.TaskID != run.ID {
		err = store.ErrInboundNotFound
	}
	if err == nil {
		existing, err = s.store.DismissTrackerInbound(name, changeID, actorID, strings.TrimSpace(command.Reason), s.clock())
	}
	switch {
	case errors.Is(err, store.ErrInboundNotFound):
		writeJSONAPIError(w, http.StatusNotFound, "tracker request not found", "request_not_found")
	case errors.Is(err, store.ErrInboundNotOpen):
		writeJSONAPIError(w, http.StatusConflict, "the request is not open (already applied, dismissed or superseded)", "request_not_open")
	case err != nil:
		writeJSONAPIError(w, http.StatusInternalServerError, "the request could not be dismissed", "tracker_unavailable")
	default:
		writeJSONResponse(w, http.StatusOK, map[string]any{"request": existing})
	}
}
