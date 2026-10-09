## Purpose

Единый безопасный CLI для инициализации, запуска, оценки и наблюдения workflow.
## Requirements
### Requirement: CLI commands and exit codes
CLI MUST предоставлять `init`, `run`, `runs`, `show`, `cancel`, `list`, `eval`, `web`, `db backup`, `db restore`, `version`, `help`; run MUST возвращать 0 для success, 1 для failure/rejection, 2 для BLOCKED и 3 для stopped.

#### Scenario: Unknown command
- **КОГДА** передана неизвестная команда
- **ТОГДА** CLI MUST вывести usage и завершиться ненулевым кодом

#### Scenario: Terminal error printed once
- **КОГДА** run завершается терминальной ошибкой (BLOCKED, негативный вердикт,
  отказ resume, отмена, грязный workspace)
- **ТОГДА** CLI MUST напечатать человеческую строку `✗ Пайплайн остановлен: …`
  в stderr ровно один раз, включая режим `--quiet`
- **И** machine-readable record о той же ошибке MUST печататься только в режиме
  `--json` и только в stdout
- **И** exit-код MUST остаться прежним

#### Scenario: Остановка печатает следующее действие
- **КОГДА** run останавливается в состоянии ожидания human input, approval или resumable checkpoint
- **ТОГДА** CLI MUST напечатать готовую команду для следующего действия, включая run identity и target
- **И** machine-readable record в `--json` MUST включить `run_id` и `next_action`

#### Scenario: JSON для команд задач
- **КОГДА** пользователь вызывает `runs`, `show` или `cancel` с `--json`
- **ТОГДА** CLI MUST печатать только JSON records на stdout, а человеко-читаемый прогресс направлять в stderr

### Requirement: Task run inspection
`runs` MUST показывать сохранённые lifecycle runs проекта, а `show` MUST проверять соответствие lifecycle identity immutable run evidence перед показом этапов, попыток и ожидания.

#### Scenario: Список задач
- **КОГДА** пользователь вызывает `ai-team runs --target <path>`
- **ТОГДА** CLI MUST перечислить lifecycle runs проекта с feature, phase, next stage и временем обновления

#### Scenario: Подробности прогона
- **КОГДА** пользователь вызывает `ai-team show <run_id>`
- **ТОГДА** CLI MUST проверить manifest, event chain и attempt manifests до показа истории попыток
- **И** MUST показать токены и приблизительную долю месячной подписки только при достаточных данных
- **И** MUST NOT показывать точную цену API

### Requirement: Task run cancellation
`cancel` MUST завершить только non-terminal run и сохранить непустую пользовательскую причину в событии `run_canceled`.

#### Scenario: Отмена run
- **КОГДА** пользователь вызывает `ai-team cancel <run_id> --reason <text>` для незавершённого run
- **ТОГДА** CLI MUST записать причину в verified event journal и перевести lifecycle в terminal
- **И** уже terminal run MUST быть отклонён

### Requirement: Human stage take command
CLI MUST предоставлять `ai-team stage take <run_id> --stage <id>` для текущего
ожидающего human input этапа через защищённый web controller. Actor и role MUST
определяться серверной сессией и approval policy; CLI MUST NOT принимать их как
параметры этой команды. Клиент MUST получить web session/CSRF token через
`/api/session`, отправить их с запросом взятия этапа и отклонить удалённый HTTP
URL до передачи bearer token.

#### Scenario: Локальный участник берёт текущий этап
- **КОГДА** пользователь вызывает `stage take` для текущего ожидающего human input
- **ТОГДА** CLI MUST использовать `.ai-team/web.token`, получить сессию и вывести
  server-derived владельца этапа
- **И** повторное взятие MUST показать актуального владельца без локального
  создания или подмены ownership записи

#### Scenario: Облачный участник берёт этап
- **КОГДА** CLI получает HTTPS URL и bearer token из окружения
- **ТОГДА** CLI MUST отправить защищённый запрос с CSRF token и cookie сессии
- **И** actor и role MUST быть получены только из ответа controller

#### Scenario: Недоступный или неразрешённый этап
- **КОГДА** web controller отклоняет аутентификацию, роль или состояние этапа
- **ТОГДА** CLI MUST завершиться ненулевым кодом и MUST NOT сообщать об успешном
  взятии

### Requirement: Init
`ai-team init` MUST создать `.ai-team/config.yaml`, artifact tasks, reports и
logs directories. В Git repository команда MUST обеспечить ignore
`.ai-team/`, не изменяя workspace по умолчанию.

#### Scenario: Флаги init
- **КОГДА** пользователь передаёт `--target <path>` и/или
  `--write-gitignore` и/или `--force` в любом поддерживаемом порядке
- **ТОГДА** CLI MUST применить оба флага
- **И** неизвестный флаг или отсутствующее значение MUST привести к
  ненулевому exit code

#### Scenario: Явная перезапись v5-конфига
- **КОГДА** существует `.ai-team/config.yaml` и пользователь запускает `init --force`
- **ТОГДА** CLI MUST атомарно заменить regular config выбранным v5-шаблоном
- **И** leaf symlink или special file MUST быть отклонён

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

#### Scenario: Активный шаблон
- **КОГДА** пользователь запускает run с `--template <id>`
- **ТОГДА** CLI MUST принять только id единственного активного шаблона проекта и закрепить его версию за run
- **И** неизвестный id MUST быть отклонён до запуска агентов

#### Scenario: Автоматический режим
- **КОГДА** пользователь запускает графовый run с `--mode auto`
- **ТОГДА** CLI MUST закрепить отдельную неизменяемую версию шаблона, где этапы с агентом исполняются агентом и подтверждения переходят автоматически
- **И** MUST NOT изменять активную версию шаблона проекта
- **И** resume MUST продолжить по закреплённой версии

#### Scenario: Повторный run уже доставленной фичи
- **КОГДА** пользователь запускает `run --feature F`, и прошлый run той же `F` уже довёл её до успешной delivery (записанный commit и/или PR), независимо от того, как названа delivery-стадия в конфиге
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

#### Scenario: Флаги list
- **КОГДА** пользователь передаёт `--target <path>`
- **ТОГДА** `list` MUST показать layered registry именно этого каталога,
  а не текущей рабочей директории
- **И** неизвестный флаг, отсутствующее значение или лишний позиционный
  аргумент MUST привести к ненулевому exit code

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
