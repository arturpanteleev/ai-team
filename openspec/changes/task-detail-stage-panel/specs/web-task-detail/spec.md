## ADDED Requirements

### Requirement: Stable task detail page

The web UI MUST provide a task detail page at `/tasks/{runID}` keyed by stable
run ID. The page MUST display task metadata and the immutable template pinned
to that run. It MUST show current-stage actions only when those actions appear
in the task detail response's `allowed_actions` list.

#### Scenario: Inspect a task

- **WHEN** a user opens a task by its run ID
- **THEN** the page MUST show its task text, mode, status, external links, and
  pinned template version
- **AND** MUST show stage results with their revisions and a comparison when
  multiple revisions exist
- **AND** MUST show task events, checker evidence, and links to attempt logs

#### Scenario: Restrict stage actions

- **WHEN** the task lifecycle reports a set of allowed actions
- **THEN** the page MUST render only the controls corresponding to that set
- **AND** MUST refresh the task projection after a successful or rejected
  action

#### Scenario: Review grills and delivery plans

- **WHEN** a task has a pending grill or delivery-plan approval
- **THEN** the page MUST show the pending questions or plan and the action
  allowed by the lifecycle
- **AND** resolved grill questions and decisions MUST remain in task history

#### Scenario: Traverse either run mode

- **WHEN** a task uses manual or automatic mode
- **THEN** a user MUST be able to traverse its full pinned template from the
  browser
- **AND** controls unavailable in that mode or state MUST remain hidden

### Requirement: Task detail API presentation metadata

`GET /api/runs/{runID}` MUST return task text, mode, external links, and the
run's pinned template description so the task detail page does not infer
historical state from the current project template.

#### Scenario: Historical task template

- **WHEN** a task's pinned template differs from the active project template
- **THEN** task detail MUST return the pinned template's stages, returns, and
  version for rendering the task page
