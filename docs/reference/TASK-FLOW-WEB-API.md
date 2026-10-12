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
| `POST /api/runs/{runID}/stages/{stageID}/grill/new-version` | Re-check a pinned human result revision |
| `POST /api/runs/{runID}/stages/{stageID}/grill/done` | Finish the grill without more help |
| `POST /api/runs/{runID}/plan/approve` | Approve a pinned delivery plan hash |
| `GET /api/runs/{runID}/events?after=&limit=&warnings=&order=&before=` | Run event feed |
| `GET /api/events?after=&limit=&warnings=&run_id=&order=&before=` | Global event feed |
| `GET /api/metrics/flow?from=&to=&run_id=` | Flow metrics JSON |
| `GET /api/metrics/flow.csv?from=&to=&run_id=` | Flow metrics CSV |
| `GET /api/backlog` | Draft idea backlog and the instance auto-pickup policy |
| `POST /api/backlog` | Add an idea (`title`, `idea`, `feature`); always created as `draft` |
| `POST /api/backlog/{itemID}/status` | Set `draft`, `ready` or `discarded` (human transitions only) |

The idea backlog is stored separately from tasks. Only an explicit human
`ready` marks an item as available for auto pickup; `starting`, `started` and
`failed` belong to the picker and cannot be set through the API. A `failed`
item returns to the queue only when a human marks it `ready` again. See
`auto_pickup` in the configuration reference for limits and events.

Stage actions are authorized by the functions of the run's pinned template
(B-82). `take`, `executor`, `run-agent`, `results`/`submit`, `grill/*`,
`override` and `skip` require the stage `function`; `confirm` and `return`
require `approver_function` when the stage declares one, otherwise `function`.
A member without the required function receives `403 forbidden` before the
controller is called. When the template sets `forbid_self_approval: true`,
`confirm` and `POST /api/runs/{runID}/approvals/{approvalID}/decisions` reject
a stage confirmation by any author of the confirmed visit (took the stage,
submitted or edited its result or agent output, made grill or override
decisions, launched the agent via `run-agent`, which records a
`stage_agent_requested` event, or changed the executor) with `403` (code `self_approval_forbidden` on the stage
route). Unverifiable authorship is refused; `return` is not subject to the
policy.

To edit a human stage result during a pending grill, post a new immutable
revision to `/api/runs/{runID}/artifact-revisions` with the stage result path,
the latest `base_revision` and `base_sha256`, and changed result content. The
route validates the content against the run's SHA-256-verified pinned stage
result type and required sections before appending. Comment-only submissions,
unchanged bytes, and edits that remove a required section are rejected. It
accepts stage result edits only while a matching grill question is pending;
stale revisions and edits after the grill resolves are rejected. Then call
`grill/new-version` to pin the edited revision. That action also verifies that
at least one selected result's content SHA-256 differs from the grill's pinned
baseline before starting the next check round.

The browser opens a task at `/tasks/{runID}`. Run detail also returns the task
text, mode, external links, and the immutable template description pinned when
the run was created. The page uses that pinned stage graph for historical
tasks and renders each stage action only when its name is present in
`allowed_actions`. It keeps result revisions and resolved grill decisions in
the task history and displays checker evidence, event details, and attempt log
links alongside the run.

When `allowed_actions` contains `results`, the page submits the stage's
configured result type through `POST .../results`: a Markdown editor with
preview and `.md` file upload (UTF-8, up to 10 MiB), a link form that uses the
stage's pinned `link_kind` and accepts only HTTP(S) URLs without credentials,
or an approve with optional text sent as `note`. An empty “Что сделано”
(`description`) shows a warning but does not block submission; the server then
records `description_missing`. The unsent form is kept as a draft in the
browser's `localStorage` per run and stage, restored (and the form reopened)
after a page reload, kept when the server rejects the submission, and removed
once the server accepts it. Without usable storage the form still works and
says the draft is not saved.

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

The optional `run_id` restricts the selected runs to one task (400 for an
unsafe ID, 404 for an unknown task). Without `from` and `to` it covers the
whole task; monthly subscription denominators still use all project runs. The
executor kind of an attempt (`human_attempts`, `agent_attempts`, stage tokens)
comes from its `attempt_finished` event, because `stages.execution` holds the
workflow execution state.

The task page shows a timeline (FR-22) with one lane per pinned stage. Lane
segments are derived from the task event feed only: `attempt_started` opens
work for an agent or waiting for executor for a human; `approval_requested`
with `human_input` is waiting for executor, with `grill_questions` or
`stage_check` is a check, and any other trigger is awaiting confirmation;
`stage_taken` and a decided `human_input` are work; `attempt_finished` opens a
check (stage checks and gates run after the attempt) unless the outcome failed;
`transition_selected`, `stage_skipped`, `run_finished` and `run_canceled` close
the interval. Returns (`return_to_*` transitions and return events), check
overrides and skips are marked on the issuing stage. A stage without events has
no bar. Selecting a lane opens the stage card (FR-23), whose tokens and
subscription-share estimate come from `GET /api/metrics/flow?run_id=`; unknown
values read “нет данных”. A table of the same segments and marks is available
for screen readers, and lanes are navigable with the arrow, Home and End keys.

The flow metrics calculation is expected to complete in 300 ms or less for
1000 recorded runs.

The `/observability` dashboard page reads these routes. Its period picker
selects inclusive calendar days and sends `from=<first day>` and
`to=<day after last day>` as `YYYY-MM-DD`, so the API window `[from, to)` covers
whole UTC days. CSV export uses the same period as the displayed metrics.
