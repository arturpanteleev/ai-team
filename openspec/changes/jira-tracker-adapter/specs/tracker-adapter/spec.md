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

The Jira adapter MUST find the issue key in the task's `external_links`. A key
is recognized only when an external-link field is exactly `KEY-N`, or when it is
an `http`/`https` URL whose host equals the `base_url` host (case-insensitive)
and whose path is `<base_url path>/browse/KEY-N` (a trailing `/` and a query
string are allowed). Keys MUST NOT be searched for inside arbitrary text: a
combined field such as `APP-12 https://example.atlassian.net/browse/APP-12` or
a URL with a lowercase key is not recognized. For a completed stage it MUST add
a comment containing the ai-team task URL and stage result, whatever the
outcome. When the stage has a configured target status and the attempt
succeeded (`status` is `passed`, `outcome` is `passed` and `decision` is not
`rejected`), Jira MUST transition to that status before the comment is posted.
Failed, infra-failed, skipped, rejected attempts and attempts with missing
outcome fields MUST NOT transition the issue. An unmapped stage MUST still
receive a result comment. Jira state MUST never be read back into the task
workflow.

#### Scenario: A mapped stage completes successfully

- **WHEN** a Jira-linked task completes a mapped stage with `status=passed`,
  `outcome=passed` and a decision other than `rejected`
- **THEN** Jira MUST transition the issue to the configured status when needed
- **AND** receive a result comment with the ai-team task link

#### Scenario: A mapped stage fails or is rejected

- **WHEN** a Jira-linked task finishes a mapped stage as failed, infra-failed,
  skipped or rejected, or without outcome fields
- **THEN** Jira MUST NOT receive a transition request
- **AND** receive a result comment with the ai-team task link

#### Scenario: An external link is not a recognized key format

- **WHEN** the only external link mentions a key inside other text, uses a
  lowercase key, or points to a host other than the `base_url` host
- **THEN** the adapter MUST make no Jira request and record no
  `integration_error`

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
