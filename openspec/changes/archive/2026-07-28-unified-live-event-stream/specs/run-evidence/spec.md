## MODIFIED Requirements

### Requirement: Run and attempt identity
Каждый run MUST иметь immutable run_id, каждый запуск агента — attempt_id;
durable projection events MUST сохранять обе identity и run-local sequence.

#### Scenario: Projection event identity
- **КОГДА** recorder сохраняет lifecycle event
- **ТОГДА** event MUST содержать run_id и sequence
- **И** attempt event MUST содержать attempt_id
- **И** SQLite id MUST предоставлять глобальный delivery cursor
