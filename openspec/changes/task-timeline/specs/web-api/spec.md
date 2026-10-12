## ADDED Requirements

### Requirement: Flow metrics for one task

`GET /api/metrics/flow` and `GET /api/metrics/flow.csv` MUST accept an optional
`run_id` that restricts the selected runs to that task while keeping monthly
subscription denominators over all project runs. Without `from` and `to`, the
selection MUST cover the whole task; with them, the period MUST also apply.
The executor kind of an attempt MUST be taken from its recorded
`attempt_finished` event when present.

#### Scenario: Select one task

- **WHEN** a client requests flow metrics with `run_id` of an existing task
- **THEN** stage, state, usage, override, skip and return values MUST include only that task

#### Scenario: Unknown or invalid task

- **WHEN** `run_id` is not a safe identity or does not exist
- **THEN** the API MUST return a JSON error with 400 or 404 respectively
