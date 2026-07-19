## ADDED Requirements

### Requirement: WebSocket hook
Frontend ДОЛЖЕН предоставлять хук для WebSocket соединения.

#### Scenario: Подключение
- **КОГДА** приложение загружается
- **ТОГДА** useWebSocket хук ДОЛЖЕН подключиться к `ws://localhost:8080/ws`
- **И** начать слушать events

#### Scenario: Обработка events
- **КОГДА** WebSocket получает event
- **ТОГДА** хук ДОЛЖЕН обновить state приложения:
  - stage_started → обновить статус этапа на running
  - stage_completed → обновить статус и duration этапа
  - pipeline_completed → обновить общий статус

#### Scenario: Reconnect
- **КОГДА** WebSocket отключается
- **ТОГДА** хук ДОЛЖЕН попытаться переподключиться с exponential backoff
- **И** максимум 5 попыток

### Requirement: Автообновление Dashboard
Dashboard ДОЛЖЕН автоматически обновляться при WebSocket events.

#### Scenario: Новый pipeline run
- **КОГДА** WebSocket получает pipeline_completed
- **ТОГДА** Dashboard ДОЛЖЕН добавить/обновить run в списке без перезагрузки
