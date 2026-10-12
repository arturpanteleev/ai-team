# Auto task pickup

## ADDED Requirements

### Requirement: Draft idea backlog

The control plane MUST store backlog ideas separately from tasks. A new
backlog item MUST be created as `draft`, and a `draft` item MUST NOT start a
task automatically. Only a human MUST be able to set `draft`, `ready` or
`discarded`; picker-owned statuses (`starting`, `started`, `failed`) MUST NOT
be settable through the API.

#### Scenario: Idea stays a draft

- **GIVEN** a human adds an idea to the backlog
- **WHEN** the picker runs
- **THEN** the item MUST remain `draft`
- **AND** no task MUST be created for it

### Requirement: Explicit pickup marker

The picker MUST select only items whose status is exactly `ready`, in the
order they were marked ready, one item per poll, and MUST start the task in
`auto` mode through the normal task admission path. Any other or unknown
status value MUST NOT be picked. A claim MUST be atomic, and the claimed item
MUST be bound to its run before the run becomes visible.

#### Scenario: Unknown marker

- **GIVEN** a backlog item whose status is `READY`, `auto` or any other value than `ready`
- **WHEN** the picker runs
- **THEN** the item MUST NOT be claimed or started

#### Scenario: Interrupted claim

- **GIVEN** the process stopped after claiming an item
- **WHEN** the web server restarts
- **THEN** an item bound to a visible run MUST become `started`
- **AND** an item without an admitted run MUST become `failed` and MUST NOT be retried automatically

### Requirement: Parallel task limit

The picker MUST NOT start a task while the number of non-terminal tasks on the
instance is at or above `auto_pickup.max_parallel_tasks` (default 1). Tasks
waiting for a human and unreadable lifecycle checkpoints MUST count as active.
A limited item MUST stay `ready`, and the picker MUST record one
`auto_pickup_deferred` event per item, reason and UTC day.

#### Scenario: At the limit

- **GIVEN** `max_parallel_tasks` is 1 and one task is waiting for a human
- **WHEN** the picker runs
- **THEN** the ready item MUST stay queued
- **AND** an `auto_pickup_deferred` event with reason `parallel_limit` MUST be recorded

### Requirement: Pickup requires token budgets

Enabling auto pickup MUST require positive `budget.max_task_tokens` and
`budget.max_daily_tokens`. The picker MUST NOT start a task when the daily
token budget is exhausted or the daily usage is unknown.

#### Scenario: Daily budget spent

- **GIVEN** the instance used its daily token budget in the current UTC day
- **WHEN** the picker runs, including after a server restart
- **THEN** the ready item MUST stay queued with reason `daily_budget`
- **AND** the item MUST become eligible again after 00:00 UTC
