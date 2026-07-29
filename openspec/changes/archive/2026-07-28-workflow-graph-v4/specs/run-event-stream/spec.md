## ADDED Requirements

### Requirement: Live-событие выбранного ребра

Каждый выбранный graph transition MUST сохраняться в SQLite event projection
и передаваться через versioned WebSocket stream.

#### Scenario: Transition выбран

- **КОГДА** edge action resolved и exact target определён
- **ТОГДА** `transition_selected` MUST содержать from, outcome, edge target,
  action и selected target
- **И** reconnect MUST восстановить событие через cursor replay
