## MODIFIED Requirements

### Requirement: Run and attempt identity
Каждый долговечный run MUST иметь одну immutable run_id во всех process
sessions; каждый запуск агента MUST иметь новый attempt_id.

#### Scenario: Resume evidence
- **КОГДА** non-terminal run возобновляется после restart
- **ТОГДА** существующий verified event chain MUST быть продолжен
- **И** MUST быть добавлен `run_resumed`, но не второй `run_started`
- **И** новые attempt identifiers MUST продолжить прежнюю ordinal sequence
