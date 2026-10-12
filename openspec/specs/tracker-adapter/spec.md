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

The Jira adapter MUST find the issue key in the task's `external_links`. For an
`attempt_finished` event it MUST post a comment containing the ai-team task URL
and stage result. When `tracker.jira.stage_statuses` maps that stage, the
adapter MUST transition the issue to the configured destination status before
posting the comment. A stage without a status mapping still receives a result
comment. No Jira response or webhook may mutate the ai-team task.

#### Scenario: Mapped stage finishes

- **WHEN** a task linked to Jira issue `APP-12` finishes a mapped stage
- **THEN** Jira MUST transition to the configured target status when needed
- **AND** receive a comment with the ai-team task link and stage result

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

Delivery is at least once across process crashes. If Jira accepts an operation
and the process stops before the local cursor commits, that operation MAY be
sent again after restart; Jira status changes are checked against current
status, while comments can duplicate in this crash window.

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
