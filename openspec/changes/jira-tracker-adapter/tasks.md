# Jira task tracker adapter (B-80) — tasks

- [x] Add tracker adapter/event types and Jira Cloud stage transition/comment
  behavior with injectable HTTP client.
- [x] Add optional strict project template configuration, environment-based
  token loading, and server wiring while keeping one selected template/graph.
- [x] Subscribe through the durable SQLite event cursor and project
  `integration_error` without affecting task outcomes.
- [x] Add Jira mock-server, task-event integration, restart replay, cursor, and
  failure-isolation regressions.
- [x] Update OpenSpec, architecture, and user-facing configuration reference.
- [x] Run `GOTOOLCHAIN=go1.26.9 make verify` on the final tree.
