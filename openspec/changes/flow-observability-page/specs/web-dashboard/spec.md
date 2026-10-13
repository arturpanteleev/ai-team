## ADDED Requirements

### Requirement: Flow observability page

The web UI MUST provide an `/observability` page that shows flow metrics for a
user-selected period and exports the same period as CSV.

#### Scenario: Review metrics for a period

- **WHEN** a user selects first and last calendar days and applies the period
- **THEN** the UI MUST request `GET /api/metrics/flow` with `from` set to the
  first day and `to` set to the day after the last day
- **AND** MUST show Lead Time and Cycle Time median and p80, the share of agent
  results accepted without edits, per-stage attempts, time, agent share,
  execution/decision/outcome states, tokens, cost by provider tariff and the
  separately labeled subscription-share estimate, and per-stage overrides,
  skips and returns

#### Scenario: Cost by provider tariff

- **WHEN** the metrics carry `cost_by_tariff` with an amount
- **THEN** the UI MUST show the amount labeled as cost by tariff together with
  the tariff dates used
- **WHEN** the amount is null because no tariff is configured, the model is
  unknown or another attempt is unpriced
- **THEN** the UI MUST show «нет данных» with the reason and MUST NOT show zero

#### Scenario: Reject an inverted period

- **WHEN** the last day precedes the first day
- **THEN** the UI MUST show an error and MUST NOT request metrics

#### Scenario: Unknown values are not zero

- **WHEN** the selected period has no completed tasks, no stage attempts or
  unknown usage
- **THEN** the UI MUST show the affected values as unavailable and MUST NOT
  present them as zero

#### Scenario: Load failure

- **WHEN** the metrics request fails
- **THEN** the UI MUST show the error and MUST NOT keep displaying metrics of a
  previously loaded period

#### Scenario: Export CSV

- **WHEN** the user exports CSV
- **THEN** the UI MUST download `GET /api/metrics/flow.csv` for the currently
  displayed period
- **AND** on failure MUST show the error without downloading a file
- **AND** export MUST be unavailable while metrics load, after a load failure,
  or when the displayed period has no data
- **AND** text cells starting with `=`, `+`, `-`, `@`, tab or carriage return
  MUST be prefixed with `'` so spreadsheets do not evaluate them
