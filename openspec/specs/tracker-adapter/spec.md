## Purpose

Defines the abstract task tracker, the explicit one-to-one link between a task
and a tracker issue, and the outbound synchronization contract with its failure
isolation, and the inbound synchronization contract: tracker status changes
become durable requests that only the existing controller paths may act on.

## Requirements

### Requirement: Task tracker is an abstract adapter interface

The `tracker` package MUST define a `Tracker` interface with: a key format
check, reading an issue (existence and accessibility), listing allowed
transitions, an idempotent transition, a comment carrying an idempotency key,
and reading status changes after an opaque cursor. Adapters MUST be found by the
name configured in `tracker.adapter` (or the only configured adapter block)
through a registry. Built-in adapters are `jira` and `fake`. Adapter failures
MUST be mapped onto the sentinel classes not-found, forbidden, invalid key and
no-transition. Reading changes MUST report whether a change was made by
ai-team's own account, and an empty cursor MUST start from the current tail.

#### Scenario: Adapter is selected by name

- **WHEN** the configuration enables exactly one adapter block, or sets
  `tracker.adapter`
- **THEN** the registry MUST build that adapter
- **AND** an unknown adapter name or a missing adapter block MUST be a
  configuration error

#### Scenario: Adapter is absent

- **WHEN** no tracker is configured
- **THEN** task event processing MUST continue without tracker requests and the
  tracker API MUST report that no tracker is configured

### Requirement: A task links to at most one issue and an issue to at most one task

The task-to-issue link MUST be an explicit record (`tracker` and `issue_key`),
never derived from `external_links`. The database MUST enforce that a task has
at most one link and an issue at most one task. Setting a link MUST check the
key format, that the issue exists and is accessible, and that neither side is
already linked to something else; setting the same pair again MUST be
idempotent. A link MUST NOT be replaced silently: the operator removes it
explicitly first. Setting or removing a link requires the `tracker:link`
permission and records the actor.

#### Scenario: Valid link

- **WHEN** an authorized actor links a task to an existing accessible issue
- **THEN** the link MUST be stored with the actor and time

#### Scenario: Link is rejected

- **WHEN** the key has an invalid format, the issue does not exist, the issue is
  not accessible, or the tracker cannot be reached
- **THEN** no link MUST be stored and the response MUST name the reason

#### Scenario: One-to-one violation

- **WHEN** a task that already has a link is linked to another issue, or an
  issue linked to one task is linked to a second task
- **THEN** the request MUST be refused with a conflict
- **AND** the original link MUST remain until it is explicitly removed

### Requirement: Existing external links migrate safely

At the first start with a configured tracker, the server MUST move issue keys
from legacy `external_links` into explicit links once per tracker, using the
adapter's recognition of keys. A task MUST be linked only when exactly one
distinct key is recognized and no other task references that key. Every other
case MUST be stored as a visible conflict with a reason and MUST NOT create a
link. Conflicts MUST stay until the operator links or unlinks the task
explicitly. Later starts MUST NOT repeat the import.

A Jira key is recognized only when a field, after trimming surrounding
whitespace, is exactly `KEY-N`, or is an `http`/`https` URL whose host name and
effective port equal those of `base_url` and whose path is
`<base_url path>/browse/KEY-N` (trailing `/`, a query and a fragment allowed,
key case-insensitive). The adapter MUST NOT search for a key inside arbitrary
text.

#### Scenario: Unambiguous link migrates

- **WHEN** a task's external links contain exactly one recognized key that no
  other task references
- **THEN** the task MUST be linked to it

#### Scenario: Ambiguity is a conflict

- **WHEN** a task references several keys, or several tasks reference one key
- **THEN** no link MUST be created for them
- **AND** each case MUST be listed as a conflict with its reason

#### Scenario: Key inside other text is not recognized

- **WHEN** the only external link mentions a key inside other text, is a bare
  lowercase key, or points to another host or non-default port than `base_url`
- **THEN** the adapter MUST NOT recognize it

### Requirement: Stage events produce exactly the configured tracker operations

For a task with a link, the server MUST map a stage ID through `tracker.on_enter`
and `tracker.on_pass` to a tracker status or transition name (the legacy
`jira.stage_statuses` is read as `on_pass`, and `on_pass` wins). An `attempt_started` event MUST request the
`on_enter` target of its stage when one is configured and MUST NOT comment. An
`attempt_finished` event MUST request the `on_pass` target only when the attempt
passed (`status` is `passed`, `outcome` is `passed` and `decision` is not
`rejected`), and MUST always post a comment with the honest outcome. A
`check_overridden` event MUST post a comment with outcome `overridden` and MUST
NOT request a transition. A failed, rejected, skipped, warning, unknown or
missing outcome MUST NOT be reported as a pass and MUST NOT move the issue; the
comment MUST say the stage did not pass. The outcome type MUST be open so new
outcomes can be added without changing the planner. A failed operation MUST NOT
prevent the remaining operations of the same event. A task without an explicit
link MUST cause no tracker request.

#### Scenario: Stage entry and successful completion

- **WHEN** a linked task enters a mapped stage and then passes it
- **THEN** the issue MUST receive exactly the `on_enter` and `on_pass`
  transitions of that stage
- **AND** receive one comment with the task link, the stage, the outcome and the
  attempt ID

#### Scenario: Failed, skipped or overridden stage

- **WHEN** a linked task finishes a mapped stage as failed, rejected, skipped or
  with missing outcome fields, or a check is overridden
- **THEN** no transition MUST be requested
- **AND** the comment MUST state the real outcome and MUST NOT state `passed`

### Requirement: Delivery is idempotent and durable

A comment MUST carry an idempotency key derived from the run and the event ID,
and the adapter MUST NOT post a second comment with a key it has already
posted. For Jira the key is a separate text node `ai-team event: <key>`, the
attempt ID a separate node `ai-team attempt: <attempt_id>`, and the adapter MUST
page through the issue comments, newest first, until it finds the key or runs
out of comments before posting (with a finite page limit); if that check fails
or the limit is reached the comment MUST NOT be posted. A transition to the status the issue
already has MUST send nothing. The consumer MUST advance its durable
per-adapter cursor only after the outcome is persisted; on first enable the
cursor starts at the current event tail, later starts replay events after the
saved cursor, and if the cursor cannot be read or initialized the consumer MUST
NOT start and MUST NOT replay from the beginning. Delivery is at least once;
replaying or manually retrying an event MUST NOT create a duplicate comment.

#### Scenario: Replay does not duplicate

- **WHEN** an event whose comment was already posted is processed again
- **THEN** no second comment MUST be posted and the issue status MUST end the
  same

#### Scenario: Process restarts with a pending event

- **WHEN** an event is stored after the consumer stops and before it restarts
- **THEN** the restarted consumer MUST process it after the saved cursor

#### Scenario: Integration cursor is unavailable at start

- **WHEN** the tracker cursor cannot be read or written at web server start
- **THEN** the consumer MUST NOT start
- **AND** no past event MUST be sent to the tracker again

### Requirement: Tracker failures are visible, durable and retryable

A tracker failure MUST NOT change or block the local run. Every handled event
MUST leave a durable delivery record (event, task, issue, status, attempts,
last error), and a failure MUST also append a warning `integration_error` event
with the source event identity. The error text MUST NOT contain the token, the
Authorization header, request URLs or the text of a transport error; transport
failures are recorded only by class. The API MUST expose a per-task state
(link, conflicts, recent deliveries and a sync status of `ok`, `failed`,
`conflict`, `not_linked` or `not_configured`), an overview with the counts of
failed deliveries and conflicts, and lists of conflicts and deliveries. An
authorized actor MUST be able to retry one failed delivery or all failed
deliveries of a task; a retry uses the task's current link, increments the
attempt count, keeps a still-failing delivery visible, and MUST refuse a
delivery that is not failed. A delivery that failed for a link that was later
removed MUST become `abandoned` and MUST NOT be sent to a new link or counted as
a failure. A retry MUST NOT send a transition when a newer event of the
same task has already performed a transition on the same issue (a newer event
whose delivery carried only a comment does not count, so its transition is
retried); a delivery whose stale transition is skipped becomes `superseded`
with the reason stored apart from the original error (its comment is still
delivered idempotently). A retry racing with link removal MUST NOT turn an
`abandoned` delivery back into `delivered` or `failed`.

#### Scenario: Tracker request fails

- **WHEN** the tracker rejects a transition or comment
- **THEN** the task MUST retain its pipeline result
- **AND** SQLite MUST contain a failed delivery and a durable `integration_error`
  event
- **AND** the task's sync status MUST be `failed`

#### Scenario: Manual retry after recovery

- **WHEN** the tracker recovers and an authorized actor retries the failed
  delivery
- **THEN** the missing operations MUST be applied without duplicating the
  comment
- **AND** the delivery MUST become delivered and the sync status `ok`

#### Scenario: Transport error does not leak credentials

- **WHEN** a transport failure echoes the request URL or Authorization header
- **THEN** the recorded error MUST contain only the failure class

### Requirement: Inbound status changes are polled with a durable cursor

When `tracker.inbound` is configured, the server MUST poll the adapter's change
reader for the linked issues every `poll_interval` (default one minute, at least
five seconds) and MUST offer a manual `sync now` that runs the same pass under
the same lock. The cursor MUST be stored durably per adapter. The first pass on
an empty cursor MUST only record the tracker's current tail and MUST NOT replay
earlier history. The cursor MUST advance only after the outcome of every change
on the page is stored. A tracker or storage failure MUST leave the cursor
unchanged and MUST be recorded visibly (time, consecutive failures, error text
without credentials); the next pass MUST repeat the read. The API MUST expose the poll
interval, the status map, the last poll and last success time, the delay since
the last success and the last error.

#### Scenario: Temporary tracker outage

- **WHEN** reading changes fails and later succeeds, and an external status
  change happened in between
- **THEN** the change MUST be decided exactly once after recovery
- **AND** the failure MUST have been visible in the sync state until recovery
- **AND** repeating the sync MUST produce no second request

### Requirement: An inbound status is an explicit request, not a graph command

The server MUST treat an inbound status as a request defined by an explicit map.
`tracker.inbound.rules` maps an external status (case-insensitive) to a request:
`advance` (continue the current stage) or `return` with a `to_stage`, each
optionally limited to a current `stage`. A status that no rule names MUST be
recorded as ignored and MUST NOT move the graph. If several rules with
different requests match a status at the current stage, the change MUST be
stored as a conflict that names the options and MUST NOT move the graph.
Configuration MUST reject an unknown action or stage, a missing `to_stage` for
`return`, an identical status and stage named twice, and a poll interval below the
minimum. Every outcome MUST be one durable record keyed by the tracker's change
ID (`applied`, `pending`, `conflict`, `ignored`, `superseded`, `dismissed`), so
a replayed change is never decided twice.

#### Scenario: Ambiguous status

- **WHEN** a wildcard rule and a stage-specific rule with different requests
  both match the arriving status
- **THEN** a conflict MUST be stored
- **AND** the controller MUST NOT be called

### Requirement: Inbound requests act only through the controller's own paths

Inbound requests MUST act only through the controller's own paths. The only
graph action an inbound request may perform automatically is
continuing a stage that is ready (the same controller call as the `run-agent`
action, under the actor `tracker:<adapter>`). The server MUST NOT
confirm a stage, decide an approval, override or skip a gate, submit a result
or return a task on behalf of a tracker status, so required gates, human
confirmations, the configured return routes and visit limits cannot be bypassed.
When the conditions are not met (a failed gate, a pending confirmation or
input, a running stage, a return that needs the approver, a controller
refusal), the request MUST be stored as `pending` with the reason and the actions
the controller allows right now, and a return without a configured route MUST be
stored as a conflict. An operator MUST be able to dismiss an open request with a
reason, which changes nothing in the task. An open request MUST become
`superseded` when the task moves to another stage, finishes, or loses its link.
The local state MUST never be replaced by the tracker's status.

#### Scenario: Allowed change moves the task

- **WHEN** an external status mapped to `advance` arrives while the current
  stage is ready to continue
- **THEN** the controller MUST continue that stage
- **AND** the request MUST be `applied` and visible through REST and MCP

#### Scenario: Premature Done

- **WHEN** a status mapped to `advance` arrives while the stage waits for a
  failed-gate decision or a human confirmation
- **THEN** the request MUST be `pending` with the reason and the allowed actions
- **AND** no approval, override, skip or resume MUST be performed

### Requirement: Inbound trust comes from configuration and every launch is audited

The trust for an inbound launch MUST come from the `tracker.inbound`
configuration: the operator who lists a status in `rules` decides that this
status may continue a ready stage. The external author MUST NOT be identified
as an ai-team user and no stage permission (RBAC) MUST be evaluated against
that author; the author's display name MUST be stored and shown only as
untrusted information and MUST NOT be part of the actor. The actor in the
audit trail is the adapter (`tracker:<adapter>`) together with the change ID.
Before the controller is called, the server MUST record the run event
`tracker_inbound_launch` (actor, adapter, change ID, stage, issue key, status,
untrusted author) and MUST NOT launch when the event cannot be recorded. This
event MUST NOT count as stage authorship: a tracker launch neither makes a
person an author of the stage nor removes authorship from a person who worked
on it, so `forbid_self_approval` and the role check of whoever confirms the
stage stay exactly as they are. Everything that needs a person's right (a
confirmation, a gate decision, a return) MUST remain a request that the person
performs under their own identity.

A request MUST be applied only to the stage at which the status was set. A
status set before the task entered its current stage MUST be stored as
`superseded` with the reason, and a request that an earlier pass began for a
stage and did not finish (a crash after the launch) MUST be `superseded` when
the task is no longer ready at that stage instead of launching the next one.
Fields that come from the tracker (author, statuses, change ID, reason) MUST
be cleaned of control characters and limited to 256 bytes (reason: 512) when
stored. A manual `sync now` inside five seconds after a successful manual pass
MUST return that pass (marked `coalesced`, with the tracker and the cursor of
that pass and zero counters, because nothing was read) without reading the
tracker; a failed pass is never reused, and a clock that stepped back never
extends the window.

The staleness check tolerates a tracker clock that runs behind by up to five
seconds, so the tolerance is asymmetric: a change is stale only when it is
older than the stage entry by more than five seconds. A tracker clock that runs
fast can therefore let a stale change through as fresh, and the five-second
window can let a change made just before the stage entry be treated as fresh;
this is accepted because the only automatic action is continuing a ready stage.
A checkpoint written before the stage entry time was recorded falls back to its
last update time for this comparison.

#### Scenario: Launch is audited and grants no authorship

- **WHEN** an allowed status continues a ready stage
- **THEN** a `tracker_inbound_launch` event with the actor `tracker:<adapter>`
  and the change ID MUST be recorded before the controller is called
- **AND** the author's display name MUST NOT appear in the actor
- **AND** the event MUST NOT be a stage-author event

#### Scenario: Stale status

- **WHEN** the status change is older than the moment the task entered its stage
- **THEN** the request MUST be `superseded` and the controller MUST NOT be called

### Requirement: Refused requests wait for the operator and pre-link history is not replayed

The server MUST keep a request refused by the controller (an active run, a
visit limit, drift of the configuration or template) as `pending` with the
controller's reason until an operator acts through the normal stage route or
dismisses it; the server MUST NOT retry it automatically. The first pass on an
empty cursor records the tracker tail, and while no task is linked the cursor
keeps following the tail, so status changes made before a link was created are
not visible and are never replayed.

#### Scenario: Controller refuses

- **WHEN** the controller refuses to continue the stage
- **THEN** the request MUST stay `pending` with the refusal text

### Requirement: Inbound and outbound updates do not loop

A change whose origin is ai-team's own account MUST be recorded as ignored and
MUST NOT create a request. An adapter MUST fail the read, leaving the cursor
unchanged, when it cannot determine its own account, instead of guessing the
origin. An adapter MUST bound the size of one tracker query (Jira sends issue
keys in batches of at most 50 and merges them under one cursor). Changes of unknown origin MUST be treated as
external. A change for an issue that has no link MUST NOT be processed.

#### Scenario: Own transition

- **WHEN** ai-team moves the issue on stage entry and the next sync reads that
  change
- **THEN** it MUST be ignored as an echo and the controller MUST NOT be called

### Requirement: Inbound state is readable over REST and MCP

The server MUST expose the sync state (`GET /api/tracker/sync`, also inside
`GET /api/tracker`), the list of inbound requests filtered by state with `open`
meaning pending and conflict (`GET /api/tracker/inbound`), and the inbound
requests of one task inside `GET /api/runs/{runID}/tracker` with the task's
sync status `inbound_pending` while a request is open and no delivery has
failed. Running `sync now` and dismissing a request require the `tracker:link`
permission. The MCP server MUST provide read tools for tracker status, sync
state, inbound requests, link conflicts, deliveries and a task's tracker state,
returning the same data as REST.

#### Scenario: Operator sees what waits

- **WHEN** a request is pending or in conflict
- **THEN** the task view, the inbound list and the MCP tools MUST show it with
  its reason and the actions allowed now
