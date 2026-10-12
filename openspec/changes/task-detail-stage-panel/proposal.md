# Task detail page and stage panel (B-62)

## Proposal

Add a stable browser page at `/tasks/{runID}` that shows the task's pinned
template, lifecycle state, stage history, results, approvals, events, and
available actions. The page supports manual and automatic runs and keeps each
action aligned with the server's current `allowed_actions` response.

## Motivation

The task board and creation flow already use stable run IDs, but a person still
needs to navigate into the run, understand its current stage, and act on its
result, checker, grill, or delivery-plan state. Reusing the pinned template and
the task API makes the browser view reflect the workflow that actually started
the run, even if the project's active template later changes.

## Scope

- Extend task detail responses with task text, mode, external links, and the
  pinned template description.
- Add the task detail route and connect board cards and task creation to it.
- Show state-specific actions, immutable result revisions and comparison,
  checker evidence and logs, grill history, event history, and delivery-plan
  approval.
- Cover a complete manual and automatic browser traversal with action gating.

## Acceptance criteria

- `/tasks/{runID}` resolves a task through its stable run ID and displays the
  run's pinned template/version and task metadata.
- The page renders task actions only when the API lists them in
  `allowed_actions`.
- Results, revisions, comparison, checker report, grill questions/answers,
  events, and agent logs are available from the task page.
- Browser E2E traverses a complete manual and automatic route and verifies
  invalid actions remain hidden.
- `GOTOOLCHAIN=go1.26.9 make verify` passes.
