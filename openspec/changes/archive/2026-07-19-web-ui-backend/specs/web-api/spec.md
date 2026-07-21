## ADDED Requirements

### Requirement: GET /api/pipelines
Система MUST возвращать список всех pipeline runs.

#### Scenario: Успешный запрос
- **КОГДА** клиент отправляет `GET /api/pipelines`
- **ТОГДА** сервер MUST вернуть JSON массив pipeline runs
- **И** каждый run содержит: id, feature, status, started_at, completed_at
- **И** runs отсортированы по started_at (новые первые)

#### Scenario: Пустая БД
- **КОГДА** нет pipeline runs
- **ТОГДА** сервер MUST вернуть пустой массив `[]`

### Requirement: GET /api/pipelines/:id
Система MUST возвращать детали конкретного pipeline run.

#### Scenario: Успешный запрос
- **КОГДА** клиент отправляет `GET /api/pipelines/123`
- **ТОГДА** сервер MUST вернуть JSON с:
  - run: id, feature, status, started_at, completed_at, config_snapshot
  - stages: массив stage с agent_name, status, duration_ms, inputs, outputs

#### Scenario: Run не найден
- **КОГДА** pipeline run с id 123 не существует
- **ТОГДА** сервер MUST вернуть 404

### Requirement: GET /api/pipelines/:id/artifacts
Система MUST возвращать список артефактов pipeline run.

#### Scenario: Успешный запрос
- **КОГДА** клиент отправляет `GET /api/pipelines/123/artifacts`
- **ТОГДА** сервер MUST вернуть JSON массив артефактов
- **И** каждый артефакт содержит: name, path, size, mod_time

### Requirement: GET /api/artifacts/:path
Система MUST возвращать содержимое артефакта.

#### Scenario: Текстовый артефакт
- **КОГДА** клиент запрашивает артефакт `.md` файл
- **ТОГДА** сервер MUST вернуть содержимое файла с Content-Type: text/markdown

#### Scenario: Артефакт не найден
- **КОГДА** файл не существует
- **ТОГДА** сервер MUST вернуть 404
