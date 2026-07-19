## Purpose

REST API для web UI — endpoints для получения информации о pipeline runs, stages и артефактах.

## Requirements

### Requirement: GET /api/pipelines
Система ДОЛЖНА возвращать список всех pipeline runs.

#### Scenario: Успешный запрос
- **КОГДА** клиент отправляет `GET /api/pipelines`
- **ТОГДА** сервер ДОЛЖЕН вернуть JSON массив pipeline runs
- **И** каждый run содержит: id, feature, status, started_at, completed_at
- **И** runs отсортированы по started_at (новые первые)

#### Scenario: Пустая БД
- **КОГДА** нет pipeline runs
- **ТОГДА** сервер ДОЛЖЕН вернуть пустой массив `[]`

### Requirement: GET /api/pipelines/:id
Система ДОЛЖНА возвращать детали конкретного pipeline run.

#### Scenario: Успешный запрос
- **КОГДА** клиент отправляет `GET /api/pipelines/123`
- **ТОГДА** сервер ДОЛЖЕН вернуть JSON с:
  - run: id, feature, status, started_at, completed_at, config_snapshot
  - stages: массив stage с agent_name, status, duration_ms, inputs, outputs

#### Scenario: Run не найден
- **КОГДА** pipeline run с id 123 не существует
- **ТОГДА** сервер ДОЛЖЕН вернуть 404

### Requirement: GET /api/pipelines/:id/artifacts
Система ДОЛЖНА возвращать список артефактов pipeline run.

#### Scenario: Успешный запрос
- **КОГДА** клиент отправляет `GET /api/pipelines/123/artifacts`
- **ТОГДА** сервер ДОЛЖЕН вернуть JSON массив артефактов
- **И** каждый артефакт содержит: name, path, size, mod_time

### Requirement: GET /api/artifacts/:path
Система ДОЛЖНА возвращать содержимое артефакта.

#### Scenario: Текстовый артефакт
- **КОГДА** клиент запрашивает артефакт `.md` файл
- **ТОГДА** сервер ДОЛЖЕН вернуть содержимое файла с Content-Type: text/markdown

#### Scenario: Артефакт не найден
- **КОГДА** файл не существует
- **ТОГДА** сервер ДОЛЖЕН вернуть 404