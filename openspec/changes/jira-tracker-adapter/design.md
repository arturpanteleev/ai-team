# Design: Jira task tracker adapter

`pkg/tracker.Adapter` accepts task events through an injectable interface.
The web server consumes `attempt_finished` rows by a per-adapter SQLite cursor,
enriches each event with the task's persisted `external_links`, and runs the
adapter in a background worker. Its first cursor is initialized at the current
tail so enabling the integration does not replay old tasks. Subsequent starts
resume from the saved cursor.

The Jira adapter uses an injected `http.Client`, Jira Cloud REST v3, and
Basic auth from `email` plus a token read from the configured environment
variable. It resolves the Jira issue key from external links that are exactly
`KEY-N` or a `<base_url>/browse/KEY-N` URL on the `base_url` host (never by
substring search). For a successful attempt (`status=passed`, `outcome=passed`,
decision not `rejected`) on a mapped stage it checks the current status and
selects a currently available transition by destination status; other outcomes
skip the transition. It then always posts an ADF comment with the task detail
URL and event result. A stage without a mapping still receives a comment. No inbound Jira path exists.

On adapter failure the web worker appends `integration_error` to SQLite with
warning severity, then advances the source cursor. If warning persistence or
cursor persistence fails, it leaves the source event pending for replay. This
is at-least-once across crashes; Jira status checks avoid a repeated transition,
but a comment can repeat if the Jira request succeeded immediately before the
process stopped and before SQLite saved its cursor.
