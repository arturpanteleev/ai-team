## MODIFIED Requirements

### Requirement: Ролевые overrides
Каждый pipeline item MUST поддерживать `name`, `model`, `effort`, `cli`,
`timeout`, `max_retries`, `loopback_to`, `on_negative_verdict`, checkpoint
fields, `checks`, а также необязательные `approval_roles` и
`approval_quorum` для исходящего transition.

#### Scenario: Global fallback
- **КОГДА** model, effort или cli не задан на этапе
- **ТОГДА** MUST использоваться соответствующее глобальное значение

#### Scenario: Effort
- **КОГДА** effort равен low, medium или high
- **ТОГДА** runtime MUST передать это значение в служебные требования prompt
- **И** неизвестное значение MUST быть отклонено

#### Scenario: Approval roles
- **КОГДА** stage содержит `approval_roles`
- **ТОГДА** список MUST быть непустым, без дубликатов и пустых значений
- **И** `approval_quorum` MUST быть `any` или `all`

#### Scenario: Default workflow
- **КОГДА** создаётся default config
- **ТОГДА** смысловые AI transitions MUST иметь human roles для product,
  architecture, development, review и QA ответственности
