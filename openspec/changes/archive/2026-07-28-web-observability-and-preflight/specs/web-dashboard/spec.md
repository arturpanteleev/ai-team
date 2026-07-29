## ADDED Requirements

### Requirement: Readiness перед запуском

Dashboard MUST показывать preflight report рядом с формой нового run.

#### Scenario: Required check failed

- **КОГДА** readiness имеет failed required check
- **ТОГДА** dashboard MUST показать конкретную проверку
- **И** MUST заблокировать отправку формы нового run
