package web

import (
	"github.com/go-chi/chi/v5"
)

// registerGraphAPI mounts the read-only graph and workflow catalog routes.
//
// The routes live in this function rather than inline in NewServer so the
// handlers and their security stay together in this package. A caller mounts
// them on the real router once; this function adds the same JSON-error and
// read-security middleware the other read routes use, so mounting it on a bare
// router cannot accidentally expose the catalog.
//
// Routes (all GET, no writes):
//
//	/api/graphs/{graphID}        workflow class graph
//	/api/runs/{runID}/graph      one task's graph with computed stage statuses
//	/api/workflows               workflow catalogue (list)
//	/api/workflows/{workflowID}  workflow card with graphs, gate chains, tasks
func (s *Server) registerGraphAPI(router chi.Router) {
	router.Group(func(r chi.Router) {
		r.Use(s.apiJSONErrors)
		r.Use(s.readSecurity)
		r.Get("/api/graphs/{graphID}", s.handleGetGraph)
		r.Get("/api/runs/{runID}/graph", s.handleGetRunGraph)
		r.Get("/api/workflows", s.handleListWorkflows)
		r.Get("/api/workflows/{workflowID}", s.handleGetWorkflow)
	})
}
