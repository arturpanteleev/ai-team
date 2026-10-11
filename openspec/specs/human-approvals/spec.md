# human-approvals Specification

## Purpose
PendingApproval/ApprovalDecision как typed сущности: exact subject hash, stale-rejection, idempotency и persisted waiting state.
## Requirements
### Requirement: Exact transition approval
Каждый защищённый workflow transition MUST иметь persisted approval,
привязанный к точному subject hash.

#### Scenario: Approval requested
- **КОГДА** stage предлагает защищённый переход
- **ТОГДА** controller MUST сохранить run_id, attempt_id, from/to stage,
  trigger, subject hash, actions, required roles и quorum
- **И** MUST перевести run в `waiting`

#### Scenario: Stale subject
- **КОГДА** decision содержит другой subject hash
- **ТОГДА** decision MUST быть отклонён без изменения run

### Requirement: Audited human decision

Каждое решение MUST фиксировать trusted actor identity, роль, action,
комментарий и timestamp и MUST проходить role/action/quorum validation. В
cloud mode actor identity и доступные роли MUST поступать из
аутентифицированной server-side session, а не из command body.

#### Scenario: Any quorum

- **КОГДА** quorum равен `any` и аутентифицированный actor с допустимой
  required role принимает решение
- **ТОГДА** approval MUST стать resolved

#### Scenario: All quorum

- **КОГДА** quorum равен `all`
- **ТОГДА** approval MUST стать resolved только после одинакового action от
  каждой required role

#### Scenario: Недопустимая или неподтверждённая роль

- **КОГДА** actor role отсутствует в required roles или ролях principal
- **ТОГДА** decision MUST быть отклонён

### Requirement: Transport-independent waiting
Отсутствие TTY MUST NOT отменять или автоматически отклонять человеческое
решение.

#### Scenario: Non-interactive worker
- **КОГДА** worker достигает защищённого перехода без TTY
- **ТОГДА** он MUST сохранить pending approval и non-terminal lifecycle state
- **И** MUST завершить текущую process session с управляемым stopped status
- **И** тот же run MUST продолжиться после внешнего decision

### Requirement: Typed human stage input

Approval storage MUST support `kind: input` as a typed human-stage submission,
separate from transition approvals and question-answer loops. The immutable
payload MUST bind stage ID, result type, output name/path, and exact subject
hash. A markdown or link submission MUST preserve the submitted comment bytes;
retries from the same actor with identical action and bytes MUST be idempotent,
while changed bytes MUST be rejected. Human input MUST resolve only through
an authorized actor and MUST target the same stage so resume cannot skip a
graph node.

#### Scenario: Exact typed submission

- **КОГДА** an authorized actor submits a non-empty result for a pending input
  approval with the exact subject
- **ТОГДА** the controller MUST retain the exact content and actor, resolve the
  approval, and permit resume of the bound stage

#### Scenario: Conflicting retry

- **КОГДА** the same actor resubmits different bytes or an action not permitted
  by the declared result type
- **ТОГДА** the store MUST reject the update and preserve the original
  decision

#### Scenario: Input cannot be bypassed

- **КОГДА** a run uses `--approve-gates` while waiting for a human input
- **ТОГДА** the missing typed result MUST remain unresolved and the stage MUST
  remain waiting

### Requirement: Skip and return actions require a reason

Every human decision that skips a stage or selects a configured return MUST
include a non-empty reason. Whitespace-only reasons MUST be rejected without
resolving the approval. A skip decision MUST remain bound to the same human
input stage; a return decision MUST remain bound to the exact configured graph
route.

#### Scenario: Human skips an optional input stage

- **КОГДА** a waiting `kind: input` approval belongs to a `skippable` human
  stage and an authorized actor chooses `skip` with a reason
- **ТОГДА** the controller MUST resolve the input approval, record a skipped
  attempt and warning event, and MUST NOT write a synthetic stage result

#### Scenario: Skip or return has no reason

- **КОГДА** a skip or return decision has an empty or whitespace-only reason
- **ТОГДА** the store MUST reject it and leave the approval unresolved

### Requirement: Approval subject привязан к candidate

Subject transition approval, относящегося к source workflow, MUST включать
exact candidate workspace hash.

#### Scenario: Решение устарело после mutation

- **КОГДА** candidate workspace hash больше не совпадает с hash subject
- **ТОГДА** decision или resume MUST быть отклонён как stale
