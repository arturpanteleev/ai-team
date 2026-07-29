## MODIFIED Requirements

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

## ADDED Requirements

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
