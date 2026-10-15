package web

import "net/http"

// The MCP parity matrix (S13) lists every route the router serves and says how
// MCP covers it: the tool that performs the same operation, or the reason the
// route needs no tool because MCP's own transport does its job (or must never
// do it). TestMCPParityMatrix walks the real chi router, so a route added
// without a line here fails the tests, and a line without a route or a tool
// fails too. docs/reference/mcp.md carries the same table; a test keeps the two
// in step.

// mcpExceptionKind classifies a route that has no MCP tool.
type mcpExceptionKind string

const (
	// mcpExceptTransportAuth: the route establishes, ends or manages a browser
	// session or credential. MCP authenticates every call with its Bearer
	// token instead, so the function is performed by the transport.
	mcpExceptTransportAuth mcpExceptionKind = "transport-auth"
	// mcpExceptTransport: the route is a transport of its own (the MCP endpoint,
	// the WebSocket push channel) and has an MCP-native counterpart.
	mcpExceptTransport mcpExceptionKind = "transport"
	// mcpExceptStatic: static frontend assets; not an API.
	mcpExceptStatic mcpExceptionKind = "static"
)

// mcpRouteParity is one row of the matrix.
type mcpRouteParity struct {
	// Method is the HTTP method, or "*" for every method of the pattern.
	Method string
	// Pattern is the chi route pattern exactly as chi.Walk reports it.
	Pattern string
	// Tool names the registry operation that serves this route.
	Tool string
	// Alias: the route is a second spelling of the tool's route (same handler);
	// the tool's own route is another row.
	Alias bool
	// Kind and Reason explain a route without a tool.
	Kind   mcpExceptionKind
	Reason string
	// Conditional: the route exists only in some configurations (OIDC enabled,
	// frontend served), so the matrix may list it although a given server
	// does not register it.
	Conditional bool
}

var mcpParityMatrix = []mcpRouteParity{
	// --- Transport and authentication routes: no tool, by design ---------
	{Method: "*", Pattern: "/mcp", Kind: mcpExceptTransport,
		Reason: "This is the MCP endpoint itself: every tool above is reached through it."},
	{Method: http.MethodGet, Pattern: "/ws", Kind: mcpExceptTransport,
		Reason: "Server-push WebSocket for the browser dashboard. MCP here is stateless request/response without server-initiated messages; " +
			"the same information is polled with list_events and list_task_events (cursor paging)."},
	{Method: http.MethodGet, Pattern: "/*", Kind: mcpExceptStatic, Conditional: true,
		Reason: "Static files of the web frontend (served only when a frontend is built in); not an API."},
	{Method: http.MethodGet, Pattern: "/api/auth/config", Kind: mcpExceptTransportAuth,
		Reason: "Public pre-login discovery of the sign-in modes for the browser. An MCP client is already authenticated by its Bearer token on every call; whoami reports the identity."},
	{Method: http.MethodGet, Pattern: "/api/session", Kind: mcpExceptTransportAuth,
		Reason: "Exchanges a Bearer token for a browser cookie session and its CSRF token. MCP has no cookie session (the cookie is never read on /mcp): " +
			"the Bearer token authenticates each call and, for actions, replaces the CSRF token."},
	{Method: http.MethodDelete, Pattern: "/api/session", Kind: mcpExceptTransportAuth,
		Reason: "Ends a browser cookie session. MCP has none; an MCP credential is ended by revoking its personal token in the web UI (or, for cloud tokens, by its expiry or removing the member)."},
	{Method: http.MethodGet, Pattern: "/api/auth/oidc/login", Kind: mcpExceptTransportAuth, Conditional: true,
		Reason: "Browser redirect that starts single sign-on for a person. MCP carries a token that was issued after sign-in."},
	{Method: http.MethodGet, Pattern: "/api/auth/oidc/callback", Kind: mcpExceptTransportAuth, Conditional: true,
		Reason: "Browser redirect target that completes single sign-on and sets the session cookie. Not reachable by a Bearer client."},
	{Method: http.MethodPost, Pattern: "/api/team/activate", Kind: mcpExceptTransportAuth,
		Reason: "Lets an invited person activate their own membership with a one-time token before they are a team member. MCP never admits non-members " +
			"(mcpAuthenticate refuses an actor without an active membership and never bootstraps a team)."},
	{Method: http.MethodGet, Pattern: "/api/mcp/tokens", Kind: mcpExceptTransportAuth,
		Reason: "Lists the caller's personal MCP tokens. Token management needs a browser session and its CSRF token; an MCP credential may not manage credentials " +
			"(the route answers 403 to any MCP-dispatched request)."},
	{Method: http.MethodPost, Pattern: "/api/mcp/tokens", Kind: mcpExceptTransportAuth,
		Reason: "Mints a personal MCP token. Browser session and CSRF only: a leaked MCP token must not be able to mint more tokens (403 to any MCP-dispatched request)."},
	{Method: http.MethodDelete, Pattern: "/api/mcp/tokens/{tokenID}", Kind: mcpExceptTransportAuth,
		Reason: "Revokes a personal MCP token. Browser session and CSRF only, for the same reason as minting (403 to any MCP-dispatched request)."},

	// --- Identity, server, projects, backlog ------------------------------
	{Method: http.MethodGet, Pattern: "/api/auth/me", Tool: "whoami"},
	{Method: http.MethodGet, Pattern: "/api/version", Tool: "get_version"},
	{Method: http.MethodGet, Pattern: "/api/preflight", Tool: "get_preflight"},
	{Method: http.MethodGet, Pattern: "/api/projects", Tool: "list_projects"},
	{Method: http.MethodPost, Pattern: "/api/projects", Tool: "create_project"},
	{Method: http.MethodGet, Pattern: "/api/projects/{projectID}", Tool: "get_project"},
	{Method: http.MethodPatch, Pattern: "/api/projects/{projectID}", Tool: "update_project"},
	{Method: http.MethodPost, Pattern: "/api/projects/{projectID}/archive", Tool: "archive_project"},
	{Method: http.MethodPost, Pattern: "/api/projects/{projectID}/unarchive", Tool: "unarchive_project"},
	{Method: http.MethodGet, Pattern: "/api/backlog", Tool: "list_backlog"},
	{Method: http.MethodPost, Pattern: "/api/backlog", Tool: "create_backlog_item"},
	{Method: http.MethodPost, Pattern: "/api/backlog/{itemID}/status", Tool: "set_backlog_item_status"},

	// --- Tasks: reads -----------------------------------------------------
	{Method: http.MethodGet, Pattern: "/api/runs", Tool: "list_tasks"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}", Tool: "get_task"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/events", Tool: "list_task_events"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/gates", Tool: "get_task_gates"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/stages/{stageID}/results/{version}", Tool: "get_task_stage_result"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/workflow", Tool: "get_task_workflow"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/stage-statuses", Tool: "get_task_stage_statuses"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/template-version", Tool: "get_task_template_version"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/logs/{attemptID}", Tool: "get_task_log"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/artifacts/*", Tool: "read_task_artifact"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/artifact-revisions", Tool: "list_artifact_revisions"},
	{Method: http.MethodGet, Pattern: "/api/runs/{runID}/tracker", Tool: "get_task_tracker"},
	{Method: http.MethodGet, Pattern: "/api/events", Tool: "list_events"},
	{Method: http.MethodGet, Pattern: "/api/metrics/flow", Tool: "get_flow_metrics"},
	{Method: http.MethodGet, Pattern: "/api/metrics/flow.csv", Tool: "export_flow_metrics_csv"},
	{Method: http.MethodGet, Pattern: "/api/pipelines", Tool: "list_pipelines"},
	{Method: http.MethodGet, Pattern: "/api/pipelines/{id}", Tool: "get_pipeline"},
	{Method: http.MethodGet, Pattern: "/api/pipelines/{id}/artifacts", Tool: "list_task_artifacts"},
	{Method: http.MethodGet, Pattern: "/api/artifacts/*", Tool: "read_artifact"},

	// --- Tasks: actions ---------------------------------------------------
	{Method: http.MethodPost, Pattern: "/api/runs", Tool: "start_task"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/resume", Tool: "resume_task"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/cancel", Tool: "cancel_task"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/executor", Tool: "change_task_executor"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/delivery/retry", Tool: "retry_delivery"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/plan/approve", Tool: "approve_delivery_plan"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/approvals/{approvalID}/decisions", Tool: "decide_approval"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/artifact-revisions", Tool: "create_artifact_revision"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/gate-attempts/{attemptID}/retry", Tool: "retry_gate_attempt"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/executor", Tool: "set_stage_executor"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/take", Tool: "take_stage"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/run-agent", Tool: "run_stage_agent"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/results", Tool: "submit_stage_result"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/submit", Tool: "submit_stage_result", Alias: true},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/confirm", Tool: "confirm_stage"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/skip", Tool: "skip_stage"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/return", Tool: "return_stage"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/override", Tool: "override_stage_gate"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/gates/{gate}/retry", Tool: "retry_stage_gate"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/grill/answer", Tool: "answer_stage_grill"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/grill/new-version", Tool: "submit_stage_grill_version"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/stages/{stageID}/grill/done", Tool: "complete_stage_grill"},

	// --- Template and gate catalog ----------------------------------------
	{Method: http.MethodGet, Pattern: "/api/templates", Tool: "list_templates"},
	{Method: http.MethodGet, Pattern: "/api/template", Tool: "get_template"},
	{Method: http.MethodGet, Pattern: "/api/template/versions", Tool: "list_template_versions"},
	{Method: http.MethodGet, Pattern: "/api/gates/catalog", Tool: "get_gate_catalog"},
	{Method: http.MethodPost, Pattern: "/api/template/validate", Tool: "validate_template"},
	{Method: http.MethodPost, Pattern: "/api/template/publish", Tool: "publish_template"},

	// --- Team and roles ---------------------------------------------------
	{Method: http.MethodGet, Pattern: "/api/team/members", Tool: "list_team_members"},
	{Method: http.MethodGet, Pattern: "/api/team/audit", Tool: "get_team_audit"},
	{Method: http.MethodPost, Pattern: "/api/team/invitations", Tool: "invite_team_member"},
	{Method: http.MethodPatch, Pattern: "/api/team/members/{actorID}/roles", Tool: "set_team_member_roles"},
	{Method: http.MethodDelete, Pattern: "/api/team/members/{actorID}", Tool: "revoke_team_member"},

	// --- Task tracker -----------------------------------------------------
	{Method: http.MethodGet, Pattern: "/api/tracker", Tool: "get_tracker_status"},
	{Method: http.MethodGet, Pattern: "/api/tracker/conflicts", Tool: "list_tracker_link_conflicts"},
	{Method: http.MethodGet, Pattern: "/api/tracker/deliveries", Tool: "list_tracker_deliveries"},
	{Method: http.MethodGet, Pattern: "/api/tracker/sync", Tool: "get_tracker_sync"},
	{Method: http.MethodGet, Pattern: "/api/tracker/inbound", Tool: "list_tracker_inbound"},
	{Method: http.MethodPost, Pattern: "/api/tracker/sync", Tool: "sync_tracker_now"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/tracker/inbound/{changeID}/dismiss", Tool: "dismiss_tracker_inbound"},
	{Method: http.MethodPut, Pattern: "/api/runs/{runID}/tracker/link", Tool: "set_task_tracker_link"},
	{Method: http.MethodDelete, Pattern: "/api/runs/{runID}/tracker/link", Tool: "remove_task_tracker_link"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/tracker/retry", Tool: "retry_task_tracker_deliveries"},
	{Method: http.MethodPost, Pattern: "/api/runs/{runID}/tracker/deliveries/{eventID}/retry", Tool: "retry_task_tracker_delivery"},
}
