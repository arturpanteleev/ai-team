## Purpose

Единый versioned contract для durable replay и live-доставки lifecycle events.
## Requirements
### Requirement: Versioned durable event contract
Каждое live lifecycle event MUST иметь versioned wire representation с
глобальным cursor и run-local sequence.

#### Scenario: Сериализация события
- **КОГДА** сохранённое SQLite event публикуется клиенту
- **ТОГДА** JSON MUST содержать `version: 1`, `cursor`, `run_id`, `sequence`,
  `type`, `timestamp` и object `data`
- **И** `cursor` MUST монотонно возрастать глобально
- **И** `sequence` MUST отражать порядок внутри `run_id`

### Requirement: Cursor replay
Клиент MUST иметь возможность продолжить поток после последнего подтверждённого
cursor без потери событий.

#### Scenario: Reconnect
- **КОГДА** клиент подключается к `/ws?cursor=N`
- **ТОГДА** сервер MUST сначала отправить durable events с `cursor > N` по
  возрастанию
- **И** затем MUST продолжить live delivery
- **И** события, сохранённые на границе replay/live, MUST NOT потеряться

### Requirement: Live-событие выбранного ребра

Каждый выбранный graph transition MUST сохраняться в SQLite event projection
и передаваться через versioned WebSocket stream.

#### Scenario: Transition выбран

- **КОГДА** edge action resolved и exact target определён
- **ТОГДА** `transition_selected` MUST содержать from, outcome, edge target,
  action и selected target
- **И** reconnect MUST восстановить событие через cursor replay
