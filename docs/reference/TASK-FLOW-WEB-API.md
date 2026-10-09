# Task flow Web API

The task API keeps `/api/pipelines` for existing dashboard clients and adds
stable run resources under `/api/runs`. Read routes use the project's
authenticated session when auth is configured. Writes require the existing
session cookie, CSRF token, and permission checks; actor IDs and roles come
from the validated session and pinned stage, never from request-body identity
fields.

| Method and path | Purpose |
|---|---|
| `GET /api/templates` | Current project template and immutable versions |
| `GET /api/runs?status=&stage=&function=&mode=&limit=&offset=` | Filtered, paginated task list |
| `POST /api/runs` | Create a task |
| `GET /api/runs/{runID}` | Task, stages, approvals, ownership, results, allowed actions |
| `POST /api/runs/{runID}/cancel` | Cancel a task |
| `POST /api/runs/{runID}/stages/{stageID}/executor` | Change stage executor |
| `POST /api/runs/{runID}/stages/{stageID}/take` | Take a human stage |
| `POST /api/runs/{runID}/stages/{stageID}/run-agent` | Run or refine with the stage agent |
| `POST /api/runs/{runID}/stages/{stageID}/results` | Submit a typed human result |
| `GET /api/runs/{runID}/stages/{stageID}/results/{version}` | Read one immutable result revision |
| `POST /api/runs/{runID}/stages/{stageID}/override` | Override a failed check with a reason |
| `POST /api/runs/{runID}/stages/{stageID}/confirm` | Confirm a checked stage |
| `POST /api/runs/{runID}/stages/{stageID}/skip` | Skip a skippable stage with a reason |
| `POST /api/runs/{runID}/stages/{stageID}/return` | Return to a configured earlier stage |
| `POST /api/runs/{runID}/stages/{stageID}/grill/answer` | Answer a grill question |
| `POST /api/runs/{runID}/stages/{stageID}/grill/done` | Finish the grill without more help |
| `POST /api/runs/{runID}/plan/approve` | Approve a pinned delivery plan hash |
| `GET /api/runs/{runID}/events?after=&limit=&warnings=` | Run event feed |
| `GET /api/events?after=&limit=&warnings=&run_id=` | Global event feed |
| `GET /api/metrics/flow?from=&to=` | Flow metrics JSON |
| `GET /api/metrics/flow.csv?from=&to=` | Flow metrics CSV |

Event endpoints share the persisted SQLite event-ID cursor. `from` and `to`
accept RFC3339 timestamps or `YYYY-MM-DD`; intervals are half-open `[from,to)`
and default to the last 30 days. Pagination limits range from 1 to 100 for run
lists and 1 to 1000 for event feeds.

Write callers can send `Idempotency-Key` (or legacy `X-Request-Key`) to replay
an exact prior response. Keys are scoped to the authenticated actor; reusing a
key with a different method, path, or body returns JSON 409. A retry in flight
also returns JSON 409. Missing action-controller implementations fail closed
with JSON 503 while their engine work is being integrated.

Task API errors use `{ "error": "...", "code": "..." }`. A lifecycle
conflict returns 409 with `code: "invalid_state"` and `allowed_actions`.
Flow metrics include token totals only when all selected runs have complete
usage envelopes; otherwise token values are `null`, with known/unknown counts,
attestation state, and source retained in both JSON and CSV. Per-stage token
totals use attested attempt manifests and are `null` when any agent attempt for
that stage is unknown. When `usage.monthly_subscription_amount` is configured,
the API estimates the selected runs' share of that monthly subscription in
proportion to their attested tokens. It does not apply per-model token prices;
the estimate is unavailable when usage in the same monthly denominator is
unknown. JSON includes the envelope/configuration source and estimate basis;
CSV rows include attestation, source, and basis columns for usage values.
