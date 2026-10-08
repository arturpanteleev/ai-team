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

### Requirement: Диагностика способа входа Claude Code и Codex

Для Claude Code и Codex preflight MUST показывать один из способов входа:
API-ключ, подписка или «не найден». Он MUST проверять только наличие
доступного способа, MUST NOT отправлять credential провайдеру и MUST NOT
раскрывать значения токенов.

#### Scenario: Claude Code использует подписку

- **КОГДА** `CLAUDE_CODE_OAUTH_TOKEN` задан
- **ТОГДА** preflight MUST показать вход по подписке
- **И** Claude subprocess MUST получить эту переменную без дополнительного
  разрешения имени в allow-list

#### Scenario: Codex использует сохранённый вход по подписке

- **КОГДА** разрешённый API-ключ Codex отсутствует, а `~/.codex/auth.json`
  существует как корректный обычный JSON-файл
- **ТОГДА** runtime MUST скопировать файл в приватный временный `CODEX_HOME`
  с правами `0600`
- **И** preflight MUST показывать метод входа из `auth_mode`, не читая значение
  токена в сообщение

#### Scenario: API-ключ не разрешён

- **КОГДА** API-ключ задан в окружении, но его имя не разрешено явно
- **ТОГДА** preflight MUST NOT считать этот ключ доступным runtime
- **И** runtime MUST NOT передать его в subprocess

### Requirement: Настраиваемый бюджет внешней команды

Бюджет одной внешней команды preflight MUST браться из конфигурации проекта и
MUST иметь документированное значение по умолчанию, когда он не задан.

#### Scenario: Бюджет задан конфигурацией

- **КОГДА** конфигурация задаёт положительный `preflight_timeout`
- **ТОГДА** каждая внешняя команда preflight MUST исполняться с этим бюджетом

#### Scenario: Бюджет не задан

- **КОГДА** конфигурация не задаёт `preflight_timeout`
- **ТОГДА** MUST применяться канонический default
- **И** бюджет MUST оставаться положительным (отключить его нельзя)

### Requirement: Готовность delivery

Preflight MUST проверять GitHub CLI, authentication и remote только когда
скомпилированный workflow содержит delivery stage.

#### Scenario: Workflow без delivery

- **КОГДА** pipeline не содержит delivery stage
- **ТОГДА** отсутствие `gh` MUST NOT блокировать run

#### Scenario: Delivery требует GitHub

- **КОГДА** pipeline содержит delivery stage и `gh auth status` неуспешен
- **ТОГДА** readiness MUST быть false до начала run
