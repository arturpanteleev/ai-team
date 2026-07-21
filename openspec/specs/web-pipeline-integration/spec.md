## Purpose

Проекция доменного lifecycle workflow в SQLite и web UI.

## Requirements

### Requirement: Recorder adapter
Pipeline MUST передавать recorder-у run_id, attempt_id, точные timestamps и terminal domain state.

#### Scenario: Stage lifecycle
- **КОГДА** попытка начинается и завершается
- **ТОГДА** recorder MUST создать running row по attempt_id
- **И** MUST записать status, execution, decision, outcome, error, verdict, artifacts, checks, mutations и delivery

### Requirement: Projection failure isolation
Ошибка web projection MUST быть явно залогирована, но MUST NOT менять результат immutable workflow.

#### Scenario: SQLite недоступна
- **КОГДА** запись lifecycle event завершается ошибкой
- **ТОГДА** recorder MUST отключить дальнейшую projection для run
- **И** filesystem evidence MUST продолжить публиковаться

### Requirement: WebSocket safety
WebSocket MUST принимать browser connection только с same origin и MUST использовать ping/deadline для удаления мёртвых clients.

#### Scenario: Медленный client
- **КОГДА** client не читает bounded send queue
- **ТОГДА** hub MUST отключить его без блокировки других clients
