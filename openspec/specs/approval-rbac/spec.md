# approval-rbac Specification

## Purpose
TBD - created by archiving change cloud-auth-and-rbac. Update Purpose after archive.
## Requirements
### Requirement: Канонические workflow-роли

Authorization MUST поддерживать роли Product Owner, Architect, Developer,
Reviewer, QA и Release Manager в нормализованном машинном представлении.

#### Scenario: Неизвестная роль

- **КОГДА** credential или command содержит роль вне канонического набора
- **ТОГДА** операция MUST быть отклонена

### Requirement: Trusted actor decision

Cloud approval decision MUST использовать actor ID и роли
аутентифицированного principal.

#### Scenario: Подмена actor ID

- **КОГДА** command пытается передать другой actor ID
- **ТОГДА** server MUST игнорировать недоверенное значение или отклонить
  command и MUST записать identity из session

#### Scenario: Чужая required role

- **КОГДА** principal выбирает required role, которой у него нет
- **ТОГДА** decision MUST быть отклонён без изменения approval

### Requirement: RBAC control commands

Start, resume и cancel MUST проверяться server-side RBAC policy.

#### Scenario: Start

- **КОГДА** principal имеет роль Product Owner или Architect
- **ТОГДА** он MAY создать run

#### Scenario: Cancel

- **КОГДА** principal не имеет роль Product Owner или Release Manager
- **ТОГДА** cancel MUST быть отклонён
