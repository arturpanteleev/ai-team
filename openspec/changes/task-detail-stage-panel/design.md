# Design: task detail and stage panel

The route is keyed by the immutable run ID and reads `GET /api/runs/{runID}`.
The API response includes the task metadata and resolves the template from
the run's stored pin; the current project template is not used to render a
historical task. Existing task-stage action routes remain the source of writes.

The page uses `allowed_actions` as the action boundary. Each action is shown
only when the server reports it for the current lifecycle checkpoint, and
action errors retain the server's explanation before refreshing the projection.
Long-running states refresh from matching WebSocket events and a visible-page
poll so the page remains useful after a missed connection.

Result revisions and resolved grill approvals stay visible after the current
stage advances. Checker evidence and attempt artifacts are presented alongside
the corresponding stage attempt, with a direct log link. The same route works
for manual and automatic modes; automatic mode simply exposes the actions
provided for that mode's lifecycle state.
