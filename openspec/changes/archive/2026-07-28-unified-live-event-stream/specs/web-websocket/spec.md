## MODIFIED Requirements

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
