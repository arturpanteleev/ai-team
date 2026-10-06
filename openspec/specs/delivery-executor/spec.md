# delivery-executor Specification

## Purpose
Controller-owned delivery: canonical plan, подтверждение exact SHA-256, семь предусловий, exact-file commit/push/PR и post-commit recovery.
## Requirements
### Requirement: Validated delivery plan
Delivery MUST consume a structured plan containing branch, base commit, exact
file set, file SHA-256/modes, verification evidence, preconditions, commit
message and PR metadata.

#### Scenario: Unrelated dirty file
- **WHEN** a dirty file is not listed in the validated delivery plan
- **THEN** it MUST NOT be staged or committed

#### Scenario: Git transforms staged bytes
- **WHEN** attributes, filters or line-ending normalization make staged blobs differ from approved bytes
- **THEN** the executor MUST reject delivery before commit

### Requirement: Protected branch safety
The delivery executor MUST determine and reject the repository default or protected branch before push.

#### Scenario: Detached or default branch
- **WHEN** delivery starts from a detached HEAD or default branch without an approved feature branch transition
- **THEN** push MUST be denied

### Requirement: Idempotent delivery
Delivery MUST persist step results and safely resume after partial failure.

#### Scenario: Push succeeds and PR creation fails
- **WHEN** a retry occurs after the remote branch already exists
- **THEN** the executor MUST reuse the verified commit and retry only PR creation

#### Scenario: Crash after commit before state persistence
- **WHEN** branch HEAD advanced after the exact approved commit but commit identity was not persisted
- **THEN** the executor MUST re-verify commit message, parent, paths, modes and blob hashes before recovery
- **AND** it MUST NOT create a duplicate commit

### Requirement: Delivery только из candidate

Planner и executor MUST строить, проверять, коммитить и отправлять exact
candidate worktree.

#### Scenario: Успешная delivery

- **КОГДА** plan hash одобрен и candidate identity неизменна
- **ТОГДА** executor MUST commit/push candidate branch
- **И** MUST NOT переключать live checkout


### Requirement: Ограниченная по времени и отменяемая доставка

Post-terminal доставка MUST исполняться с настраиваемым положительным бюджетом времени и MUST оставаться отменяемой контекстом процесса.

#### Scenario: Бюджет задан конфигурацией

- **КОГДА** конфигурация задаёт положительный `delivery_timeout`
- **ТОГДА** доставка MUST исполняться с этим бюджетом
- **И** превышение MUST давать ошибку, называющую исчерпанный бюджет

#### Scenario: Бюджет не задан

- **КОГДА** конфигурация не задаёт `delivery_timeout`
- **ТОГДА** MUST применяться канонический default
- **И** бюджет MUST оставаться положительным (отключить его нельзя)

#### Scenario: Бюджет run'а исчерпан

- **КОГДА** wall-time бюджет run'а исчерпан к моменту post-terminal доставки
- **ТОГДА** доставка MUST NOT отменяться из-за этого: она уже одобрена человеком и исполняется после terminal finalize

#### Scenario: Процесс получил сигнал завершения

- **КОГДА** контроллер получает SIGINT или SIGTERM во время commit/push/PR
- **ТОГДА** доставка MUST прерваться
- **И** ошибка MUST называть отмену извне, а не выглядеть сбоем шага

### Requirement: Неинтерактивные внешние команды доставки

Внешние команды доставки MUST исполняться без интерактивного запроса креденшелов.

#### Scenario: git просит пароль

- **КОГДА** `git push` пытается запросить креденшелы
- **ТОГДА** окружение команды MUST запрещать терминальный запрос
- **И** унаследованные askpass-хуки MUST NOT передаваться ребёнку
