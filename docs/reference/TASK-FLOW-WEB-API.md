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
| `GET /api/runs/{runID}/events?after=&limit=&warnings=&order=&before=` | Run event feed |
| `GET /api/events?after=&limit=&warnings=&run_id=&order=&before=` | Global event feed |
| `GET /api/metrics/flow?from=&to=` | Flow metrics JSON |
| `GET /api/metrics/flow.csv?from=&to=` | Flow metrics CSV |

Event endpoints share the persisted SQLite event-ID cursor. By default
(`order=asc`) they return events with a cursor greater than `after`, oldest
first; this is the replay contract for resuming a stream. With `order=desc`
they return events newest first: without `before` the page starts at the newest
persisted event, and `before=<next_cursor>` returns only older events.
`next_cursor` is the last cursor examined in the requested direction. `before`
requires `order=desc` and cannot be combined with `after`. `from` and `to`
accept RFC3339 timestamps or `YYYY-MM-DD`; intervals are half-open `[from,to)`
and default to the last 30 days. Pagination limits range from 1 to 100 for run
lists and 1 to 1000 for event feeds.

The `/events` page shows the newest events first (`order=desc`), puts new
events from the WebSocket stream at the top, and offers a server-side
warnings-only filter. Each event links by stable `run_id`; the UI resolves that
ID to the existing task detail page. “Показать ещё” loads older events with
`before=<next_cursor>`; the cursor also moves past non-warning events when the
warning filter is selected.

Write callers can send `Idempotency-Key` (or legacy `X-Request-Key`) to replay
an exact prior response. Keys are scoped to the authenticated actor; reusing a
key with a different method, path, or body returns JSON 409. A retry in flight
also returns JSON 409. Missing action-controller implementations fail closed
with JSON 503 while their engine work is being integrated.

Task API errors use `{ "error": "...", "code": "..." }`. A lifecycle
conflict returns 409 with `code: "invalid_state"` and `allowed_actions`.
Flow metrics report lead and cycle time through acceptance completion as a
median and nearest-rank p80. Each stage includes duration and attempt counts by
execution, decision, and outcome, plus its share of non-invalidated attempts
executed by an agent. The report includes overrides, skips, and returns by the
stage that issued the action, and successful agent results without linked human
edits. The JSON and CSV exports contain the same metrics.

Run token totals use complete attested usage envelopes; if selected usage is
incomplete, token values are `null` with known/unknown counts, attestation, and
source retained. Per-stage token totals use attested attempt manifests and are
`null` when any agent attempt for that stage is unknown. When
`usage.monthly_subscription_amount` is configured in `.ai-team/config.yaml`,
the API estimates a proportional share of that configured monthly subscription
for selected runs and for each stage. It uses the configured currency, or USD
when omitted; no provider or per-token price is assumed. The run formula is
monthly subscription amount × selected-run attested tokens ÷ all complete
project-run tokens in the same UTC month. The stage formula substitutes
attested stage-manifest tokens from the selected period in the numerator; for
periods spanning months, the per-month allocations are summed. The estimate is
`null` if selected usage, stage usage, or any run in a required monthly
denominator is unknown or incomplete, or if the denominator is zero.
These are subscription estimates, not runtime `cost_usd`. JSON and CSV preserve
attestation, configuration/envelope/manifest source, currency, and formula
basis.

The flow metrics calculation is expected to complete in 300 ms or less for
1000 recorded runs.
