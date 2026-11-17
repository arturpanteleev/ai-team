package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// registerGraphAPI mounts the graph and workflow catalog routes: the read-only
// catalogue plus the one structured process-patch write of the workflow card.
//
// The routes live in this function rather than inline in NewServer so the
// handlers and their security stay together in this package. A caller mounts
// them on the real router once; the read group adds the same JSON-error and
// read-security middleware the other read routes use, so mounting it on a bare
// router cannot accidentally expose the catalog. The patch is a real command:
// it gets JSON errors, session/CSRF write security and idempotency like the
// other writes, and it accepts only title/description (WF-06).
//
// Routes:
//
//	GET  /api/graphs/{graphID}                  workflow class graph
//	GET  /api/runs/{runID}/graph                one task's graph with computed stage statuses
//	GET  /api/workflows                         workflow catalogue (list)
//	GET  /api/workflows/{workflowID}            workflow card with graphs, gate chains, tasks
//	POST /api/workflows/{workflowID}/process    structured process-only patch (title/description)
func (s *Server) registerGraphAPI(router chi.Router) {
	router.Group(func(r chi.Router) {
		r.Use(s.apiJSONErrors)
		r.Use(s.readSecurity)
		r.Get("/api/graphs/{graphID}", s.handleGetGraph)
		r.Get("/api/runs/{runID}/graph", s.handleGetRunGraph)
		r.Get("/api/workflows", s.handleListWorkflows)
		r.Get("/api/workflows/{workflowID}", s.handleGetWorkflow)
	})
	router.Group(func(r chi.Router) {
		r.Use(s.apiJSONErrors)
		r.Use(s.writeSecurity)
		r.Use(func(next http.Handler) http.Handler {
			return apiIdempotencyMiddleware{store: s.store, next: next}
		})
		r.Post("/api/workflows/{workflowID}/process", s.handlePatchWorkflowProcess)
	})
}
