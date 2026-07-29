## ADDED Requirements

### Requirement: Preflight API

Web API MUST предоставлять текущий typed readiness report, а write API нового
run MUST повторно применять тот же gate.

#### Scenario: Окружение не готово

- **КОГДА** клиент читает preflight при failed required check
- **ТОГДА** API MUST вернуть report с `ready=false`
- **И** последующий start MUST быть отклонён с объяснением причины
