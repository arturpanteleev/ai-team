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
`GET /api/pipelines/:id` MUST возвращать run, упорядоченные attempts и
pending/resolved approvals с actions, roles, quorum, exact subject и audit
decisions.

#### Scenario: Run не найден
- **КОГДА** numeric projection id отсутствует
- **ТОГДА** сервер MUST вернуть 404

#### Scenario: Run ожидает человека
- **КОГДА** lifecycle run имеет pending approval
- **ТОГДА** response MUST содержать approval identity и все данные,
  необходимые для осознанного решения

### Requirement: Immutable artifacts
`GET /api/pipelines/:id/artifacts` MUST перечислять evidence выбранного run, а `GET /api/runs/:runID/artifacts/:path` MUST читать только его immutable run directory.

#### Scenario: Фича запущена повторно
- **КОГДА** live artifact той же фичи изменился после старого run
- **ТОГДА** API старого run MUST вернуть старое immutable содержимое

#### Scenario: Traversal или symlink
- **КОГДА** path выходит из run root лексически или через symlink
- **ТОГДА** сервер MUST отказать в доступе

### Requirement: Run command API
API MUST предоставлять `POST /api/runs`, `POST /api/runs/{runID}/resume` и
`POST /api/runs/{runID}/cancel`.

#### Scenario: Accepted
- **КОГДА** команда валидна и worker может быть запущен
- **ТОГДА** API MUST вернуть 202 и run_id

#### Scenario: Невалидный JSON
- **КОГДА** body превышает limit, содержит unknown fields или trailing JSON
- **ТОГДА** API MUST вернуть 400 без запуска worker

### Requirement: Approval decision API
API MUST предоставлять
`POST /api/runs/{runID}/approvals/{approvalID}/decisions`.

#### Scenario: Audit actor
- **КОГДА** решение принято
- **ТОГДА** response MUST вернуть resolved/pending status
- **И** persisted decision MUST содержать actor, role, action и comment

### Requirement: Preflight API

Web API MUST предоставлять текущий typed readiness report, а write API нового
run MUST повторно применять тот же gate.

#### Scenario: Окружение не готово

- **КОГДА** клиент читает preflight при failed required check
- **ТОГДА** API MUST вернуть report с `ready=false`
- **И** последующий start MUST быть отклонён с объяснением причины

### Requirement: Immutable workflow API

Web API MUST отдавать compiled workflow snapshot exact существующего run без
доступа к произвольным run files.

#### Scenario: Workflow запрошен

- **КОГДА** клиент запрашивает workflow существующего run
- **ТОГДА** API MUST вернуть immutable `workflow.json`
- **И** MUST NOT подменять его текущим config
