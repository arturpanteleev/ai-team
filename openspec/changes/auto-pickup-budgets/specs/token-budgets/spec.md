# Token budgets

## ADDED Requirements

### Requirement: Task and daily token budgets

The pipeline MUST check a positive `budget.max_task_tokens` or
`budget.max_daily_tokens` before every agent attempt that invokes a model.
Delivery stages MUST NOT be stopped by the token budget. A budget MUST count
as exhausted when usage is equal to or greater than the limit. Usage MUST be
recomputed from durable attempt manifests, not process memory; an attempt
MUST count toward the UTC day in which it finished. Unknown usage with a
configured limit MUST count as exhausted.

#### Scenario: Exactly at the task budget

- **GIVEN** a task has used exactly `max_task_tokens`
- **WHEN** the next agent stage is about to start
- **THEN** the agent MUST NOT start

#### Scenario: Restart keeps the daily counter

- **GIVEN** the instance spent its daily budget and the process restarted
- **WHEN** an agent stage is about to start in the same UTC day
- **THEN** the agent MUST NOT start

#### Scenario: Remote evidence without a ledger

- **GIVEN** a worker reads evidence through the controller API and no daily ledger is supplied
- **WHEN** a daily budget is configured
- **THEN** the daily usage MUST be treated as unknown and the task MUST stop

### Requirement: Budget stop requires a human decision

Exceeding a token budget MUST append a `budget_exceeded` event with scope,
used tokens and limit, and MUST pause the run as resumable with status
`stopped` and `run_paused` reason `budget_exceeded`. The run MUST NOT continue without an explicit human resume.
A resume after a task-budget stop MUST grant exactly one more allowance of the
same size counted from the usage recorded at that stop. The daily budget MUST
be re-checked on resume.

#### Scenario: Resume after a task budget stop

- **GIVEN** a run stopped with `budget_exceeded` scope `task`
- **WHEN** a human resumes it
- **THEN** the next agent attempt MUST run
- **AND** the run MUST stop again once usage since the stop reaches the limit
