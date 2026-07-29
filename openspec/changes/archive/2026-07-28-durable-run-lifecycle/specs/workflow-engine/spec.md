## MODIFIED Requirements

### Requirement: Explicit state machine
Pipeline MUST вычислять execution/decision/outcome явно и MUST сохранять
подтверждённое состояние на durable stage boundaries.

#### Scenario: Process restart
- **КОГДА** процесс завершается между stages
- **ТОГДА** новый процесс MUST восстановить следующий допустимый stage из
  persisted state
- **И** MUST NOT повторять уже подтверждённый attempt без явного rollback
