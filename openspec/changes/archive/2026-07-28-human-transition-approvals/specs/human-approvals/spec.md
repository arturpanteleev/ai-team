## ADDED Requirements

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
Каждое решение MUST фиксировать actor identity, роль, action, комментарий и
timestamp и MUST проходить role/action/quorum validation.

#### Scenario: Any quorum
- **КОГДА** quorum равен `any` и допустимая required role принимает решение
- **ТОГДА** approval MUST стать resolved

#### Scenario: All quorum
- **КОГДА** quorum равен `all`
- **ТОГДА** approval MUST стать resolved только после одинакового action от
  каждой required role

#### Scenario: Недопустимая роль
- **КОГДА** actor role отсутствует в required roles
- **ТОГДА** decision MUST быть отклонён

### Requirement: Transport-independent waiting
Отсутствие TTY MUST NOT отменять или автоматически отклонять человеческое
решение.

#### Scenario: Non-interactive worker
- **КОГДА** worker достигает защищённого перехода без TTY
- **ТОГДА** он MUST сохранить pending approval и non-terminal lifecycle state
- **И** MUST завершить текущую process session с управляемым stopped status
- **И** тот же run MUST продолжиться после внешнего decision
