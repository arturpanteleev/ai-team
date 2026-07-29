## Purpose

Долговечное исполнение run независимо от жизненного цикла controller process.

## Requirements

### Requirement: Persisted run state machine
Controller MUST атомарно сохранять исполняемое состояние run независимо от
жизненного цикла процесса.

#### Scenario: Stage boundary
- **КОГДА** attempt завершён и следующий переход определён
- **ТОГДА** state MUST сохранить run_id, phase, next stage и attempt ordinal
- **И** checkpoint MUST быть атомарным

#### Scenario: Повреждённое состояние
- **КОГДА** state отсутствует, повреждён или не совпадает с immutable evidence
- **ТОГДА** resume MUST завершиться ошибкой без запуска агента

### Requirement: Resume same run
Новый controller process MUST иметь возможность продолжить resumable run с той
же identity и verified evidence chain.

#### Scenario: Resume после restart
- **КОГДА** пользователь возобновляет non-terminal run
- **ТОГДА** controller MUST сохранить прежний run_id
- **И** MUST проверить event chain и snapshot hashes
- **И** MUST продолжить с persisted next stage
- **И** MUST NOT создавать второй `run_started`

#### Scenario: Terminal run
- **КОГДА** запрошен resume terminal run
- **ТОГДА** controller MUST отклонить операцию
