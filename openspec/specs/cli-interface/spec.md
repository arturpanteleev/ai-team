## Purpose

Единый безопасный CLI для инициализации, запуска, оценки и наблюдения workflow.
## Requirements
### Requirement: CLI commands and exit codes
CLI MUST предоставлять `init`, `run`, `list`, `eval`, `web`, `version`, `help`; run MUST возвращать 0 для success, 1 для failure/rejection, 2 для BLOCKED и 3 для stopped.

#### Scenario: Unknown command
- **КОГДА** передана неизвестная команда
- **ТОГДА** CLI MUST вывести usage и завершиться ненулевым кодом

### Requirement: Init
`ai-team init` MUST создать `.ai-team/config.yaml`, artifact tasks, reports и logs directories, а в git repository MUST обеспечить ignore `.ai-team/`.

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
`run` MUST валидировать feature и config до создания filesystem paths, получить workspace lock, создать immutable run_id и выполнить настроенный pipeline.

#### Scenario: Feature traversal
- **КОГДА** feature содержит slash, backslash или `..`
- **ТОГДА** CLI и direct pipeline API MUST отклонить запрос до записи файлов

#### Scenario: Повторный run уже доставленной фичи
- **КОГДА** пользователь запускает `run --feature F` без `--retry-from`, и прошлый run той же `F` уже довёл её до успешной deployer delivery (записанный commit и/или PR)
- **ТОГДА** CLI MUST вывести non-blocking предупреждение в stderr с run_id и ссылкой на предыдущую доставку до перезаписи артефактов analyst
- **И** MUST NOT отказать в выполнении нового run из-за одного этого условия

### Requirement: Explicit approvals
Non-interactive run MUST требовать `--approve-gates` для checkpoints. Внешние
delivery effects MUST требовать `--approve-plan <sha256>`, точно совпадающий с
SHA-256 опубликованного canonical plan.

#### Scenario: Только gates разрешены
- **КОГДА** передан только `--approve-gates`
- **ТОГДА** pipeline MUST остановиться перед commit/push/PR с exit code 3
- **И** MUST вывести canonical plan и его SHA-256

#### Scenario: Разрешён другой plan
- **КОГДА** `--approve-plan` не совпадает с текущим canonical plan
- **ТОГДА** pipeline MUST NOT выполнять commit, push или PR

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

