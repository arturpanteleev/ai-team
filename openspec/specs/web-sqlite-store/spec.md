## Purpose

Версионированная SQLite projection run/attempt/event истории.

## Requirements

### Requirement: Run and attempt projection
SQLite MUST хранить внешний unique run_id, attempt_id, stage_index, execution,
decision, outcome, verdict, checks, mutations, delivery results и
упорядоченный durable lifecycle event stream.

#### Scenario: Попытка завершена
- **КОГДА** controller публикует StageResult
- **ТОГДА** соответствующая запись attempt MUST обновляться только по её identity

#### Scenario: Cursor query
- **КОГДА** consumer запрашивает events после cursor с bounded limit
- **ТОГДА** store MUST вернуть только events с большим id
- **И** MUST упорядочить их по id по возрастанию

### Requirement: Versioned migrations
Schema migrations MUST иметь номера в `schema_migrations` и применяться без потери существующих rows.

#### Scenario: Legacy database
- **КОГДА** открывается БД со старой таблицей pipeline_runs/stages
- **ТОГДА** недостающие identity и evidence columns MUST быть добавлены идемпотентно

#### Scenario: Database before projects
- **КОГДА** открывается БД версии 13 без таблицы `projects`
- **ТОГДА** миграция MUST создать проект «Основной» (ID 1) и колонки `project_id` у `pipeline_runs` и `backlog_items`
- **И** все существующие run и элементы бэклога MUST принадлежать проекту «Основной», а повторное открытие MUST NOT создавать дубликатов

#### Scenario: Already migrated database
- **КОГДА** открывается БД, у которой `MAX(version)` в `schema_migrations` равна версии бинарника
- **ТОГДА** store MUST только прочитать версию и MUST NOT брать write-lock, поэтому параллельные открытия занятой БД не получают `SQLITE_BUSY`

#### Scenario: Migration required
- **КОГДА** версия БД меньше версии бинарника
- **ТОГДА** миграция MUST выполняться в `BEGIN IMMEDIATE`, а версия MUST перечитываться под блокировкой, чтобы параллельный процесс не мигрировал повторно

#### Scenario: Newer database
- **КОГДА** версия БД больше версии бинарника
- **ТОГДА** store MUST отказаться открыть БД до любой записи в неё

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
