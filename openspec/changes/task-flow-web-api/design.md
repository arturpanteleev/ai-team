# Design: task-oriented Web API

The existing `/api/pipelines` API remains unchanged. New task read endpoints
are grouped under `/api/runs` and resolve lifecycle and template data by the
run ID. Template and result reads use the current template store and immutable
per-run pins/revisions. Events continue the SQLite global cursor so clients can
resume a run feed and the global feed from the same cursor.

Writes remain behind the current session/CSRF and permission middleware. The
idempotency layer scopes keys to the actor from the validated server-side
session and fingerprints method, path, and exact body bytes. It stores the
response in SQLite and rejects a key reused with different request bytes.

Controller-owned stage actions are optional typed interfaces on the existing
run controller. This keeps routes and validation in the Web layer while the
pipeline/controller implementation owns transitions, durable evidence,
approval resolution, and dispatch. A missing action interface is fail-closed
with JSON 503 while the corresponding engine branch is in flight; this is an
integration seam, not an accepted final behavior.

Flow metrics use projected stages/events and the controller's usage envelope.
Unknown or incomplete token usage increments `unknown_runs` and is not added as
zero-valued known usage. Date ranges are half-open `[from,to)` and default to
the most recent 30 days.
