## Purpose

Live WebSocket transport поверх durable SQLite event stream.

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
Hub MUST публиковать versioned events из production SQLite stream и
поддерживать durable replay по cursor.

#### Scenario: Event записан другим процессом
- **КОГДА** CLI сохраняет lifecycle event в общей SQLite database
- **ТОГДА** запущенный web server MUST обнаружить event
- **И** MUST передать его подключённым WebSocket clients

#### Scenario: Replay перед live mode
- **КОГДА** клиент подключается с валидным cursor
- **ТОГДА** hub MUST упорядоченно отправить пропущенные durable events
- **И** после replay MUST продолжить live broadcast

### Requirement: Connection safety
Browser connections MUST быть same-origin, иметь bounded send queues, read/write deadlines и ping/pong cleanup.

#### Scenario: Медленный клиент
- **КОГДА** send queue клиента заполнена
- **ТОГДА** hub MUST отключить клиента без блокировки остальных consumers
