# Token budgets

## ADDED Requirements

### Requirement: Task and daily token budgets

The pipeline MUST check a positive `budget.max_task_tokens` or
`budget.max_daily_tokens` before every agent attempt that invokes a model
and before every checker-agent run, including grill rounds. Checker usage MUST
be recorded durably and MUST count toward both budgets. A checker run MUST
leave a start marker before calling the model; on resume a marker without a
final record MUST count as unknown usage. Resume after a budget stop before a
checker MUST run only the pending check and MUST NOT repeat the finished stage
attempt.
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
A resume MUST NOT grant a new task budget by itself. One more allowance of the
same size, counted from the usage recorded at the latest task-budget stop, MUST
be granted only by an explicit human decision with a non-empty reason; the
grant MUST be recorded in evidence as `budget_allowance_granted` with the actor,
the reason and the baseline, and MUST cover only that one stop. The web resume
MUST record `run_resume_requested` with `budget_allowance_granted` true only for
an explicit grant, MUST reject a grant without a reason, and MUST reject a grant
when no task-budget stop is left uncovered. The daily budget MUST NOT be granted
or reset by a resume and MUST be re-checked on resume.

#### Scenario: Resume after a task budget stop without a grant

- **GIVEN** a run stopped with `budget_exceeded` scope `task`
- **WHEN** a human resumes it without granting a budget allowance
- **THEN** the next agent attempt MUST NOT run
- **AND** the run MUST pause again with reason `budget_exceeded`

#### Scenario: Resume with an explicit budget grant

- **GIVEN** a run stopped with `budget_exceeded` scope `task`
- **WHEN** a human resumes it granting a budget allowance with a reason
- **THEN** `budget_allowance_granted` MUST be recorded with the actor and the reason
- **AND** the next agent attempt MUST run
- **AND** the run MUST stop again once usage since the stop reaches the limit

#### Scenario: Grant without a reason

- **WHEN** a web resume requests a budget grant without a reason
- **THEN** the request MUST be rejected with `400` and the run MUST NOT resume

#### Scenario: Grant after a daily stop

- **GIVEN** a run stopped with `budget_exceeded` scope `daily`
- **WHEN** a human requests a budget grant
- **THEN** no allowance MUST be recorded and the daily budget MUST stay exhausted

#### Scenario: Grill checker after the budget is spent

- **GIVEN** a grill checker round consumed the rest of the task budget
- **WHEN** a human answers and the run resumes in a new process
- **THEN** the next checker round MUST NOT invoke the model

#### Scenario: Stage attempt consumed the whole budget

- **GIVEN** a stage attempt used exactly `max_task_tokens` and the run stopped before its checker
- **WHEN** a human resumes the run granting a budget allowance
- **THEN** the checker MUST run on the granted allowance
- **AND** the stage attempt MUST NOT be repeated

#### Scenario: Output changed before the pending check

- **GIVEN** a run stopped before the checker of a finished attempt
- **AND** the attempt output was edited before resume
- **WHEN** a human resumes the run
- **THEN** the checker MUST NOT run and the stage MUST NOT be repeated
- **AND** the run MUST pause with reason `pending_check_output_changed`
