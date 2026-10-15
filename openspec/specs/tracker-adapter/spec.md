## Purpose

Defines the abstract task tracker, the explicit one-to-one link between a task
and a tracker issue, and the outbound synchronization contract with its failure
isolation. Inbound synchronization is out of scope here and builds on the
tracker's change-reading method.

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
check the newest 50 issue comments for the key before posting; if that check
fails the comment MUST NOT be posted. A transition to the status the issue
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
delivery that is not failed.

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
