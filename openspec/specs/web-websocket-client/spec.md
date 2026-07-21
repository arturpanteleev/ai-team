## Purpose

WebSocket клиент для опциональных in-process updates; polling SQLite API
остаётся обязательным механизмом обнаружения cross-process CLI runs.

## Requirements

### Requirement: WebSocket hook
Frontend MUST предоставлять хук для WebSocket соединения.

#### Scenario: Подключение
- **КОГДА** приложение загружается
- **ТОГДА** useWebSocket хук MUST построить `ws:` или `wss:` URL из текущего same-origin host
- **И** начать слушать events

#### Scenario: Обработка events
- **КОГДА** WebSocket получает event
- **ТОГДА** хук MUST обновить state приложения:
  - stage_started → обновить статус этапа на running
  - stage_completed → обновить статус и duration этапа
  - pipeline_completed → обновить общий статус

#### Scenario: Reconnect
- **КОГДА** WebSocket отключается
- **ТОГДА** хук MUST попытаться переподключиться с exponential backoff
- **И** задержка MUST быть bounded

### Requirement: Автообновление Dashboard
Dashboard MUST автоматически обновляться при WebSocket events и через bounded
periodic polling независимо от наличия running rows.

#### Scenario: Новый pipeline run
- **КОГДА** WebSocket получает pipeline_completed
- **ТОГДА** Dashboard MUST добавить/обновить run в списке без перезагрузки

#### Scenario: CLI не подключён к текущему Hub
- **КОГДА** CLI пишет lifecycle projection в SQLite из отдельного процесса
- **ТОГДА** Dashboard MUST периодически poll даже если список пуст или все известные runs terminal
- **И** Pipeline Detail MUST poll пока выбранный run имеет статус running
