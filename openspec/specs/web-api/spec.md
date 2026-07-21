## Purpose

Run-aware REST API для истории workflow и immutable evidence.

## Requirements

### Requirement: Paginated run list
`GET /api/pipelines` MUST возвращать runs в порядке started_at DESC с параметрами `limit` от 1 до 100 и неотрицательным `offset`.

#### Scenario: Успешный запрос
- **КОГДА** клиент запрашивает `/api/pipelines?limit=50&offset=0`
- **ТОГДА** сервер MUST вернуть JSON array с `id`, `run_id`, feature, status и timestamps
- **И** MUST вернуть общее количество в `X-Total-Count`

#### Scenario: Невалидный limit
- **КОГДА** limit больше 100
- **ТОГДА** сервер MUST вернуть 400

### Requirement: Run details
`GET /api/pipelines/:id` MUST возвращать run и упорядоченные attempts с attempt_id, execution, decision, outcome, verdict и evidence JSON fields.

#### Scenario: Run не найден
- **КОГДА** numeric projection id отсутствует
- **ТОГДА** сервер MUST вернуть 404

### Requirement: Immutable artifacts
`GET /api/pipelines/:id/artifacts` MUST перечислять evidence выбранного run, а `GET /api/runs/:runID/artifacts/:path` MUST читать только его immutable run directory.

#### Scenario: Фича запущена повторно
- **КОГДА** live artifact той же фичи изменился после старого run
- **ТОГДА** API старого run MUST вернуть старое immutable содержимое

#### Scenario: Traversal или symlink
- **КОГДА** path выходит из run root лексически или через symlink
- **ТОГДА** сервер MUST отказать в доступе
