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

### Requirement: Целостность persisted approval

Каждая persisted approval-запись MUST быть аутентифицирована MAC ключа
контроллера, и этот ключ MUST храниться вне target: в target пишет агент, и
запись, подделываемая без ключа, не является решением человека. Любое чтение
записи MUST проверять MAC до применения её семантики и MUST fail-closed
отказывать при несовпадении.

#### Scenario: Решение изменено на диске

- **КОГДА** approval-запись изменена после того, как её записал контроллер
  (например, статус переведён в resolved мимо `ai-team decision`)
- **ТОГДА** resume MUST быть отклонён с указанием причины
- **И** delivery MUST NOT быть выполнена

#### Scenario: Запись создана мимо контроллера

- **КОГДА** approval-запись не содержит MAC или подписана другим ключом
- **ТОГДА** запись MUST быть отвергнута как неаутентифицированная

### Requirement: Approval subject привязан к candidate

Subject transition approval, относящегося к source workflow, MUST включать
exact candidate workspace hash.

#### Scenario: Решение устарело после mutation

- **КОГДА** candidate workspace hash больше не совпадает с hash subject
- **ТОГДА** decision или resume MUST быть отклонён как stale
