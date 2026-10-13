## Purpose

Defines one-way task tracker adapters and their failure isolation contract.

## Requirements

### Requirement: Task tracker adapter consumes durable events

The web task service MUST deliver completed `attempt_finished` events to its
configured tracker adapter outside the pipeline execution path. Adapter
failures MUST NOT change or block a task outcome. The task event remains the
source input; the adapter MUST NOT write inbound tracker state into ai-team.

#### Scenario: Completed stage is published

- **WHEN** SQLite stores an `attempt_finished` event for a task
- **THEN** the configured adapter MUST receive its run, stage, result data, and
  task external links
- **AND** adapter work MUST run independently of the task execution result

#### Scenario: Adapter is absent

- **WHEN** no tracker adapter is configured
- **THEN** task event processing MUST continue without tracker requests

### Requirement: Jira maps configured stages and comments on the task

The Jira adapter MUST find the issue key in the task's `external_links`. A key
is recognized only when an external-link field, after trimming surrounding
whitespace, is exactly `KEY-N`, or when it is
an `http`/`https` URL whose host name equals the `base_url` host name
(case-insensitive), whose port equals the `base_url` port (a scheme's default
port is equal to an omitted one) and whose path is `<base_url path>/browse/KEY-N`;
trailing `/` characters, a query string and a `#` fragment are allowed. The key
in such a URL is matched case-insensitively and normalized to upper case. The
adapter MUST NOT search for a key inside arbitrary text: a combined field such
as `APP-12 https://example.atlassian.net/browse/APP-12` or a bare lowercase
field such as `app-12` is not recognized. For an `attempt_finished`
event it MUST post a comment containing the ai-team task URL and stage result,
whatever the outcome. When `tracker.jira.stage_statuses` maps that stage and
the attempt succeeded (`status` is `passed`, `outcome` is `passed` and
`decision` is not `rejected`), the adapter MUST transition the issue to the
configured destination status before posting the comment. Any other outcome
(failed, infra-failed, skipped, rejected or missing fields) MUST NOT transition
the issue. A stage without a status mapping still receives a result comment.
No Jira response or webhook may mutate the ai-team task.

#### Scenario: Mapped stage finishes successfully

- **WHEN** a task linked to Jira issue `APP-12` finishes a mapped stage with
  `status=passed`, `outcome=passed` and a decision other than `rejected`
- **THEN** Jira MUST transition to the configured target status when needed
- **AND** receive a comment with the ai-team task link and stage result

#### Scenario: Mapped stage fails or is rejected

- **WHEN** a task linked to Jira issue `APP-12` finishes a mapped stage as
  failed, infra-failed, skipped or rejected, or without outcome fields
- **THEN** the adapter MUST NOT request a Jira transition
- **AND** Jira MUST receive a comment with the ai-team task link and stage
  result

#### Scenario: External link is not a recognized key format

- **WHEN** the only external link mentions a key inside other text, is a bare
  lowercase key, or points to a host or non-default port other than the
  `base_url` one
- **THEN** the adapter MUST make no Jira request and record no
  `integration_error`

#### Scenario: Task has no Jira key

- **WHEN** a completed stage belongs to a task without a Jira issue key in its
  external links
- **THEN** the adapter MUST make no Jira request

### Requirement: Jira failures become durable warnings

An adapter or Jira HTTP failure MUST append a durable SQLite `integration_error`
event with warning severity and source event identity. The tracker consumer MUST
advance its durable cursor only after handling the event or persisting its
`integration_error`. A later cursor failure MUST leave the source event pending
for replay. On first enable the cursor MUST start at the current event tail;
later process starts MUST replay events after the saved cursor.

If the integration cursor cannot be read or initialized at start, the
consumer MUST NOT start in that process; it MUST NOT fall back to replaying
from the beginning of the event store.

Delivery is at least once across process crashes. If Jira accepts an operation
and the process stops before the local cursor commits, that operation MAY be
sent again after restart. Jira status changes are checked against current
status. A comment for an event with an attempt ID MUST carry the text node
`ai-team attempt: <attempt_id>`, and the adapter MUST check the newest issue
comments for that marker before posting and MUST NOT post a second comment for
the same attempt. If that check fails, the comment MUST NOT be posted and the
failure is recorded as an `integration_error`.

An `integration_error` MUST NOT contain the Jira token, the Authorization
header, request URLs or the text of a transport error; transport failures are
recorded only by class.

#### Scenario: Jira request fails

- **WHEN** Jira rejects a transition or comment request
- **THEN** the task MUST retain its pipeline result
- **AND** SQLite MUST contain a durable `integration_error` event
- **AND** the failed source event MUST not be retried indefinitely in the same
  process after its warning has been persisted

#### Scenario: Process restarts with a pending event

- **WHEN** an `attempt_finished` event is stored after the tracker consumer
  stops and before it restarts
- **THEN** the restarted consumer MUST read it from SQLite after the saved
  integration cursor

#### Scenario: Replay after a crash does not duplicate the comment

- **WHEN** Jira already has a comment with the marker of attempt `A`
- **AND** the `attempt_finished` event of attempt `A` is replayed
- **THEN** the adapter MUST NOT post another comment

#### Scenario: Integration cursor is unavailable at start

- **WHEN** the tracker cursor cannot be read or written at web server start
- **THEN** the tracker consumer MUST NOT start
- **AND** no past event MUST be sent to Jira again
