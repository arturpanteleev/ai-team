package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/tracker"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

type trackerLinkCommand struct {
	IssueKey string `json:"issue_key"`
}

func (s *Server) requireTracker(w http.ResponseWriter) bool {
	if s.trackerSync == nil {
		writeJSONAPIError(w, http.StatusConflict, "no task tracker is configured", "tracker_not_configured")
		return false
	}
	return true
}

func (s *Server) trackerActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if err := s.authorize(r, cloudidentity.PermissionTrackerLink, ""); err != nil {
		writeJSONAPIError(w, http.StatusForbidden, err.Error(), "forbidden")
		return "", false
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	return actorID, true
}

func (s *Server) trackerRun(w http.ResponseWriter, r *http.Request) (*store.PipelineRun, bool) {
	run, err := s.store.GetPipelineRunByRunID(chi.URLParam(r, "runID"))
	if err != nil || run == nil {
		writeJSONAPIError(w, http.StatusNotFound, "run not found", "run_not_found")
		return nil, false
	}
	return run, true
}

// handleTrackerStatus reports whether a tracker is configured and what it is
// configured to do, plus the numbers an operator should look at.
func (s *Server) handleTrackerStatus(w http.ResponseWriter, _ *http.Request) {
	if s.trackerSync == nil {
		writeJSONResponse(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	failed, err := s.store.ListTrackerDeliveries(0, store.TrackerDeliveryFailed, 500)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker state is unavailable", "tracker_unavailable")
		return
	}
	conflicts, err := s.store.ListTrackerLinkConflicts(0)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker state is unavailable", "tracker_unavailable")
		return
	}
	inbound, err := s.inboundSummary()
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker state is unavailable", "tracker_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"configured": true, "tracker": s.trackerSync.tracker.Name(),
		"on_enter": s.trackerSync.maps.OnEnter, "on_pass": s.trackerSync.maps.OnPass,
		"failed_deliveries": len(failed), "link_conflicts": len(conflicts), "inbound": inbound,
	})
}

func (s *Server) handleTrackerConflicts(w http.ResponseWriter, _ *http.Request) {
	conflicts, err := s.store.ListTrackerLinkConflicts(0)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker conflicts are unavailable", "tracker_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"conflicts": conflicts})
}

func (s *Server) handleTrackerDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != store.TrackerDeliveryFailed && status != store.TrackerDeliveryDelivered &&
		status != store.TrackerDeliveryAbandoned && status != store.TrackerDeliverySuperseded {
		writeJSONAPIError(w, http.StatusBadRequest, "status must be failed, delivered, abandoned or superseded", "bad_request")
		return
	}
	deliveries, err := s.store.ListTrackerDeliveries(0, status, 200)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker deliveries are unavailable", "tracker_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"deliveries": deliveries})
}

// handleRunTracker returns the link, conflicts, recent deliveries and a
// one-word sync status of a task.
func (s *Server) handleRunTracker(w http.ResponseWriter, r *http.Request) {
	run, ok := s.trackerRun(w, r)
	if !ok {
		return
	}
	if s.trackerSync == nil {
		writeJSONResponse(w, http.StatusOK, map[string]any{"configured": false, "sync_status": "not_configured"})
		return
	}
	link, err := s.store.GetTrackerLink(run.ID)
	if err == nil {
		var conflicts []store.TrackerLinkConflict
		if conflicts, err = s.store.ListTrackerLinkConflicts(run.ID); err == nil {
			var deliveries []store.TrackerDelivery
			if deliveries, err = s.store.ListTrackerDeliveries(run.ID, "", 50); err == nil {
				var inbound []store.TrackerInbound
				if inbound, err = s.store.ListTrackerInbound(run.ID, nil, 50); err == nil {
					status := trackerSyncStatus(link, conflicts, deliveries)
					if status == "ok" {
						for _, item := range inbound {
							if item.State == store.TrackerInboundPending || item.State == store.TrackerInboundConflict {
								status = "inbound_pending"
								break
							}
						}
					}
					writeJSONResponse(w, http.StatusOK, map[string]any{
						"configured": true, "tracker": s.trackerSync.tracker.Name(), "link": link,
						"conflicts": conflicts, "deliveries": deliveries,
						"inbound": s.inboundViews(inbound), "sync_status": status,
					})
					return
				}
			}
		}
	}
	writeJSONAPIError(w, http.StatusInternalServerError, "tracker state is unavailable", "tracker_unavailable")
}

// handleSetTrackerLink links the task with one existing card after checking
// the key format, that the card is reachable, and that neither side is linked
// to anything else.
func (s *Server) handleSetTrackerLink(w http.ResponseWriter, r *http.Request) {
	actorID, ok := s.trackerActor(w, r)
	if !ok || !s.requireTracker(w) {
		return
	}
	run, ok := s.trackerRun(w, r)
	if !ok {
		return
	}
	var command trackerLinkCommand
	if err := decodeCommand(w, r, &command); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	key := strings.TrimSpace(command.IssueKey)
	t := s.trackerSync.tracker
	if err := t.ValidateKey(key); err != nil {
		writeJSONAPIError(w, http.StatusBadRequest, err.Error(), "invalid_issue_key")
		return
	}
	issue, err := t.GetIssue(r.Context(), key)
	switch {
	case errors.Is(err, tracker.ErrNotFound):
		writeJSONAPIError(w, http.StatusUnprocessableEntity, "tracker issue "+key+" was not found", "issue_not_found")
		return
	case errors.Is(err, tracker.ErrForbidden):
		writeJSONAPIError(w, http.StatusUnprocessableEntity, "tracker issue "+key+" is not accessible with the configured credentials", "issue_forbidden")
		return
	case err != nil:
		writeJSONAPIError(w, http.StatusBadGateway, "the tracker could not be reached to check the issue", "tracker_unavailable")
		return
	}
	link, err := s.store.SetTrackerLink(run.ID, t.Name(), key, actorID, s.clock())
	switch {
	case errors.Is(err, store.ErrTaskAlreadyLinked):
		writeJSONAPIError(w, http.StatusConflict, "the task is already linked to another issue; remove that link first", "task_already_linked")
		return
	case errors.Is(err, store.ErrIssueAlreadyLinked):
		code := "issue_already_linked"
		message := "the issue is already linked to another task; remove that link first"
		if owner, lookupErr := s.store.TrackerLinkByIssue(t.Name(), key); lookupErr == nil && owner != nil {
			message += " (task " + strconv.FormatInt(owner.TaskID, 10) + ")"
		}
		writeJSONAPIError(w, http.StatusConflict, message, code)
		return
	case err != nil:
		writeJSONAPIError(w, http.StatusInternalServerError, "the link could not be saved", "tracker_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"link": link, "issue": issue})
}

func (s *Server) handleDeleteTrackerLink(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trackerActor(w, r); !ok {
		return
	}
	run, ok := s.trackerRun(w, r)
	if !ok {
		return
	}
	removed, err := s.store.DeleteTrackerLink(run.ID)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "the link could not be removed", "tracker_unavailable")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) writeTrackerRetryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDeliveryNotFound):
		writeJSONAPIError(w, http.StatusNotFound, "tracker delivery not found", "delivery_not_found")
	case errors.Is(err, errTrackerDeliveryNotFailed):
		writeJSONAPIError(w, http.StatusConflict, "only a failed delivery can be retried", "delivery_not_failed")
	case errors.Is(err, errTrackerLinkMissing):
		writeJSONAPIError(w, http.StatusConflict, "the task has no tracker link; link it first", "task_not_linked")
	default:
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker retry failed", "tracker_unavailable")
	}
}

func (s *Server) handleRetryTrackerDelivery(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trackerActor(w, r); !ok || !s.requireTracker(w) {
		return
	}
	run, ok := s.trackerRun(w, r)
	if !ok {
		return
	}
	eventID, err := strconv.ParseInt(chi.URLParam(r, "eventID"), 10, 64)
	if err != nil || eventID <= 0 {
		writeJSONAPIError(w, http.StatusBadRequest, "invalid event id", "bad_request")
		return
	}
	existing, err := s.store.GetTrackerDelivery(eventID)
	if err == nil && existing.TaskID != run.ID {
		err = store.ErrDeliveryNotFound
	}
	if err != nil {
		s.writeTrackerRetryError(w, err)
		return
	}
	delivery, err := s.retryTrackerDelivery(r.Context(), eventID)
	if err != nil {
		s.writeTrackerRetryError(w, err)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"delivery": delivery})
}

// handleRetryTrackerDeliveries retries every failed delivery of the task, in
// event order. Each result is reported; one failure does not stop the rest.
func (s *Server) handleRetryTrackerDeliveries(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trackerActor(w, r); !ok || !s.requireTracker(w) {
		return
	}
	run, ok := s.trackerRun(w, r)
	if !ok {
		return
	}
	failed, err := s.store.ListTrackerDeliveries(run.ID, store.TrackerDeliveryFailed, 500)
	if err != nil {
		writeJSONAPIError(w, http.StatusInternalServerError, "tracker deliveries are unavailable", "tracker_unavailable")
		return
	}
	results := make([]store.TrackerDelivery, 0, len(failed))
	for index := len(failed) - 1; index >= 0; index-- {
		delivery, retryErr := s.retryTrackerDelivery(r.Context(), failed[index].EventID)
		if retryErr != nil && delivery.EventID == 0 {
			delivery = failed[index]
		}
		results = append(results, delivery)
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"deliveries": results})
}
