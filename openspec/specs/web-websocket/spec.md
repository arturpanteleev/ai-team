## Purpose

Опциональный in-process WebSocket transport. Он не является источником истины и
не переносит SQLite events между отдельными CLI и web process.

## Requirements

### Requirement: WebSocket hub
Система MUST предоставить WebSocket hub для явно опубликованных in-process updates.

#### Scenario: Подключение клиента
- **КОГДА** клиент подключается к `GET /ws`
- **ТОГДА** сервер MUST принять WebSocket соединение
- **И** добавить клиента в hub

#### Scenario: Отключение клиента
- **КОГДА** клиент отключается
- **ТОГДА** сервер MUST удалить клиента из hub

### Requirement: Broadcast events
Hub MUST отправлять явно опубликованные events всем подключённым клиентам; cross-process SQLite writer MUST NOT ошибочно считаться подключённым к in-memory hub.

#### Scenario: Stage started event
- **КОГДА** producer публикует stage started event в hub
- **ТОГДА** hub MUST отправить JSON: `{"type": "stage_started", "pipeline_id": 123, "agent": "analyst"}`

#### Scenario: Stage completed event
- **КОГДА** producer публикует stage completed event в hub
- **ТОГДА** hub MUST отправить JSON: `{"type": "stage_completed", "pipeline_id": 123, "agent": "analyst", "status": "passed", "duration_ms": 5000}`

#### Scenario: Pipeline completed event
- **КОГДА** producer публикует pipeline completed event в hub
- **ТОГДА** hub MUST отправить JSON: `{"type": "pipeline_completed", "pipeline_id": 123, "status": "completed"}`

### Requirement: Connection safety
Browser connections MUST быть same-origin, иметь bounded send queues, read/write deadlines и ping/pong cleanup.

#### Scenario: Медленный клиент
- **КОГДА** send queue клиента заполнена
- **ТОГДА** hub MUST отключить клиента без блокировки остальных consumers
