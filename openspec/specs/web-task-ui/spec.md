# web-task-ui Specification

## Purpose

Контракт web UI задачи после аудита S16: страница задачи показывает за один
экран, где задача, кто действует, что проверяется и какое следующее
разрешённое действие; боковые колонки имеют неизменное назначение; у каждого
действия одно имя; права в интерфейсе совпадают с серверными; цвет несёт
смысл; трекер, роли и токены доступны без скрытых меню. Статусы этапов
берутся из проекции `stage-status`, права — из `cloud-identity`, трекер — из
`tracker-adapter`.

## Requirements

### Requirement: Task page has three zones with a fixed purpose

The task page MUST consist of a header, a stage route and three zones whose
purpose never changes: the left column «Статус и контекст» (where the task is,
who acts, what is awaited, what is checked, the tracker state, the delivery
state), the main area (materials) and the right column «Действия» (only what
the viewer may do right now). The DOM order MUST be the visual order on every
width: status, actions, materials.

#### Scenario: One screen answers the four questions

- **КОГДА** a task is in any state of the stage-status projection (not reached,
  ready, in progress by a human, in progress by an internal or an external agent,
  checking, awaiting a human, passed, rejected or blocked, skipped, interrupted,
  a second visit after a return, an administrator override, an advisory warning,
  a technical gate error, a tracker conflict)
- **ТОГДА** the left column MUST name the stage the projection says the task is at,
  its status chip, who acts (kind and name of the executor, the owner when taken),
  what it waits for and the required roles
- **И** the right column MUST list the actions allowed to the viewer, or say plainly
  that there are none and why (waiting for another role, the viewer's roles)

#### Scenario: Status column has no controls

- **КОГДА** the page renders
- **ТОГДА** the left column MUST NOT contain buttons or input fields
- **И** the right column MUST NOT contain tabs, logs, artifact or report content,
  the delivery plan, the grill questions or the decision history

#### Scenario: A stopped run names where it stopped

- **КОГДА** a run has no current stage (failed or stopped)
- **ТОГДА** the page MUST name the last visited stage as «Остановилась на этапе»
- **И** MUST show the short reason of the failure without opening anything

### Requirement: The actions column is stable and bounded

The right column MUST be sticky on wide screens, MUST have a maximum height of the
viewport and MUST scroll inside itself. Bulky content (logs, artifacts, reports,
the plan, grill questions, history, tracker requests) MUST open in the main area.

#### Scenario: A huge log does not lengthen the actions column

- **КОГДА** the journal has more than a thousand events and the task has hundreds of artifacts
- **ТОГДА** the height and content of the actions column MUST be the same as for a short journal
- **И** only the main area MUST grow

#### Scenario: Questions are read in the main area and answered in the column

- **КОГДА** a grill, a question or a delivery plan is pending
- **ТОГДА** the question text or the plan content MUST be shown in the main area
- **И** the answer field and the buttons MUST be in the actions column

### Requirement: One action, one name

Every action MUST have one name used by the button, the «Доступно» line of the route,
the audit trail and the MCP documentation; the vocabulary MUST live in one module.
The actions column MUST NOT show two buttons with the same name or two buttons for one
meaning (accepting the pending approval is one button, whether it comes from the confirm
action or from the decision); a return MUST name where the stage goes back to; the main
yellow button MUST be at most one and MUST NOT be a refusal, a return or a bypass.

#### Scenario: Accepting is one button

- **КОГДА** the same pending approval can be accepted both by the stage confirmation and
  by the approve decision
- **ТОГДА** the actions column MUST show exactly one «Принять и перейти» (or «Принять»
  when the decision does not leave the stage)

#### Scenario: Exceptional actions are visible

- **КОГДА** skip, return or a bypass are allowed
- **ТОГДА** their forms MUST be visible in the group «Исключения» of the actions column
  and MUST NOT be hidden behind a menu
- **И** every one of them MUST require a reason

### Requirement: Interface permissions mirror the server

The interface MUST decide whether to show an action by the same table as the server
(`run:start`, `run:resume`, `run:cancel`, `run:deliver`, `artifact:edit`, `template:edit`,
`team:manage`, `project:manage`, `tracker:link`, `gate:override`); the server remains the
final authority. A viewer without the right MUST NOT see the button and MUST be told
which roles may do it. In local mode without a principal everything is offered.

#### Scenario: A developer looks at a decision of the product owner

- **КОГДА** the pending approval requires the product owner and the viewer is a developer
- **ТОГДА** the actions column MUST show «Ответить или решить может участник с ролью: …»
  and MUST NOT show the decision buttons

#### Scenario: Stage actions follow the stage function

- **КОГДА** the run detail lists `take`, `results`, `skip` or `run-agent` in
  `allowed_actions` but not in `viewer_actions` (the viewer's role is not the function
  of the stage)
- **ТОГДА** the actions column MUST NOT show those buttons and MUST name the role
  that may act
- **И** the buttons MUST be shown only for the intersection of `allowed_actions` and
  `viewer_actions`

#### Scenario: A pending transition confirmation addresses the completed stage

- **КОГДА** a transition confirmation waits and its `from_stage` differs from the next stage
- **ТОГДА** the return routes and the return request MUST use the `from_stage`, as the
  heading of the group does, and skip MUST NOT be offered
- **И** one return path MUST exist per meaning, under one name

#### Scenario: The administrator right is separate

- **КОГДА** the viewer holds the product owner role without the administrator right
- **ТОГДА** the bypass form MUST NOT be offered and the page MUST say that only an
  administrator may pass a task after a failed required gate

### Requirement: Colour carries meaning

Red MUST mean only a real failure or a bypass of a failure (a failed run or stage, a
failed required gate, a technical error of a required gate, an administrator override,
a failed tracker delivery); amber MUST mean only an advisory gate that did not pass;
waiting, unknown and informational states MUST be neutral or blue. The same concept
MUST look the same everywhere (the same chip, the same icon).

#### Scenario: An override stays red in every view

- **КОГДА** an administrator passed a task after a failed required gate
- **ТОГДА** the stage in the route, the tab «Гейты», the banner of the gate list and the
  history of overrides MUST all mark it red with the text «Обход провала»
- **И** the outcome of the gate MUST still read «Провал»

#### Scenario: A recommendation is amber

- **КОГДА** an advisory gate did not pass
- **ТОГДА** the route, the tab «Гейты» and the banner MUST mark it amber «Рекомендация не выполнена»
- **И** it MUST NOT block the transition

#### Scenario: Waiting is not red

- **КОГДА** the transition waits for gates or a decision
- **ТОГДА** the transition line MUST NOT be red; only «blocked», «error» and «incomplete» are red

### Requirement: Technical gate error can be retried on the same version

A required gate that ended with an error or a timeout MUST be shown as a technical error
(red, dashed, «Ошибка запуска» or «Таймаут»), different from a content failure, and a
retryable gate MUST have a button «Повторить гейт: <имя>» in the actions column that
repeats exactly that gate on the same result version.

#### Scenario: Retry

- **КОГДА** the API allows `gate-retry` and a gate is retryable
- **ТОГДА** the actions column MUST show «Повторить гейт: <имя>» for that gate only

### Requirement: Tracker is a first-class part of the task page

When a tracker is configured the task page MUST show the link with the card, the
synchronization state, failed deliveries, link conflicts and pending inbound requests:
a summary in the status column, link / unlink / retry / sync now in the actions column
and the full lists in the tab «Трекер». A page «Трекер» MUST list the open inbound requests
and the failed deliveries of all tasks with links to them and show the poll state. All
tracker writes MUST be offered only with `tracker:link`; an inbound request MUST never
change the task status in the interface — it is shown with its reason and the action the
controller allows now, and can be closed only with a reason.

#### Scenario: Link, retry and sync

- **КОГДА** the viewer holds `tracker:link`
- **ТОГДА** the actions column MUST offer «Связать с карточкой» (no link) or «Снять связь»
  (linked), «Повторить отправку в трекер (N)» when N deliveries failed and
  «Синхронизировать сейчас» when inbound synchronization is configured

#### Scenario: Pending inbound request

- **КОГДА** the tracker asked for a transition the controller cannot make
- **ТОГДА** the tab MUST show the requested change, the reason and the allowed action
- **И** «Закрыть запрос без действий» MUST require a reason

#### Scenario: Not configured

- **КОГДА** no tracker is configured
- **ТОГДА** the menu MUST NOT show «Трекер» and the task page MUST NOT show the tab

### Requirement: Team and roles are managed with visible controls

The team page MUST edit roles with checkboxes of every role including the system
administrator right, MUST NOT use a free-text prompt, MUST disable the administrator
checkbox (and the edit and revoke of an administrator) for a viewer who is not an
administrator and say why, MUST ask for confirmation of a revocation inline and MUST show
the server refusal (for example, the last administrator) with the editor kept open.

#### Scenario: Role editor

- **КОГДА** the product owner opens «Изменить роли» of a member
- **ТОГДА** the roles MUST be shown as checkboxes and an empty set MUST NOT be savable

### Requirement: Keyboard, focus and phone

The tabs MUST follow the tab pattern (`tablist`, `tab`, `tabpanel`, one tab stop, arrows,
Home and End); focus MUST be visible; targets on narrow screens MUST be at least 44px;
no page MUST scroll horizontally at 360, 390 and 430px.

#### Scenario: Tabs from the keyboard

- **КОГДА** focus is on a tab and the user presses the right arrow
- **ТОГДА** the next tab MUST be selected and focused and its panel shown

### Requirement: Empty and error states are explicit

Every list page MUST distinguish «loading», «error» (with the reason and a retry) and
«empty» (with the first step); a task that cannot be loaded MUST be shown as an alert;
a role without the right to create tasks MUST be told who may.

#### Scenario: Failed load is not an empty list

- **КОГДА** the API answers with an error
- **ТОГДА** the page MUST show the error and a retry button and MUST NOT show «задач нет»
