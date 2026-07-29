## Purpose

WebSocket клиент для versioned durable lifecycle event stream.

## Requirements

### Requirement: WebSocket hook
Frontend MUST подключаться к `/ws` с последним обработанным cursor,
дедуплицировать events и автоматически переподключаться.

#### Scenario: Подключение
- **КОГДА** приложение загружается
- **ТОГДА** useWebSocket хук MUST построить `ws:` или `wss:` URL из текущего same-origin host
- **И** начать слушать events

#### Scenario: Event получен
- **КОГДА** hook получает валидное version 1 event с новым cursor
- **ТОГДА** hook MUST сохранить cursor
- **И** MUST передать event consumer

#### Scenario: Повтор или reconnect
- **КОГДА** event cursor уже обработан или соединение восстановлено
- **ТОГДА** дубликат MUST быть проигнорирован
- **И** новое соединение MUST передать последний cursor в query parameter
- **И** reconnect delay MUST использовать bounded exponential backoff

### Requirement: Автообновление Dashboard
Dashboard и Pipeline Detail MUST обновлять projection по live lifecycle events;
polling MAY использоваться только как редкий fallback.

#### Scenario: Production event
- **КОГДА** приходит event нового run или attempt
- **ТОГДА** соответствующий экран MUST запросить актуальную projection без
  ожидания polling interval
