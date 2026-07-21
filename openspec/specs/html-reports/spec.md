## Purpose

Run/attempt-aware HTML reports без ложного зелёного статуса.

## Requirements

### Requirement: Stage attempt report
После каждой попытки система MUST создать stage report с run_id, attempt_id, status, execution, decision, outcome, verdict, duration, error, artifacts, deterministic checks, mutations и delivery results.

#### Scenario: Required check упал
- **КОГДА** LLM verdict положителен, но required check завершился ненулевым exit code
- **ТОГДА** отчёт MUST показывать failed outcome, command, exit code и reason

#### Scenario: Attempt invalidated
- **КОГДА** loopback заменил попытку
- **ТОГДА** итоговый отчёт MUST показывать её как Invalidated, а не Passed или Failed

### Requirement: Final report
При любом terminal outcome система MUST сформировать итоговый отчёт со всеми попытками и overall status.

#### Scenario: Stopped delivery approval
- **КОГДА** delivery plan создан, но approval отсутствует
- **ТОГДА** final report MUST показывать run stopped и delivery attempt skipped

### Requirement: Immutable publication
После генерации controller MUST скопировать report tree в immutable run directory.

#### Scenario: Более новый run той же фичи
- **КОГДА** live report перезаписан новым run
- **ТОГДА** immutable report старого run MUST сохранить прежнее содержимое

### Requirement: Stage summary
Report MAY показывать свежий `.stage-summary/{agent}.md`, но stale summary из предыдущей попытки MUST быть удалён до запуска этапа.

#### Scenario: Summary не создан
- **КОГДА** текущая попытка не публикует summary
- **ТОГДА** отчёт MUST NOT переиспользовать старый summary
