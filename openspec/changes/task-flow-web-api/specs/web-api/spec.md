## ADDED Requirements

### Requirement: Task-oriented run API

The Web API MUST expose stable task resources through `GET /api/templates`,
`GET /api/runs`, `POST /api/runs`, `GET /api/runs/{runID}`, and
`POST /api/runs/{runID}/cancel`. The run list MUST support status, stage,
function, and mode filters plus bounded pagination. Run detail MUST include
stage attempts, pending and resolved approvals, owners, immutable result
revisions, and the allowed actions for the current stage.

#### Scenario: List and inspect a task

- **WHEN** an authenticated client lists runs and opens one by run ID
- **THEN** the list MUST return stable run IDs and pagination metadata
- **AND** detail MUST resolve the run's pinned template and current lifecycle
  state rather than the active project template

#### Scenario: Unknown run

- **WHEN** a client requests an unknown run ID
- **THEN** the API MUST return a JSON 404 response with `error` and `code`

### Requirement: Task stage and plan action routes

The Web API MUST expose the §10 stage executor, take, run-agent, results,
result-version, override, confirm, skip, return, grill answer/done, and delivery
plan approval routes. Actor identity MUST come from the authenticated server
session. An action that is invalid for current lifecycle state MUST return JSON
409 with an `allowed_actions` array. Writes MUST support exact request-key
replay and reject key reuse with a different request fingerprint.

#### Scenario: State conflict

- **WHEN** a client attempts a stage action that is not allowed by current
  lifecycle state
- **THEN** the API MUST return 409 with `code: invalid_state`
- **AND** MUST include the actions currently allowed for that stage

#### Scenario: Repeated write request

- **WHEN** an authenticated client retries an action with the same request key
  and exact request bytes
- **THEN** the API MUST replay the original response without repeating the
  controller action

### Requirement: Task event feeds

The Web API MUST provide `GET /api/runs/{runID}/events` and `GET /api/events`
with a shared SQLite event-ID cursor, bounded pages, and optional warning-only
filtering.

#### Scenario: Resume an event feed

- **WHEN** a client requests events after a cursor
- **THEN** the response MUST return the next events in cursor order and the
  cursor at which to continue

### Requirement: Flow metrics API

The Web API MUST provide `GET /api/metrics/flow` and
`GET /api/metrics/flow.csv` for a half-open time interval. JSON metrics MUST
include lead/cycle durations, stage durations and execution counts, override,
skip, and return counts, and token usage completeness. Missing or incomplete
usage MUST be reported as unknown rather than zero.

#### Scenario: Unknown token usage

- **WHEN** a run has no complete attested usage envelope
- **THEN** the flow report MUST increment its unknown usage count
- **AND** MUST NOT include that run in known token totals

### Requirement: JSON API errors

Errors from task API routes MUST use a JSON object with `error` and `code`.

#### Scenario: Invalid request

- **WHEN** a task API request is malformed, unauthorized, or targets missing
  state
- **THEN** the response MUST preserve the appropriate HTTP status and return
  JSON `error` and `code` fields
