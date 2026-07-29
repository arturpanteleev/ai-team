## MODIFIED Requirements

### Requirement: Pipeline detail page
Frontend MUST отображать детали pipeline run и обновлять их по versioned live
events.

#### Scenario: Live updates
- **КОГДА** WebSocket отправляет `attempt_started`, `attempt_finished`,
  `attempts_invalidated` или `run_finished` выбранного run
- **ТОГДА** страница MUST запросить актуальную SQLite projection без browser
  reload
- **И** редкий polling MAY использоваться как recovery fallback
