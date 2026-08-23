# runtime-preflight Specification

## Purpose
Typed runtime preflight перед созданием run: OpenCode/version, model/provider, credentials, Git и delivery-зависимости; fail-closed.
## Requirements
### Requirement: Типизированная проверка готовности

Система MUST формировать preflight report со стабильными идентификаторами
проверок, статусом, обязательностью и безопасным сообщением.

#### Scenario: Обязательная проверка не пройдена

- **КОГДА** хотя бы одна required check имеет status failed
- **ТОГДА** report MUST иметь `ready=false`
- **И** новый run MUST NOT быть принят

#### Scenario: Диагностическая проверка предупреждает

- **КОГДА** optional check имеет status warning
- **ТОГДА** report MUST сохранить предупреждение
- **И** оно MUST NOT само по себе блокировать run

### Requirement: Готовность OpenCode

Preflight MUST проверить доступность и версию OpenCode, выбранную model/provider
и явно разрешённые имена credential environment variables без раскрытия
значений.

#### Scenario: OpenCode отсутствует

- **КОГДА** configured OpenCode executable не найден
- **ТОГДА** required check MUST завершиться failed до первого AI call

#### Scenario: Credential разрешён явно

- **КОГДА** имя переменной указано в `AI_TEAM_OPENCODE_ENV_ALLOW`
- **ТОГДА** report MUST показать только имя и факт наличия
- **И** MUST NOT содержать значение переменной

### Requirement: Готовность delivery

Preflight MUST проверять GitHub CLI, authentication и remote только когда
скомпилированный workflow содержит delivery stage.

#### Scenario: Workflow без delivery

- **КОГДА** pipeline не содержит delivery stage
- **ТОГДА** отсутствие `gh` MUST NOT блокировать run

#### Scenario: Delivery требует GitHub

- **КОГДА** pipeline содержит delivery stage и `gh auth status` неуспешен
- **ТОГДА** readiness MUST быть false до начала run
