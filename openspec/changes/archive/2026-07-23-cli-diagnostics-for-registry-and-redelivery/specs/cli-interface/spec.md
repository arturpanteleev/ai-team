## MODIFIED Requirements

### Requirement: Layered agent list
`list` MUST объединять project, plugin, user и built-in registry layers и показывать источник победившего определения.

#### Scenario: Invalid project override
- **КОГДА** project agent definition невалидна
- **ТОГДА** registry MUST вернуть ошибку вместо fallback к built-in agent

#### Scenario: Невалидный, не-shadowing agent definition
- **КОГДА** каталог в non-builtin registry layer содержит невалидный `def.yaml`, и его имя не совпадает ни с одним built-in agent (не shadowing-сценарий)
- **ТОГДА** `list` MUST NOT пропустить его молча
- **И** MUST вывести его имя и ошибку загрузки в stderr

### Requirement: Run identity and validation
`run` MUST валидировать feature и config до создания filesystem paths, получить workspace lock, создать immutable run_id и выполнить настроенный pipeline.

#### Scenario: Feature traversal
- **КОГДА** feature содержит slash, backslash или `..`
- **ТОГДА** CLI и direct pipeline API MUST отклонить запрос до записи файлов

#### Scenario: Повторный run уже доставленной фичи
- **КОГДА** пользователь запускает `run --feature F` без `--retry-from`, и прошлый run той же `F` уже довёл её до успешной deployer delivery (записанный commit и/или PR)
- **ТОГДА** CLI MUST вывести non-blocking предупреждение в stderr с run_id и ссылкой на предыдущую доставку до перезаписи артефактов analyst
- **И** MUST NOT отказать в выполнении нового run из-за одного этого условия
