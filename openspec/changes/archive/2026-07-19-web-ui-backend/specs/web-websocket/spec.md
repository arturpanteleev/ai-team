## ADDED Requirements

### Requirement: WebSocket hub
Система ДОЛЖНА предоставить WebSocket hub для broadcast updates.

#### Scenario: Подключение клиента
- **КОГДА** клиент подключается к `GET /ws`
- **ТОГДА** сервер ДОЛЖЕН принять WebSocket соединение
- **И** добавить клиента в hub

#### Scenario: Отключение клиента
- **КОГДА** клиент отключается
- **ТОГДА** сервер ДОЛЖЕН удалить клиента из hub

### Requirement: Broadcast events
Система ДОЛЖНА отправлять events всем подключённым клиентам.

#### Scenario: Stage started event
- **КОГДА** pipeline начинает выполнение агента
- **ТОГДА** hub ДОЛЖЕН отправить JSON: `{"type": "stage_started", "pipeline_id": 123, "agent": "analyst"}`

#### Scenario: Stage completed event
- **КОГДА** агент завершается
- **ТОГДА** hub ДОЛЖЕН отправить JSON: `{"type": "stage_completed", "pipeline_id": 123, "agent": "analyst", "status": "passed", "duration_ms": 5000}`

#### Scenario: Pipeline completed event
- **КОГДА** pipeline завершается
- **ТОГДА** hub ДОЛЖЕН отправить JSON: `{"type": "pipeline_completed", "pipeline_id": 123, "status": "completed"}`
