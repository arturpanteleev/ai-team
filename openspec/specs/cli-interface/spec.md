## Purpose

Единый безопасный CLI для инициализации, запуска, оценки и наблюдения workflow.
## Requirements
### Requirement: CLI commands and exit codes
CLI MUST предоставлять `init`, `run`, `list`, `eval`, `web`, `version`, `help`; run MUST возвращать 0 для success, 1 для failure/rejection, 2 для BLOCKED и 3 для stopped.

#### Scenario: Unknown command
- **КОГДА** передана неизвестная команда
- **ТОГДА** CLI MUST вывести usage и завершиться ненулевым кодом

### Requirement: Init
`ai-team init` MUST создать `.ai-team/config.yaml`, artifact tasks, reports и
logs directories. В Git repository команда MUST обеспечить ignore
`.ai-team/`, не изменяя workspace по умолчанию.

#### Scenario: Флаги init
- **КОГДА** пользователь передаёт `--target <path>` и/или
  `--write-gitignore` в любом поддерживаемом порядке
- **ТОГДА** CLI MUST применить оба флага
- **И** неизвестный флаг или отсутствующее значение MUST привести к
  ненулевому exit code

#### Scenario: Поведение по умолчанию
- **КОГДА** `init` запускается в Git repository без `--write-gitignore`
- **ТОГДА** команда MUST использовать локальный Git exclude
- **И** MUST NOT изменять `.gitignore`

#### Scenario: Поддерживаемый typed stack
- **КОГДА** target содержит `go.mod`
- **ТОГДА** init MUST записать required `go-test-json` check и required `go vet` check в config

#### Scenario: Stack без typed adapter
- **КОГДА** target содержит Rust, Python или Node manifest, но controller не имеет parser adapter этого stack
- **ТОГДА** init MUST NOT выдавать произвольную command за test evidence
- **И** MUST предупредить, что delivery запрещён до настройки typed required test check

#### Scenario: Неизвестный stack
- **КОГДА** verification profile не определён
- **ТОГДА** init MUST предупредить, что delivery запрещён до настройки required unit/integration/e2e check

### Requirement: Run identity and validation
`run` MUST валидировать новый запуск или resume до запуска агента, получить
workspace lock и использовать одну immutable run_id на весь долговечный run.

#### Scenario: Feature traversal
- **КОГДА** feature содержит slash, backslash или `..`
- **ТОГДА** CLI и direct pipeline API MUST отклонить запрос до записи файлов

#### Scenario: Resume
- **КОГДА** пользователь запускает `ai-team run --resume <run_id>`
- **ТОГДА** CLI MUST восстановить feature, task и next stage
- **И** MUST продолжить ту же run identity после fail-closed проверок

#### Scenario: Повторный run уже доставленной фичи
- **КОГДА** пользователь запускает `run --feature F` без `--retry-from`, и прошлый run той же `F` уже довёл её до успешной deployer delivery (записанный commit и/или PR)
- **ТОГДА** CLI MUST вывести non-blocking предупреждение в stderr с run_id и ссылкой на предыдущую доставку до перезаписи артефактов analyst
- **И** MUST NOT отказать в выполнении нового run из-за одного этого условия

### Requirement: Explicit approvals
CLI MUST создавать и применять typed exact-subject decisions. Обычный
transition approval MUST NOT разрешать delivery side effects.

#### Scenario: Запись решения
- **КОГДА** пользователь передаёт run_id, approval_id, actor, role, action,
  subject hash и optional comment
- **ТОГДА** CLI MUST записать audited decision после строгой проверки

#### Scenario: Resume waiting run
- **КОГДА** waiting run возобновляется
- **ТОГДА** CLI MUST требовать resolved persisted approval
- **И** MUST применить только target выбранного action

### Requirement: Layered agent list
`list` MUST объединять project, plugin, user и built-in registry layers и показывать источник победившего определения.

#### Scenario: Invalid project override
- **КОГДА** project agent definition невалидна
- **ТОГДА** registry MUST вернуть ошибку вместо fallback к built-in agent

#### Scenario: Невалидный, не-shadowing agent definition
- **КОГДА** каталог в non-builtin registry layer содержит невалидный `def.yaml`, и его имя не совпадает ни с одним built-in agent (не shadowing-сценарий)
- **ТОГДА** `list` MUST NOT пропустить его молча
- **И** MUST вывести его имя и ошибку загрузки в stderr

### Requirement: Eval evidence
`eval` MUST поддерживать `--samples` от 1 до 20 и сохранять JSON evidence; LLM quality result MUST быть advisory.

#### Scenario: Несколько samples
- **КОГДА** пользователь передаёт `--samples 3`
- **ТОГДА** результат MUST содержать individual samples, median, mean и standard deviation

