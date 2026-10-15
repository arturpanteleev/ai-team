# stage-status Specification

## Purpose

Вычисляемые статусы этапов задачи (S11): одна серверная проекция того, где
находится каждый этап, выведенная из durable-фактов (журнал событий, согласования,
checkpoint жизненного цикла, закреплённый шаблон) и одинаково отдаваемая в
REST, MCP, карточку задачи, маршрут и таймлайн. Проекция чистая: она не
хранится, не редактируется вручную и после перезапуска даёт тот же ответ.

## Requirements

### Requirement: Stage status is a pure projection of durable facts

The system MUST derive the status of every stage of a task from the append-only
event journal, the approvals, the lifecycle checkpoint and the pinned template,
as a pure function: the same facts MUST give the same projection after a reload
or a restart, and the projection MUST NOT change its inputs. The system MUST NOT
store a status and MUST NOT offer any API (REST, MCP or UI) that writes one; a
request that carries a status in its body MUST NOT change the projection. An event
journal written before this capability, or without transition events, MUST still
project: visits are then inferred from the attempts and at most one visit is
open.

#### Scenario: Replay after restart

- **КОГДА** the server is restarted on the same database and target directory
- **ТОГДА** the projection of every task MUST be identical to the one served
  before the restart
- **И** the projection built from the hash-chained evidence journal MUST give
  the same statuses, visit counts and visit outcomes as the one built from the
  task event feed

#### Scenario: A status cannot be set

- **КОГДА** a client sends POST, PUT, PATCH or DELETE to
  `/api/runs/{id}/stage-statuses`, or sends a status in the body of a stage action
- **ТОГДА** the request MUST be rejected
- **И** the projection MUST be unchanged

### Requirement: Readiness, phase, executor, outcome and visits are separate

The projection MUST report for every stage a single `status` (`not_reached`,
`ready`, `in_progress`, `checking`, `awaiting_human`, `passed`, `rejected`,
`failed`, `blocked`, `skipped`, `canceled`) and, separately, `readiness`
(`not_reached`, `ready`, `engaged`, `left`), `phase` (`none`, `working`,
`checking`, `awaiting_human`), `executor`, `outcome`, whether the stage is the
`current` one and whether it was ever `visited`. `checking` MUST NOT be reported
as plain work (`in_progress`) and a stage that waits for a person's decision,
answer or confirmation MUST be `awaiting_human`, not `in_progress` or `ready`. A
stage that waits for a person to take it and submit a result MUST be `ready`
with the block `human_input`, and `in_progress` once a person has taken it.

#### Scenario: Checks and gates run

- **КОГДА** the attempt of a stage finished and its checks or gates still run
- **ТОГДА** the stage MUST be `checking` with phase `checking`
- **И** a human gate that waits for a person MUST make it `awaiting_human`

#### Scenario: A decision is awaited

- **КОГДА** the result of a stage waits for confirmation, a check decision, grill
  answers or the delivery plan
- **ТОГДА** the stage MUST be `awaiting_human` with `blocked_by` naming the
  source (`approval`, `check_decision`, `grill_questions`, `delivery_plan`), the
  approval, the roles and the offered actions
- **И** the failed required gates MUST be listed for a check decision

#### Scenario: The next stage is not reached yet

- **КОГДА** the confirmation of a stage result is open
- **ТОГДА** the confirmed stage MUST be the one that is `awaiting_human` and the
  next stage MUST stay `not_reached`
- **И** the stage MUST name `action_stage_id` when the action routes address
  the next stage

### Requirement: An interrupted attempt is not work in progress

An attempt without an end MUST be `in_progress` only while a worker can execute
it. When the run is `interrupted` or `stopped` (or the evidence journal records
`attempt_abandoned`), the open stage MUST be `ready` with the block `run_state`,
its outcome MUST stay `pending` and the attempt MUST be listed unfinished with
execution `canceled`. The SQLite feed does not record `attempt_abandoned`, so
the projection MUST derive this from the run status; the projection of the feed
and the projection of the evidence journal MUST be equal. A visit that was
closed by a failed task MUST end `failed`, not `canceled`; `canceled` is only
for a canceled task. A visited stage whose visit has no attempt and was left by
a transition MUST NOT be `not_reached`: it is `passed` (moved on) or `rejected`
(left by a return).

#### Scenario: Interruption in the middle of an attempt

- **КОГДА** the journal has an attempt without an end and the run is
  `interrupted`
- **ТОГДА** the stage MUST be `ready` with `blocked_by.kind` `run_state`, also
  after a server restart that reconciled the run

### Requirement: The executor is the actual one

The projection MUST tell a person, an internal agent and an external agent apart
(`human`, `internal_agent`, `external_agent`). For a finished attempt it MUST
report the executor the attempt evidence names (the runtime of an internal
agent, the adapter of an external one, the person who did it) with source
`attempt`; before work starts it MUST report the configured executor with source
`configured`, or the person's choice with source `override` for the visit it was
made for. An executor override of another visit MUST NOT apply.

#### Scenario: External agent

- **КОГДА** a stage is configured with an executor adapter
- **ТОГДА** its executor MUST be `external_agent` named after the adapter, also
  before the stage is reached

### Requirement: The current stage and the visit history are different data

A visit is one time a stage was entered. The projection MUST list every visit
of every stage with its number, whether it is active, when it was entered and from
where, how it ended, where it went, whether it was left by a return, and its
attempts. After a transition the earlier visit MUST be inactive; a return to a
stage MUST start a new visit; attempts of earlier visits MUST NOT be dropped,
and an attempt invalidated by a return MUST stay listed as superseded. A stay on
the same stage for another attempt MUST NOT open a new visit. The status of a
stage MUST be the status of its open visit when it is current, so the first
attempt of an earlier visit MUST NOT make a returned-to stage look completed. A
stage whose latest visit was invalidated MUST be `not_reached` and `visited`. A
finished task MUST have no current stage and no active visit.

#### Scenario: Return from code review to implementation

- **КОГДА** code review rejects the result and the task is returned to
  implementation
- **ТОГДА** implementation MUST have two visits: the first inactive with its
  outcome and attempt kept, the second active and entered by a return
- **И** code review MUST be `not_reached` with one inactive, returned,
  superseded visit
- **И** the second review MUST be a new visit of code review

### Requirement: The projection is served to every client

The task view (`GET /api/runs/{id}`) MUST carry the full projection as
`stage_statuses`; each task of the list (`GET /api/runs`) MUST carry a compact
`stage_status` (the current stage and a route of every stage); and
`GET /api/runs/{id}/stage-statuses` MUST serve the same projection as the task
view. The MCP server MUST expose it as the tool `get_task_stage_statuses` and the
resource `aiteam://tasks/{run_id}/stage-statuses` with the same JSON as REST. The
current stage MUST carry the actions open on it next to its status. A task
without a valid pinned template MUST answer the dedicated route with `409`.

#### Scenario: One answer everywhere

- **КОГДА** a client reads the task view, the dedicated route, the list card and
  the MCP tool at the same moment
- **ТОГДА** the statuses MUST agree

### Requirement: The UI shows the projection with one vocabulary

The task card, the route and the timeline MUST show the same status words and
the executor kind from the projection. The card MUST show the current stage with
its status, its executor, the visit (and that it came by a return) and what
blocks it, and a route of every stage. The route MUST show every stage with its
status, executor, number of visits and whether a not-reached stage was active
before. The timeline MUST show the status of every lane and the visits of a
stage as bands under the scale, an inactive visit apart from the active one. Red
MUST mean only a real refusal, failure or bypass and amber only an advisory
outcome; a stage status MUST also be readable without color.

#### Scenario: Returned stage

- **КОГДА** a stage is not reached again after a return but was active before
- **ТОГДА** the route and the timeline MUST say it was active earlier
