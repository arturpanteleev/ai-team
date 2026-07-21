## MODIFIED Requirements

### Requirement: Запись запусков в SQLite из CLI
Каждый `ai-team run` MUST записывать запуск и этапы в `{target}/.ai-team/web.db` через `Recorder`-интерфейс пайплайна (WebSocket-хаб для межпроцессных событий не используется).

#### Scenario: Pipeline started
- **КОГДА** пайплайн запускается
- **ТОГДА** MUST быть создана запись в `pipeline_runs` (feature, status=running, started_at, config_snapshot)

#### Scenario: Stage lifecycle
- **КОГДА** этап начинается / завершается
- **ТОГДА** MUST создаваться/обновляться запись в `stages` (status, duration_ms, error, verdict, inputs/outputs)

#### Scenario: Pipeline completed
- **КОГДА** пайплайн завершается
- **ТОГДА** запись run MUST получить финальный status (completed/failed/blocked) и completed_at

#### Scenario: Недоступная БД не валит пайплайн
- **КОГДА** SQLite недоступна (например, нет прав)
- **ТОГДА** пайплайн MUST вывести warning и продолжить без записи

### Requirement: Verdict в stages
Таблица `stages` MUST содержать колонку `verdict`; миграция добавляет её в существующие БД.

#### Scenario: Миграция существующей БД
- **КОГДА** сервер или CLI открывает БД без колонки `verdict`
- **ТОГДА** колонка MUST быть добавлена без потери данных
