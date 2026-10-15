package web

// registerMCPReadTools declares the read-only product operations. Every entry
// maps to an existing REST GET route; answers are that route's JSON verbatim
// (apart from local pagination metadata where the REST route has none).
//
// Extension points: S11 (stage and gate state), S14 (tracker sync) and S15
// (external executors) append their reads here or in their own register
// function called from newMCPServer; S13 adds actions to the same registry.
func registerMCPReadTools(registry *mcpRegistry) {
	intPtr := func(value int) *int { return &value }
	runID := mcpParam{Name: "run_id", Kind: mcpString, Required: true,
		Description: "Task run ID (the run_id field of list_tasks / get_task)."}
	projectID := mcpParam{Name: "project_id", Kind: mcpInteger, Min: intPtr(1),
		Description: "Only entries of this project (numeric project ID from list_projects)."}
	eventParams := []mcpParam{
		{Name: "after", Kind: mcpInteger, Min: intPtr(0), Description: "Return events with a cursor greater than this (ascending order)."},
		{Name: "before", Kind: mcpInteger, Min: intPtr(1), Description: "Return events with a cursor below this; requires order=desc."},
		{Name: "order", Kind: mcpString, Enum: []string{"asc", "desc"}, Description: "Event order (default asc)."},
		{Name: "limit", Kind: mcpInteger, Min: intPtr(1), Max: intPtr(1000), Description: "Events per page (default 100, max 1000)."},
		{Name: "warnings", Kind: mcpBoolean, Description: "true: only warning events (overrides, skips, stalls, integration errors)."},
	}

	registry.add(mcpOperation{Name: "whoami", Title: "Current identity",
		Description: "The authenticated principal (actor ID and roles) every other call is authorized as.",
		Path:        "/api/auth/me"})

	registry.add(mcpOperation{Name: "list_projects", Title: "Projects",
		Description: "Projects (folders every task belongs to). Archived projects are hidden unless include_archived is true.",
		Path:        "/api/projects", ArrayKey: "projects", Resource: "aiteam://projects",
		Params: []mcpParam{{Name: "include_archived", Kind: mcpBoolean, Description: "Include archived projects."}}})
	registry.add(mcpOperation{Name: "get_project", Title: "Project",
		Description: "One project by numeric ID.", Path: "/api/projects/{project_id}", Resource: "aiteam://projects/{project_id}",
		Params: []mcpParam{{Name: "project_id", Kind: mcpInteger, Required: true, Min: intPtr(1), Description: "Numeric project ID."}}})

	registry.add(mcpOperation{Name: "list_backlog", Title: "Backlog",
		Description: "Idea backlog items (draft, ready, claimed) with the auto-pickup policy; optionally one project.",
		Path:        "/api/backlog", ArrayKey: "items", Params: []mcpParam{projectID}})

	registry.add(mcpOperation{Name: "list_tasks", Title: "Tasks",
		Description: "Tasks (runs) newest first with filters. Each item carries status, phase, current stage and function, executor and the actions the caller may take. Paginated by limit (1-100, default 50) and offset; total is the filtered count.",
		Path:        "/api/runs",
		Params: []mcpParam{
			projectID,
			{Name: "status", Kind: mcpString, Description: "Run status or phase to match."},
			{Name: "stage", Kind: mcpString, Description: "Current stage ID."},
			{Name: "function", Kind: mcpString, Description: "Function (role) of the current stage."},
			{Name: "mode", Kind: mcpString, Enum: []string{"manual", "auto"}, Description: "Task mode."},
			{Name: "limit", Kind: mcpInteger, Min: intPtr(1), Max: intPtr(100), Description: "Page size (default 50, max 100)."},
			{Name: "offset", Kind: mcpInteger, Min: intPtr(0), Description: "Entries to skip."},
		}})
	registry.add(mcpOperation{Name: "get_task", Title: "Task snapshot",
		Description: "Full task snapshot, identical to the REST task view: run, stage attempts, lifecycle state, approvals, stage owners, human results, pinned template, delivery plan and the actions the caller may take.",
		Path:        "/api/runs/{run_id}", Resource: "aiteam://tasks/{run_id}", Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "get_task_stage_result", Title: "Stage result",
		Description: "One version of a human stage result (the submitted artifact text).",
		Path:        "/api/runs/{run_id}/stages/{stage_id}/results/{version}", Resource: "aiteam://tasks/{run_id}/stages/{stage_id}/results/{version}",
		Params: []mcpParam{runID,
			{Name: "stage_id", Kind: mcpString, Required: true, Description: "Stage ID from the task template."},
			{Name: "version", Kind: mcpString, Required: true, Description: "Result version number."}}})
	registry.add(mcpOperation{Name: "get_task_workflow", Title: "Task workflow",
		Description: "Workflow graph and per-stage state of the task as the UI route view shows it.",
		Path:        "/api/runs/{run_id}/workflow", Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "get_task_template_version", Title: "Pinned template version",
		Description: "The process template version this task is pinned to, with its stages and gates.",
		Path:        "/api/runs/{run_id}/template-version", Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "list_task_events", Title: "Task events",
		Description: "Append-only event feed of one task: stage transitions, checks, decisions, overrides. Cursor paging: pass next_cursor as after (asc) or before (desc) while has_more is true.",
		Path:        "/api/runs/{run_id}/events", Params: append([]mcpParam{runID}, eventParams...)})
	registry.add(mcpOperation{Name: "list_events", Title: "Global events",
		Description: "Event feed across all tasks (optionally one run). Same cursor paging as list_task_events.",
		Path:        "/api/events", Params: append([]mcpParam{{Name: "run_id", Kind: mcpString, Description: "Only this run."}}, eventParams...)})
	registry.add(mcpOperation{Name: "get_task_log", Title: "Attempt log",
		Description: "Tail (at most 64 KiB) of the log of one stage attempt; truncated=true and offset tell how much precedes it.",
		Path:        "/api/runs/{run_id}/logs/{attempt_id}",
		Params: []mcpParam{runID, {Name: "attempt_id", Kind: mcpString, Required: true,
			Description: "Attempt ID from the stage attempts of get_task."}}})
	registry.add(mcpOperation{Name: "list_task_artifacts", Title: "Task artifacts",
		Description: "Evidence and report files of a task (attempts, reports, brief) with size and modification time. Use read_task_artifact for content.",
		Path:        "/api/pipelines/{id}/artifacts", ArrayKey: "artifacts", RootArray: true,
		Params: []mcpParam{{Name: "id", Kind: mcpInteger, Required: true, Min: intPtr(1),
			Description: "Numeric task ID (the id field of list_tasks / get_task run)."}}})
	registry.add(mcpOperation{Name: "read_task_artifact", Title: "Read artifact",
		Description: "Text content of one task artifact by path (as listed by list_task_artifacts). Large files come in byte_offset slices.",
		Path:        "/api/runs/{run_id}/artifacts/{path}",
		Params: []mcpParam{runID, {Name: "path", Kind: mcpString, Required: true, Subpath: true,
			Description: "Artifact path relative to the run, e.g. reports/checks.json."}}})
	registry.add(mcpOperation{Name: "list_artifact_revisions", Title: "Artifact revisions",
		Description: "Revision history of a human-edited artifact of the task.",
		Path:        "/api/runs/{run_id}/artifact-revisions",
		Params: []mcpParam{runID, {Name: "path", Kind: mcpString, Required: true,
			Description: "Artifact path inside the run."}}})

	registry.add(mcpOperation{Name: "list_templates", Title: "Templates",
		Description: "The active process template (YAML, stages, graph, validation) and its versions.",
		Path:        "/api/templates", Resource: "aiteam://templates"})
	registry.add(mcpOperation{Name: "get_template", Title: "Active template",
		Description: "The active process template in the editor view.", Path: "/api/template", Resource: "aiteam://template"})
	registry.add(mcpOperation{Name: "list_template_versions", Title: "Template versions",
		Description: "Published versions of the process template, newest first.",
		Path:        "/api/template/versions", ArrayKey: "versions", Resource: "aiteam://template/versions"})
	registry.add(mcpOperation{Name: "get_gate_catalog", Title: "Gate catalog",
		Description: "Versioned catalog of gate kinds a template stage may use, with their contracts.",
		Path:        "/api/gates/catalog", Resource: "aiteam://gates/catalog"})

	registry.add(mcpOperation{Name: "get_flow_metrics", Title: "Flow metrics",
		Description: "Lead/cycle time, per-stage statistics, overrides, skips, returns and token usage for a period (default last 30 days) or one run.",
		Path:        "/api/metrics/flow",
		Params: []mcpParam{
			{Name: "from", Kind: mcpString, Description: "Period start, RFC3339 or YYYY-MM-DD."},
			{Name: "to", Kind: mcpString, Description: "Period end (exclusive), RFC3339 or YYYY-MM-DD."},
			{Name: "run_id", Kind: mcpString, Description: "Restrict to one run (whole run unless a period is given)."}}})

	registry.add(mcpOperation{Name: "list_team_members", Title: "Team and roles",
		Description: "Team members with status and roles. Requires the team management right; others get a forbidden error.",
		Path:        "/api/team/members", ArrayKey: "members"})
	registry.add(mcpOperation{Name: "get_team_audit", Title: "Team audit",
		Description: "Latest team audit entries (invitations, role changes, token issue/revoke). Requires the team management right.",
		Path:        "/api/team/audit", ArrayKey: "events"})
	registry.add(mcpOperation{Name: "get_preflight", Title: "Runtime preflight",
		Description: "Readiness report of the run controller (tools, credentials, template).", Path: "/api/preflight"})
}
