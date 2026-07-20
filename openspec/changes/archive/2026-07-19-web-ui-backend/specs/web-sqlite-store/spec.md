## ADDED Requirements

### Requirement: SQLite schema для pipeline runs
Система MUST хранить информацию о pipeline runs в SQLite.

#### Scenario: Таблица pipeline_runs
- **КОГДА** pipeline запускается
- **ТОГДА** запись MUST быть создана в таблице `pipeline_runs` с полями:
  - `id` (INTEGER PRIMARY KEY)
  - `feature` (TEXT)
  - `status` (TEXT: running/completed/failed/blocked)
  - `started_at` (DATETIME)
  - `completed_at` (DATETIME, nullable)
  - `config_snapshot` (TEXT — JSON конфига)

#### Scenario: Таблица stages
- **КОГДА** агент завершается
- **ТОГДА** запись MUST быть создана в таблице `stages` с полями:
  - `id` (INTEGER PRIMARY KEY)
  - `pipeline_run_id` (INTEGER FK)
  - `agent_name` (TEXT)
  - `status` (TEXT: running/passed/failed/blocked)
  - `started_at` (DATETIME)
  - `completed_at` (DATETIME, nullable)
  - `duration_ms` (INTEGER)
  - `error` (TEXT, nullable)
  - `inputs_json` (TEXT — JSON артефактов)
  - `outputs_json` (TEXT — JSON артефактов)

### Requirement: Автоматические миграции
SQLite schema MUST автоматически применяться при запуске сервера.

#### Scenario: Первый запуск
- **КОГДА** SQLite файл не существует
- **ТОГДА** система MUST создать файл и применить schema

#### Scenario: Обновление schema
- **КОГДА** schema изменилась
- **ТОГДА** система MUST применить миграции без потери данных
