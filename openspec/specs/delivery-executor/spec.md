# delivery-executor Specification

## Purpose
Controller-owned delivery: canonical plan, подтверждение exact SHA-256, семь предусловий, exact-file commit/push/PR и post-commit recovery.
## Requirements
### Requirement: Validated delivery plan
Delivery MUST consume a structured plan containing branch, base commit, remote
name and push URL, exact file set, file SHA-256/modes, verification evidence,
preconditions, commit message and PR metadata. The push URL MUST be included in
the canonical plan hash and MUST still match the configured remote when
delivery begins. Embedded credentials and credential-bearing URL parameters
MUST be rejected so plans and delivery evidence never persist secrets.

#### Scenario: Unrelated dirty file
- **WHEN** a dirty file is not listed in the validated delivery plan
- **THEN** it MUST NOT be staged or committed

#### Scenario: Git transforms staged bytes
- **WHEN** attributes, filters or line-ending normalization make staged blobs differ from approved bytes
- **THEN** the executor MUST reject delivery before commit

#### Scenario: Remote URL changes after approval
- **WHEN** the configured push URL differs from the URL in the approved plan
- **THEN** the executor MUST reject delivery before git side effects

#### Scenario: Remote URL contains credentials
- **WHEN** the configured remote URL contains embedded credentials or credential-bearing query parameters
- **THEN** the planner or executor MUST reject it without writing the credential value to plan or delivery evidence

#### Scenario: Repository hooks or fsmonitor are configured
- **WHEN** the executor runs any Git command
- **THEN** it MUST disable repository hooks, fsmonitor and commit signing for that invocation

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

#### Scenario: Commit identity in saved state is tampered
- **WHEN** a resumed delivery has a saved commit SHA, regardless of `commit_verified`
- **THEN** the executor MUST re-verify commit message, parent, paths, modes and blob hashes against the approved plan before push

### Requirement: Повторная доставка после возврата из ревью

Планировщик MUST принять ту же ветку `ai-team/<feature>`, когда задача вернулась
на реализацию (`code_review` → `implementation`) после завершённой доставки,
с одним условием: ветка целиком состоит из commits, которые контроллер сам
записал и проверил. Один state-файл в рабочем дереве доверия не заслуживает,
поэтому планировщик MUST: (1) принимать только delivery state (текущий и
архивные `*.completed.json`) с валидным HMAC-SHA256, ключ которого лежит вне
рабочего дерева (`$AI_TEAM_STATE_KEY_DIR`, иначе `<user config dir>/ai-team`);
(2) требовать, чтобы каждая запись была завершённой доставкой (commit, push,
PR), а tip записи равнялся HEAD; (3) пройти цепочку записей назад по baseline
каждого plan и заново проверить каждый commit против его сохранённого plan
(сообщение, parent, пути, режимы, blob-хеши); (4) убедиться, что
`git rev-list <baseline первой записи>..HEAD` состоит ровно из commits цепочки,
линейно. Любая неоднозначность MUST быть отказом. Проверка MUST NOT создавать
каталоги. Если цепочка принадлежит контроллеру, но base ушла вперёд (base не
предок начала цепочки), планировщик MUST отказать отдельным сообщением,
требующим rebase, а не сообщением о чужом commit. Baseline
нового плана MUST быть этим tip, новый commit MUST встать поверх него (parent
равен baseline плана), push MUST быть fast-forward без force, а существующий
открытый PR MUST быть переиспользован и проверен по новому head. Инвариант
доставки не меняется: commit, push и PR выполняет только контроллер после
проверок и подтверждения точного SHA-256 нового canonical plan.

#### Scenario: Второй проход поверх доставленного commit
- **WHEN** после доставки реализация изменила approved-файлы и план строится заново
- **THEN** `baseline_head` плана MUST равняться tip ветки, а base-ветка остаться прежней
- **AND** новый commit MUST иметь прежний tip родителем, а PR MUST остаться тем же

#### Scenario: Чужой commit на ветке
- **WHEN** tip ветки не записан контроллером как проверенная доставка
- **THEN** планировщик MUST отклонить план и требовать чистую ветку от base

#### Scenario: Подделанный state
- **WHEN** агент сделал commit и записал delivery state без валидной подписи контроллера (или чужим ключом)
- **THEN** планировщик MUST отклонить повторную доставку

#### Scenario: Base ушла вперёд
- **WHEN** ветка состоит только из доставленных контроллером commits, но base получила новые commits
- **THEN** планировщик MUST потребовать rebase ветки на base

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
