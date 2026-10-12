## ADDED Requirements

### Requirement: Task tracker adapter consumes durable events

The web task service MUST deliver stored `attempt_finished` events to its
configured tracker adapter outside the task execution path. Adapter failures
MUST NOT change or block the task outcome. The adapter MUST receive the source
event's run, stage, result data, and task external links.

#### Scenario: A completed stage is published

- **WHEN** SQLite stores an `attempt_finished` event for a task
- **THEN** the configured adapter MUST receive that event and its task links
- **AND** adapter work MUST run independently of the task result

#### Scenario: No adapter is configured

- **WHEN** a task event is stored without a configured tracker adapter
- **THEN** task processing MUST continue without tracker requests

### Requirement: Jira maps stage status and comments on the task

The Jira adapter MUST find the issue key in the task's `external_links`. For a
completed stage it MUST add a comment containing the ai-team task URL and stage
result. When the stage has a configured target status, Jira MUST transition to
that status before the comment is posted. An unmapped stage MUST still receive
a result comment. Jira state MUST never be read back into the task workflow.

#### Scenario: A mapped stage completes

- **WHEN** a Jira-linked task completes a mapped stage
- **THEN** Jira MUST transition the issue to the configured status when needed
- **AND** receive a result comment with the ai-team task link

### Requirement: Tracker failures are durable and replay-safe

The tracker consumer MUST acknowledge an event only after adapter handling or
after persisting a durable warning `integration_error` with source event
identity. Its per-adapter cursor MUST begin at the current event tail on first
enable and resume after the saved cursor on later starts. An event whose
integration error could not be persisted MUST remain pending. Delivery is at
least once across process crashes: Jira may receive a duplicate comment if the
process stops after Jira accepts it but before the cursor is committed.

#### Scenario: Jira rejects a request

- **WHEN** a transition or comment request fails
- **THEN** the task retains its pipeline result
- **AND** SQLite contains a durable warning `integration_error`
- **AND** the failed event is not retried indefinitely after that warning is
  persisted

#### Scenario: The process restarts with an undelivered event

- **WHEN** an `attempt_finished` event is stored after the consumer stops and
  before it restarts
- **THEN** the restarted consumer reads it from SQLite after the saved cursor
