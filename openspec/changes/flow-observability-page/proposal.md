# Flow Observability Page

## Why

Flow metrics (`/api/metrics/flow`, B-52) exist only as an API. Product owners
need a dashboard page to review Lead/Cycle Time, the stage breakdown, agent
share and acceptance, overrides/skips/returns and token usage for a period, and
to export the same numbers as CSV (FR-24, FR-25).

## What Changes

- Add a `Наблюдаемость` navigation item and an `/observability` page.
- Add an inclusive calendar-day period picker that requests the half-open API
  window `[from, day after to)`.
- Show Lead/Cycle Time median and p80, agent acceptance without edits, a
  per-stage table (attempts, time, agent share, execution/decision/outcome
  states with time, tokens and subscription-share estimate), and a per-stage
  table of overrides, skips and returns.
- Export the displayed period through `/api/metrics/flow.csv`.

## Impact

- Frontend only; the flow metrics API contract is unchanged. Backend tests pin
  the date-only half-open period, the empty period and invalid-period errors.
