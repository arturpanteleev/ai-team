package web

import "net/http"

// registerMCPActionTools declares every product action of the Web/REST API
// (S13) and the reads S12 left out. As with the read tools, each entry names
// one REST route: the call is served by the same router, handler, middleware
// and RBAC as a browser request, under the caller's identity, so a tool can
// never do what the caller's role does not allow in the web UI.
//
// Conventions shared by all actions:
//   - POST actions require idempotency_key (see mcpOperation.keyRequired);
//   - an action that moves a task answers at once (202) with the task ID; the
//     result is read with get_task, get_task_gates and list_task_events;
//   - the person is always the token owner: no tool accepts an actor_id.
func registerMCPActionTools(registry *mcpRegistry) {
	intPtr := func(value int) *int { return &value }
	runID := mcpParam{Name: "run_id", Kind: mcpString, Required: true,
		Description: "Task run ID (the run_id field of list_tasks / get_task)."}
	stageID := mcpParam{Name: "stage_id", Kind: mcpString, Required: true, Description: "Stage ID from the task template."}
	approvalID := mcpParam{Name: "approval_id", Kind: mcpString, Required: true,
		Description: "Approval ID (the pending approval of get_task)."}
	reason := func(required bool, text string) mcpParam {
		return mcpParam{Name: "reason", Kind: mcpString, Body: true, Required: required, Description: text}
	}
	subjectHash := func(text string) mcpParam {
		return mcpParam{Name: "subject_hash", Kind: mcpString, Body: true, Required: true, Digest: true, Description: text}
	}
	projectPath := mcpParam{Name: "project_id", Kind: mcpInteger, Required: true, Min: intPtr(1), Description: "Numeric project ID."}

	// --- Projects ---------------------------------------------------------
	registry.add(mcpOperation{Name: "create_project", Title: "Create project", Method: http.MethodPost, Path: "/api/projects",
		Description: "Create a project (folder). Every task and backlog item belongs to exactly one active project. Needs the right to create projects.",
		Params: []mcpParam{
			{Name: "name", Kind: mcpString, Body: true, Required: true, Description: "Project name."},
			{Name: "description", Kind: mcpString, Body: true, Description: "Short description."}}})
	registry.add(mcpOperation{Name: "update_project", Title: "Update project", Method: http.MethodPatch, Path: "/api/projects/{project_id}",
		Description: "Rename a project or change its description. Send only the fields to change.",
		Params: []mcpParam{projectPath,
			{Name: "name", Kind: mcpString, Body: true, Description: "New name."},
			{Name: "description", Kind: mcpString, Body: true, Description: "New description."}}})
	registry.add(mcpOperation{Name: "archive_project", Title: "Archive project", Method: http.MethodPost, Path: "/api/projects/{project_id}/archive",
		Description: "Archive a project: it stops accepting new tasks, its history stays readable.", Destructive: true,
		Params: []mcpParam{projectPath}})
	registry.add(mcpOperation{Name: "unarchive_project", Title: "Unarchive project", Method: http.MethodPost, Path: "/api/projects/{project_id}/unarchive",
		Description: "Return an archived project to active use.", Params: []mcpParam{projectPath}})

	// --- Backlog ----------------------------------------------------------
	registry.add(mcpOperation{Name: "create_backlog_item", Title: "Create backlog item", Method: http.MethodPost, Path: "/api/backlog",
		Description: "Add an idea to the backlog of a project (draft). Mark it ready with set_backlog_item_status to let auto-pickup start it.",
		Params: []mcpParam{
			{Name: "project_id", Kind: mcpInteger, Body: true, Required: true, Min: intPtr(1), Description: "Numeric project ID."},
			{Name: "title", Kind: mcpString, Body: true, Required: true, Description: "Short title."},
			{Name: "idea", Kind: mcpString, Body: true, Long: true, Description: "The idea text."},
			{Name: "feature", Kind: mcpString, Body: true, Required: true, Description: "Feature slug the task will be developed under."}}})
	registry.add(mcpOperation{Name: "set_backlog_item_status", Title: "Set backlog item status", Method: http.MethodPost, Path: "/api/backlog/{item_id}/status",
		Description: "Move a backlog item between draft, ready and discarded.",
		Params: []mcpParam{
			{Name: "item_id", Kind: mcpInteger, Required: true, Min: intPtr(1), Description: "Numeric backlog item ID (list_backlog)."},
			{Name: "status", Kind: mcpString, Body: true, Required: true, Enum: []string{"draft", "ready", "discarded"}, Description: "New status."}}})

	// --- Tasks ------------------------------------------------------------
	registry.add(mcpOperation{Name: "start_task", Title: "Create and start a task", Method: http.MethodPost, Path: "/api/runs",
		Description: "Create a task in an active project and start it on the active process template. Returns {run_id, template_version} immediately; " +
			"follow the task with get_task and list_task_events. Needs the start right.",
		Params: []mcpParam{
			{Name: "project_id", Kind: mcpInteger, Body: true, Required: true, Min: intPtr(1), Description: "Numeric project ID (list_projects). Required."},
			{Name: "feature", Kind: mcpString, Body: true, Required: true, Description: "Feature slug."},
			{Name: "task", Kind: mcpString, Body: true, Required: true, Long: true, Description: "Task description."},
			{Name: "title", Kind: mcpString, Body: true, Description: "Short title."},
			{Name: "idea", Kind: mcpString, Body: true, Long: true, Description: "Idea text."},
			{Name: "template", Kind: mcpString, Body: true, Description: "Expected template name; must match the active template."},
			{Name: "template_version", Kind: mcpString, Body: true, Description: "Expected template version; must be the active one."},
			{Name: "mode", Kind: mcpString, Body: true, Enum: []string{"manual", "auto"}, Description: "Task mode (default manual)."},
			{Name: "external_links", Kind: mcpStringArray, Body: true, Description: "Links to external tickets (\"KEY-1 https://...\")."}}})
	registry.add(mcpOperation{Name: "resume_task", Title: "Resume task", Method: http.MethodPost, Path: "/api/runs/{run_id}/resume",
		Description: "Resume a waiting or interrupted task. grant_budget_allowance (with a reason) gives the task more token budget; upgrade_runtime accepts a changed controller binary.",
		Params: []mcpParam{runID,
			{Name: "grant_budget_allowance", Kind: mcpBoolean, Body: true, Description: "Grant a new token budget (needs reason)."},
			reason(false, "Why the budget is granted (only with grant_budget_allowance)."),
			{Name: "upgrade_runtime", Kind: mcpBoolean, Body: true, Description: "Accept that the controller binary changed since the task started."}}})
	registry.add(mcpOperation{Name: "cancel_task", Title: "Cancel task", Method: http.MethodPost, Path: "/api/runs/{run_id}/cancel",
		Description: "Cancel a task. Needs the cancel right.", Destructive: true, Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "retry_delivery", Title: "Retry delivery", Method: http.MethodPost, Path: "/api/runs/{run_id}/delivery/retry",
		Description: "Retry the controller-owned delivery (commit, push, pull request) of a task whose delivery failed after its plan was approved. " +
			"The controller re-checks the approved plan hash; nothing outside that plan can be delivered. Needs the delivery right.",
		Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "approve_delivery_plan", Title: "Approve delivery plan", Method: http.MethodPost, Path: "/api/runs/{run_id}/plan/approve",
		Description: "Approve the exact delivery plan awaiting approval. plan_hash must be the SHA-256 shown in get_task (delivery plan); a stale or wrong hash is refused. " +
			"Needs the role the plan approval requires, and the person who prepared the plan may be refused by the self-approval rule.",
		Params: []mcpParam{runID, {Name: "plan_hash", Kind: mcpString, Body: true, Required: true, Digest: true,
			Description: "SHA-256 of the canonical delivery plan you reviewed."}}})
	registry.add(mcpOperation{Name: "change_task_executor", Title: "Change executor of the current stage", Method: http.MethodPost, Path: "/api/runs/{run_id}/executor",
		Description: "Switch who performs a stage (human or agent) addressing the stage in the body; prefer set_stage_executor.",
		Params: []mcpParam{runID,
			{Name: "stage_id", Kind: mcpString, Body: true, Required: true, Description: "Stage ID."},
			{Name: "executor", Kind: mcpString, Body: true, Required: true, Enum: []string{"human", "agent"}, Description: "New executor."}}})

	// --- Stage actions ----------------------------------------------------
	registry.add(mcpOperation{Name: "set_stage_executor", Title: "Choose stage executor", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/executor",
		Description: "Choose who performs the ready stage: a human or its configured agent. Needs the stage's function.",
		Params: []mcpParam{runID, stageID,
			{Name: "executor", Kind: mcpString, Body: true, Required: true, Enum: []string{"human", "agent"}, Description: "human or agent."}}})
	registry.add(mcpOperation{Name: "take_stage", Title: "Take stage", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/take",
		Description: "Take a ready human stage as its owner. Needs the stage's function.", Params: []mcpParam{runID, stageID}})
	registry.add(mcpOperation{Name: "run_stage_agent", Title: "Run stage agent", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/run-agent",
		Description: "Start the stage's agent (action run) or ask it to refine a previous result (refine). Returns at once (202); follow the attempt with get_task and get_task_log. " +
			"The launcher counts as author of the result for the self-approval rule.",
		Params: []mcpParam{runID, stageID,
			{Name: "action", Kind: mcpString, Body: true, Enum: []string{"run", "refine"}, Description: "run (default) or refine."}}})
	registry.add(mcpOperation{Name: "submit_stage_result", Title: "Submit stage result", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/results",
		Description: "Submit the result of a human stage: text (content or content_base64), a link (url) or an approval note, according to the stage's result type. " +
			"The result becomes a new immutable version; the response carries its version and sha256 (the subject_hash for the confirmation). Needs the stage's function. Up to about 3 MiB.",
		Params: []mcpParam{runID, stageID,
			{Name: "result", Kind: mcpString, Body: true, Required: true, Description: "Result type of the stage (md, text, link or approve; see get_task_template_version)."},
			{Name: "content", Kind: mcpString, Body: true, Long: true, Description: "Result text (markdown or plain)."},
			{Name: "content_base64", Kind: mcpString, Body: true, Long: true, Description: "Result text as base64 of UTF-8 (alternative to content)."},
			{Name: "url", Kind: mcpString, Body: true, Description: "Link for a link result."},
			{Name: "link_kind", Kind: mcpString, Body: true, Description: "Kind of link, if the stage requires one."},
			{Name: "note", Kind: mcpString, Body: true, Long: true, Description: "Note for the reviewer."},
			{Name: "description", Kind: mcpString, Body: true, Description: "Short description of the submission."},
			{Name: "actor_role", Kind: mcpString, Body: true, Description: "The stage role you submit as, when you hold several (checked against your roles)."}}})
	registry.add(mcpOperation{Name: "confirm_stage", Title: "Confirm stage", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/confirm",
		Description: "Confirm the finished stage so the task moves on. subject_hash is mandatory over MCP and must equal the hash of the result version awaiting confirmation " +
			"(pending approval of get_task); a stale hash is refused, so you can only confirm what you reviewed. Needs the approver role; self-approval is refused where the template forbids it.",
		Params: []mcpParam{runID, stageID, subjectHash("SHA-256 of the exact result version you reviewed.")}})
	registry.add(mcpOperation{Name: "skip_stage", Title: "Skip stage", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/skip",
		Description: "Skip a skippable stage. A reason is mandatory and recorded; the skip is shown as a skip, never as a pass.",
		Params:      []mcpParam{runID, stageID, reason(true, "Why the stage is skipped.")}})
	registry.add(mcpOperation{Name: "return_stage", Title: "Return stage", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/return",
		Description: "Return the task from this stage to an earlier stage the template allows. A reason is mandatory.",
		Params: []mcpParam{runID, stageID,
			{Name: "to_stage", Kind: mcpString, Body: true, Required: true, Description: "Target stage ID (a configured return route)."},
			reason(true, "Why the task is returned.")}})
	registry.add(mcpOperation{Name: "override_stage_gate", Title: "Override failed gate (administrator)", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/override",
		Description: "Pass a task on after a required gate failed. Only a system administrator may do this (403 otherwise, whatever the stage function); " +
			"a reason is mandatory. The override is an immutable record bound to the result version; the gate outcome stays red.",
		Destructive: true, Params: []mcpParam{runID, stageID, reason(true, "Why the failed gate is overridden.")}})
	registry.add(mcpOperation{Name: "retry_stage_gate", Title: "Retry gate", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/gates/{gate}/retry",
		Description: "Re-run a gate that ended in a technical error or timeout on the same result version; passed gates of the group are not repeated.",
		Params: []mcpParam{runID, stageID,
			{Name: "gate", Kind: mcpString, Required: true, Description: "Gate instance name (get_task_gates)."},
			{Name: "gates", Kind: mcpStringArray, Body: true, Description: "Further gate instance names to retry together."},
			reason(false, "Why the retry is requested.")}})
	registry.add(mcpOperation{Name: "retry_gate_attempt", Title: "Retry gate attempt", Method: http.MethodPost, Path: "/api/runs/{run_id}/gate-attempts/{attempt_id}/retry",
		Description: "The same technical retry addressed by the ID of the failed attempt (get_task_gates). Refused unless it is the latest attempt of its gate and a technical failure.",
		Params: []mcpParam{runID,
			{Name: "attempt_id", Kind: mcpString, Required: true, Description: "Gate attempt ID."},
			reason(false, "Why the retry is requested.")}})
	registry.add(mcpOperation{Name: "answer_stage_grill", Title: "Answer grill questions", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/grill/answer",
		Description: "Answer the questions a pending grill asks about the stage result.",
		Params:      []mcpParam{runID, stageID, {Name: "answer", Kind: mcpString, Body: true, Required: true, Long: true, Description: "The answer."}}})
	registry.add(mcpOperation{Name: "submit_stage_grill_version", Title: "Submit new version in grill", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/grill/new-version",
		Description: "Replace the result with a revised version while a grill is pending (create the revision with create_artifact_revision first).",
		Params:      []mcpParam{runID, stageID, {Name: "answer", Kind: mcpString, Body: true, Long: true, Description: "Note about the new version."}}})
	registry.add(mcpOperation{Name: "complete_stage_grill", Title: "Complete grill", Method: http.MethodPost, Path: "/api/runs/{run_id}/stages/{stage_id}/grill/done",
		Description: "Finish the grill when no more questions are needed.", Params: []mcpParam{runID, stageID}})
	registry.add(mcpOperation{Name: "decide_approval", Title: "Decide approval", Method: http.MethodPost, Path: "/api/runs/{run_id}/approvals/{approval_id}/decisions",
		Description: "Record a decision (approve, reject, return_*, answer_questions, ...) on a pending approval. subject_hash is mandatory over MCP and must equal the approval's " +
			"subject hash (get_task); the decision service checks your role, the hash and the self-approval rule exactly as for the web UI. A repeated decision by the same person, " +
			"action, hash and role is recognised as a replay and does not count twice. Override and gate retries have their own tools. " +
			"This door only records the decision: once a stage transition is resolved, call resume_task to carry the task on (confirm_stage records and resumes in one step).",
		Params: []mcpParam{runID, approvalID,
			{Name: "action", Kind: mcpString, Body: true, Required: true, Description: "Action the approval offers (actions of the pending approval in get_task)."},
			{Name: "actor_role", Kind: mcpString, Body: true, Description: "The approval role you act as (required_roles of the approval; you must hold it)."},
			subjectHash("SHA-256 of the exact subject you reviewed (subject_hash of the pending approval)."),
			{Name: "comment", Kind: mcpString, Body: true, Long: true, Description: "Comment or answer text."},
			{Name: "artifact_revisions", Kind: mcpStringMap, Body: true, Description: "Artifact path to revision ID the decision is about."}}})
	registry.add(mcpOperation{Name: "create_artifact_revision", Title: "Create artifact revision", Method: http.MethodPost, Path: "/api/runs/{run_id}/artifact-revisions",
		Description: "Save a human-edited revision of a task artifact. base_revision or base_sha256 guards against overwriting a newer revision.",
		Params: []mcpParam{runID,
			{Name: "artifact_path", Kind: mcpString, Body: true, Required: true, Description: "Artifact path inside the run (list_task_artifacts)."},
			{Name: "base_revision", Kind: mcpString, Body: true, Description: "Revision ID this edit is based on."},
			{Name: "base_sha256", Kind: mcpString, Body: true, Description: "SHA-256 of the content this edit is based on."},
			{Name: "content", Kind: mcpString, Body: true, Long: true, Description: "New content."},
			{Name: "comment", Kind: mcpString, Body: true, Long: true, Description: "Comment on the change."}}})

	// --- Template ---------------------------------------------------------
	registry.add(mcpOperation{Name: "validate_template", Title: "Validate template", Method: http.MethodPost, Path: "/api/template/validate", ReadOnly: true,
		Description: "Check a process template YAML without saving it: returns validity, the diagnostic and the resulting graph. Needs the template edit right.",
		Params: []mcpParam{{Name: "yaml", Kind: mcpString, Body: true, Required: true, Long: true, Description: "Template YAML."},
			{Name: "expected_version", Kind: mcpString, Body: true, Description: "Template version the edit is based on."}}})
	registry.add(mcpOperation{Name: "publish_template", Title: "Publish template", Method: http.MethodPost, Path: "/api/template/publish",
		Description: "Publish a new template version. Running tasks keep their pinned version. expected_version guards against overwriting a concurrent publication. Needs the template edit right.",
		Params: []mcpParam{{Name: "yaml", Kind: mcpString, Body: true, Required: true, Long: true, Description: "Template YAML."},
			{Name: "expected_version", Kind: mcpString, Body: true, Description: "Version this edit is based on (list_templates)."}}})

	// --- Team and roles ---------------------------------------------------
	registry.add(mcpOperation{Name: "invite_team_member", Title: "Invite team member", Method: http.MethodPost, Path: "/api/team/invitations",
		Description: "Invite a person by e-mail with roles. Needs the team management right. The response contains the one-time activation token for the invited person; " +
			"it is shown by the first delivery only (a repeated delivery with the same key does not store or repeat it).",
		Redact: []string{"activation_token"},
		Params: []mcpParam{{Name: "email", Kind: mcpString, Body: true, Required: true, Description: "E-mail address."},
			{Name: "roles", Kind: mcpStringArray, Body: true, Required: true, Description: "Roles to grant (for example developer, reviewer, product_owner, admin)."}}})
	registry.add(mcpOperation{Name: "set_team_member_roles", Title: "Set team member roles", Method: http.MethodPatch, Path: "/api/team/members/{actor_id}/roles",
		Description: "Replace the roles of a team member. Needs the team management right; the team keeps at least one Product Owner and one administrator.",
		Params: []mcpParam{{Name: "actor_id", Kind: mcpString, Required: true, Description: "Team member ID (list_team_members)."},
			{Name: "roles", Kind: mcpStringArray, Body: true, Required: true, Description: "The complete new role list."}}})
	registry.add(mcpOperation{Name: "revoke_team_member", Title: "Revoke team member", Method: http.MethodDelete, Path: "/api/team/members/{actor_id}", Destructive: true,
		Description: "Revoke a team member's access (their tokens stop working at once). Needs the team management right.",
		Params:      []mcpParam{{Name: "actor_id", Kind: mcpString, Required: true, Description: "Team member ID."}}})

	// --- Task tracker -----------------------------------------------------
	registry.add(mcpOperation{Name: "set_task_tracker_link", Title: "Link task to tracker issue", Method: http.MethodPut, Path: "/api/runs/{run_id}/tracker/link",
		Description: "Link the task with exactly one existing tracker issue (the key format, reachability and uniqueness are checked). Needs the tracker link right.",
		Params:      []mcpParam{runID, {Name: "issue_key", Kind: mcpString, Body: true, Required: true, Description: "Issue key, e.g. PROJ-123."}}})
	registry.add(mcpOperation{Name: "remove_task_tracker_link", Title: "Unlink task from tracker issue", Method: http.MethodDelete, Path: "/api/runs/{run_id}/tracker/link",
		Description: "Remove the task's tracker link.", Destructive: true, Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "retry_task_tracker_deliveries", Title: "Retry tracker deliveries", Method: http.MethodPost, Path: "/api/runs/{run_id}/tracker/retry",
		Description: "Retry every failed outgoing tracker update of the task.", Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "retry_task_tracker_delivery", Title: "Retry one tracker delivery", Method: http.MethodPost, Path: "/api/runs/{run_id}/tracker/deliveries/{event_id}/retry",
		Description: "Retry one failed outgoing tracker update (event ID from get_task_tracker).",
		Params:      []mcpParam{runID, {Name: "event_id", Kind: mcpString, Required: true, Description: "Delivery event ID."}}})

	// --- Reads that S12 did not need --------------------------------------
	registry.add(mcpOperation{Name: "get_version", Title: "Server version", Path: "/api/version",
		Description: "Build revision of the running ai-team server."})
	registry.add(mcpOperation{Name: "get_task_tracker", Title: "Task tracker state",
		Description: "Tracker link of a task, link conflicts, recent outgoing deliveries and a one-word sync status.",
		Path:        "/api/runs/{run_id}/tracker", Params: []mcpParam{runID}})
	registry.add(mcpOperation{Name: "get_tracker_status", Title: "Tracker status",
		Description: "Whether a task tracker is configured, its transition map and the number of failed deliveries and link conflicts.",
		Path:        "/api/tracker"})
	registry.add(mcpOperation{Name: "list_tracker_conflicts", Title: "Tracker link conflicts",
		Description: "Link conflicts between tasks and tracker issues that need an operator.", Path: "/api/tracker/conflicts"})
	registry.add(mcpOperation{Name: "list_tracker_deliveries", Title: "Tracker deliveries",
		Description: "Outgoing tracker updates across tasks, optionally by status.", Path: "/api/tracker/deliveries",
		Params: []mcpParam{{Name: "status", Kind: mcpString, Enum: []string{"failed", "delivered", "abandoned", "superseded"}, Description: "Only deliveries with this status."}}})
	registry.add(mcpOperation{Name: "export_flow_metrics_csv", Title: "Flow metrics (CSV)",
		Description: "The flow metrics of get_flow_metrics as CSV text.", Path: "/api/metrics/flow.csv",
		Params: []mcpParam{
			{Name: "from", Kind: mcpString, Description: "Period start, RFC3339 or YYYY-MM-DD."},
			{Name: "to", Kind: mcpString, Description: "Period end (exclusive), RFC3339 or YYYY-MM-DD."},
			{Name: "run_id", Kind: mcpString, Description: "Restrict to one run."}}})
	registry.add(mcpOperation{Name: "list_pipelines", Title: "Pipeline runs (legacy view)",
		Description: "Pipeline runs as the legacy dashboard lists them; list_tasks is the richer view.",
		Path:        "/api/pipelines", ArrayKey: "pipelines", RootArray: true})
	registry.add(mcpOperation{Name: "get_pipeline", Title: "Pipeline run (legacy view)",
		Description: "One pipeline run with its recorded stages (numeric ID).", Path: "/api/pipelines/{id}",
		Params: []mcpParam{{Name: "id", Kind: mcpInteger, Required: true, Min: intPtr(1), Description: "Numeric run ID."}}})
	registry.add(mcpOperation{Name: "read_artifact", Title: "Read stored artifact",
		Description: "Text of a file in the shared artifact store by path (legacy view; read_task_artifact is addressed by run).",
		Path:        "/api/artifacts/{path}",
		Params:      []mcpParam{{Name: "path", Kind: mcpString, Required: true, Subpath: true, Description: "Path inside the artifact store."}}})
}
