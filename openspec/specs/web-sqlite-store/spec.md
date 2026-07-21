## Purpose

Версионированная SQLite projection run/attempt/event истории.

## Requirements

### Requirement: Run and attempt projection
SQLite MUST хранить внешний unique run_id, attempt_id, stage_index, execution, decision, outcome, verdict, checks, mutations и delivery results.

#### Scenario: Попытка завершена
- **КОГДА** controller публикует StageResult
- **ТОГДА** соответствующая запись attempt MUST обновляться только по её identity

### Requirement: Versioned migrations
Schema migrations MUST иметь номера в `schema_migrations` и применяться без потери существующих rows.

#### Scenario: Legacy database
- **КОГДА** открывается БД со старой таблицей pipeline_runs/stages
- **ТОГДА** недостающие identity и evidence columns MUST быть добавлены идемпотентно

### Requirement: SQLite concurrency policy
Store MUST включать foreign keys, busy timeout и WAL для файловой БД.

#### Scenario: Concurrent reader
- **КОГДА** CLI записывает попытку, а web читает историю
- **ТОГДА** store MUST ожидать занятый writer в пределах busy timeout вместо немедленного `database is locked`

### Requirement: Interrupted reconciliation
После получения exclusive workspace lock controller MUST пометить оставшиеся running rows как interrupted.

#### Scenario: Предыдущий процесс аварийно завершился
- **КОГДА** новый run получает workspace lock
- **ТОГДА** старый running run и его running attempts MUST получить terminal status interrupted
