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
| `GET /api/runs?project_id=&status=&stage=&function=&mode=&limit=&offset=` | Filtered, paginated task list |
| `POST /api/runs` | Create a task; `project_id` is required |
| `GET /api/projects?include_archived=` | Project folders (archived hidden by default) |
| `GET /api/projects/{projectID}` | One project, archived ones included |
| `POST /api/projects` | Create a project (`name`, `description`); needs `project:manage` |
| `PATCH /api/projects/{projectID}` | Rename or change the description; needs `project:manage` |
| `POST /api/projects/{projectID}/archive` | Archive a project (idempotent); needs `project:manage` |
| `POST /api/projects/{projectID}/unarchive` | Return a project from the archive; needs `project:manage` |
| `GET /api/tracker` | Tracker adapter, `on_enter`/`on_pass` maps, failed-delivery and link-conflict counts |
| `GET /api/tracker/conflicts` | Ambiguous legacy links that were not guessed |
| `GET /api/tracker/deliveries?status=` | Outbound tracker deliveries (`failed`, `delivered`, `abandoned` or `superseded`) |
| `GET /api/runs/{runID}/tracker` | Task link, conflicts, recent deliveries, `sync_status` |
| `PUT /api/runs/{runID}/tracker/link` | Link the task to one existing tracker issue (`issue_key`); 1:1; needs `tracker:link` |
| `DELETE /api/runs/{runID}/tracker/link` | Remove the link and its conflicts; needs `tracker:link` |
| `POST /api/runs/{runID}/tracker/retry` | Retry every failed tracker delivery of the task; needs `tracker:link` |
| `POST /api/runs/{runID}/tracker/deliveries/{eventID}/retry` | Retry one failed delivery; needs `tracker:link` |
| `GET /api/runs/{runID}` | Task, stages, approvals, ownership, results, allowed actions |
| `POST /api/runs/{runID}/cancel` | Cancel a task |
| `POST /api/runs/{runID}/stages/{stageID}/executor` | Change stage executor |
| `POST /api/runs/{runID}/stages/{stageID}/take` | Take a human stage |
| `POST /api/runs/{runID}/stages/{stageID}/run-agent` | Run or refine with the stage agent |
| `POST /api/runs/{runID}/stages/{stageID}/results` | Submit a typed human result |
| `GET /api/runs/{runID}/stages/{stageID}/results/{version}` | Read one immutable result revision |
| `POST /api/runs/{runID}/stages/{stageID}/override` | Pass a task on after a failed required gate; system administrator only, reason required |
| `GET /api/runs/{runID}/gates` | Gates of every result version, attempts, overrides, advisory warnings, aggregated transition decision |
| `POST /api/runs/{runID}/stages/{stageID}/gates/{gate}/retry` | Run a technically failed gate again on the same result version |
| `POST /api/runs/{runID}/gate-attempts/{attemptID}/retry` | The same retry addressed by the failed attempt |
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
| `GET /api/backlog?project_id=` | Draft idea backlog and the instance auto-pickup policy |
| `POST /api/backlog` | Add an idea (`project_id`, `title`, `idea`, `feature`); always created as `draft` |
| `POST /api/backlog/{itemID}/status` | Set `draft`, `ready` or `discarded` (human transitions only) |

A project is a folder for tasks: ID, name, description and an archived flag
(no repository, template or members of its own). Every task and every backlog
item belongs to exactly one project, so `POST /api/runs` and `POST /api/backlog`
without `project_id` are `400 project_required`, an unknown project is
`404 project_not_found`, and an archived project does not accept new tasks
(`409 project_archived`); its history stays readable. Projects are never
deleted, only archived. Project names are unique ignoring case. The migration
creates the project «Основной» (ID 1) for records that predate projects; a clean
install gets the same project. Auto pickup keeps the item's project, and an item
whose project was archived after it became `ready` fails with a visible reason.
Creating, renaming and archiving projects needs the `project:manage` permission
(Product Owner or Architect).

The idea backlog is stored separately from tasks. Only an explicit human
`ready` marks an item as available for auto pickup; `starting`, `started` and
`failed` belong to the picker and cannot be set through the API. A `failed`
item returns to the queue only when a human marks it `ready` again.
`POST /api/runs/{runID}/resume` records `run_resume_requested` with the
resuming `actor_id` and `budget_allowance_granted`. A plain resume (empty body)
never grants a new task budget: after a task-budget stop the run stops again
before the next model call. A new allowance of the same size is granted only by
an explicit human decision with a reason, sent as
`{"grant_budget_allowance": true, "reason": "..."}`; then
`budget_allowance_granted` is true, the event carries `reason`, and the
pipeline records `budget_allowance_granted` in evidence. A grant without a
reason (or a reason without a grant) is `400`; a grant with no uncovered
task-budget stop, including after a daily stop, is `409`. The daily budget is
never granted or reset. `GET /api/pipelines/{id}` returns
`budget_allowance_grantable`, true while an uncovered task-budget stop exists.
A disposable worker may append `budget_allowance_granted` only in a resume job
that carries the controller grant, with the same actor and reason, once, and
with `baseline_tokens` equal to the uncovered stop. See
`auto_pickup` in the configuration reference for limits and events.

Stage actions are authorized by the functions of the run's pinned template
(B-82). `take`, `executor`, `run-agent`, `results`/`submit`, `grill/*`,
`skip` requires the stage `function` (the gate `retry` needs the same function as
`take`); `override` is not a stage function at all (see below); `confirm` and `return`
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
`next_cursor` is the last cursor examined in the requested direction.
`has_more` is `true` only when at least one more matching event (respecting the
warning filter) exists past the page, so a client can hide “load more” without
an extra request. `before` requires `order=desc`, cannot be combined with
`after`, and must be a positive cursor: `before=0` is rejected with 400, and
omitting `before` starts from the newest event. Each event carries `warning`,
the server's classification (typed warning events or `severity=warning`); the
dashboard uses it instead of its own list of warning types. Store failures
return 500 with `code: "store_unavailable"`. `from` and `to`
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
executed by an agent. Skipped attempts (`outcome` or status `skipped`) stay in
`attempts`, duration and `by_state` and are reported as `skipped_attempts`, but
count toward neither `human_attempts` nor `agent_attempts`, the agent share
denominator, usage attestation, nor agent results. The report includes overrides, skips, and returns by the
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

`cost_by_tariff` (top level and in each stage) is the provider-tariff cost,
added alongside the existing fields: attested agent-attempt tokens × the price
of the attempt's model from `usage.tariffs` in `.ai-team/config.yaml`, per one
million tokens. The tariff applied is the model's entry with the latest
`effective_date` not after the attempt finished. Cache reads and writes are
priced with the cached and cache-write prices; cache reads already counted in
input tokens (codex) are not charged twice. It covers the same attempts as
stage tokens (non-invalidated, non-skipped agent attempts of selected runs).
`amount` is `null` unless every such attempt is priced in a single currency;
`reason` then names why (`tariff_not_configured`, `model_unknown`,
`tariff_missing_for_model`, `tariff_not_effective_at_attempt`,
`cache_price_missing`, `usage_unknown`, `usage_invalid`, `mixed_currency`,
`no_agent_attempts`), with `priced_attempts`, `unpriced_attempts` and
`unpriced_models`. `tariff_dates` and `tariff_sources` list the tariff entries
used. ai-team ships no prices: without configured tariffs the cost is unknown,
never zero. The subscription estimate stays a separate field. The CSV adds
`cost_by_tariff` and `stage_cost_by_tariff` rows whose attestation is `priced`
or `unavailable:<reason>` and whose basis lists the tariff dates.

The optional `run_id` restricts the selected runs to one task (400 for an
unsafe ID, 404 for an unknown task, 500 when the store cannot be read). Without `from` and `to` it covers the
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
values read “нет данных”. When stage usage is not `complete`, the card says
“частично (N из M)” with N attested of M agent attempts. The open interval of
a running task is redrawn every 30 seconds. A table of the same segments and marks is available
for screen readers, and lanes are navigable with the arrow, Home and End keys.

The flow metrics calculation is expected to complete in 300 ms or less for
1000 recorded runs.

The `/observability` dashboard page reads these routes. Its period picker
selects inclusive calendar days and sends `from=<first day>` and
`to=<day after last day>` as `YYYY-MM-DD`, so the API window `[from, to)` covers
whole UTC days; the fields are labelled as UTC dates and the page shows the
applied period. CSV export uses the same period as the displayed metrics and is
disabled while metrics are loading, after a load error, or when the period has
no data. Text cells in the CSV that start with `=`, `+`, `-`, `@`, tab or
carriage return are prefixed with `'` so spreadsheets do not evaluate them as
formulas.

## Gate state, retry and override

`GET /api/runs/{runID}/gates` (MCP: `get_task_gates`) returns, for every stage
with a gate chain, the configured `groups` and every recorded result version
(`waves`, newest last; the newest also as `current`). A wave carries the
`artifact_sha256`, the stage `visit` and attempt, the gates, the full `attempts`
history (retried and superseded attempts included), the `override` recorded for
exactly that version and the aggregated decision `transition`:

```json
{"allowed": true, "state": "overridden",
 "reasons": ["переход разрешён администратором admin-1 несмотря на review; исход гейтов не изменён: срочный релиз"],
 "blocking": ["review"], "warnings": ["style"], "retryable": [], "overridden": ["review"]}
```

`state` is `passed`, `passed_with_warnings`, `overridden`, `blocked` (a required
`fail`), `error` (a required technical failure), `waiting` or `incomplete`.
Each gate reports its `mode`, `group`, effective `attempt` (technical `execution`
and content `verdict` are separate fields), the number of `attempts`, and the
flags `blocking`, `warning` (an advisory gate that did not pass), `retryable`
(latest attempt `error` or `timeout`) and `overridden`. Top-level `overrides`
lists every override (author, `authority: system_admin`, time, reason, version
hash, the failed attempts), `warnings` the advisory outcomes of the current
versions, and `can_override` whether the caller holds the administrator right.

**An override never changes an outcome.** A gate that returned `fail` is still
`fail` with `overridden: true`; the UI shows a red warning for it (card, route,
history) and an amber one for an advisory outcome.

`POST …/stages/{stageID}/override` (body `{"reason": "..."}`): the caller needs
the system administrator right (`admin`, permission `gate:override`) — a product
owner without it gets `403 admin_required`, whatever stage functions they hold;
an empty reason is `400 reason_required`; a stage that is not waiting on a failed
check is `409`. The override is bound to the result version that was checked:
a result with another hash needs its own override. The generic
`POST /api/runs/{runID}/approvals/{approvalID}/decisions` refuses `override` and
`retry_gate` on a check decision (`403` for a non-administrator, `409` pointing
at the stage endpoints).

`POST …/stages/{stageID}/gates/{gate}/retry` (optional body
`{"gates": ["other"], "reason": "..."}`) and `POST …/gate-attempts/{attemptID}/retry`
resolve the pending check decision with `retry_gate`: the named gates, whose
latest attempt is `error` or `timeout`, run again on the same result version
(a new attempt; the earlier one stays; gates that already passed do not run
again). The action is listed as `gate-retry` in `allowed_actions` only while the
decision offers it. A gate with a negative verdict is not retryable (`409`).
