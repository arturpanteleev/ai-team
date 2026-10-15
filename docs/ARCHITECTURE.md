# Архитектура ai-team

Этот документ — глубокое техническое описание внутреннего устройства
контроллера. Если вы ищете, как установить и запустить ai-team, начните с
[README.md](../README.md); этот файл предполагает, что вы уже знаете базовый
конвейер (`analyst → architect → coder → reviewer → tester → verifier →
deployer`) и термины из [глоссария](start/concepts.md#глоссарий).

## Карта пакетов

```text
cmd/ai-team/        точка входа CLI
pkg/
├── config/         конфигурация, строгая валидация, layered registry lookup
├── agent/          Agent struct + Registry (резолюция definition-слоёв)
├── runtime/         Runtime interface + AgentCLI (prompt, логи, model/effort)
├── pipeline/        оркестрация, enforcement вердиктов, гейты, loopback
├── workflow/        доменные state/outcome типы и чистые переходы; контракт гейта
├── stagestatus/     чистая проекция статусов этапов и истории посещений из durable-журнала
├── gatelib/         версионированная библиотека определений гейтов (реестр)
├── gatephrases/     логика гейта required_phrases (явные параметры, RE2 только по флагу)
├── gatesched/       планировщик групп гейтов: параллельные гейты, журнал, recovery
├── gaterun/         раннер гейта внешней программы: JSON-протокол, запуск без shell, evidence; Docker-адаптер (docker_program)
├── dockerrun/       запуск программы в контейнере через docker CLI: strict defaults, удаление по имени
├── llmgate/         контракт LLM-гейта: схема вердикта, попытки, error вместо pass
├── humandecision/   единый сервис решения человека: роли, точный хеш, самоподтверждение, аудит
├── verdict/         verdict-контракт: парсер маркеров, BLOCKED-протокол
├── checks/          deterministic verification runner и evidence
├── scope/           repository-relative mutation path policy (glob-матчинг)
├── delivery/        строгий canonical plan и controller-owned executor
├── evidence/        immutable run/attempt manifests, append-only events
├── safeio/          no-follow filesystem primitives (symlink rejection)
├── process/         process-group supervision и kill (Unix/Windows/plan9)
├── eval/             независимая LLM-оценка артефактов
├── notifier/         уведомления: консоль, webhook, Slack, e-mail
├── tracker/          абстрактный Tracker, адаптеры jira/fake, планирование исходящих операций
├── cicd/             controller-owned CI/CD deploy adapters (TeamCity)
├── report/           HTML-отчёты по завершённым run
├── ui/               консольный вывод (цвет, progress bar)
└── web/              HTTP API + SQLite store + StoreRecorder (дашборд)
agents/{name}/       встроенные агенты (def.yaml + prompt.md)
web/                 React-фронтенд дашборда (отдельный npm-проект)
e2etest/             mock-opencode.sh + subprocess-level E2E-тесты
openspec/            OpenSpec change history (specs/ + changes/)
```

Контроллер владеет переходами, исходами, checks и delivery-эффектами. LLM
создаёт смысловые артефакты и verdict markers, но не исполняет команды
напрямую — это базовое разделение ответственности проходит через все разделы
ниже.

## Детерминированный контроль

- Required checks запускаются массивом argv без shell, с confined working dir,
  timeout, bounded stdout/stderr, tool path/version, exit code и timestamps.
- Падение required check переопределяет положительный LLM verdict. Optional
  отсутствующий tool виден как skipped/warning и не маскируется под pass.
- Mutation policy задаётся в definition агента (см. `pkg/scope` — glob-и
  `*`/`?`/`**` с запретом выхода за пределы workspace). Baseline фиксируется
  перед попыткой. Новый Git-run требует чистого tracked/untracked workspace;
  ignored и существующие файлы считаются пользовательскими и не могут быть
  присвоены агенту. Вне Git используется полный hash snapshot, поэтому guard
  не пропускается.
- Read-only этап с любой source mutation падает. `require_diff` без
  фактической delta также падает. Delivery получает только нормализованные
  разрешённые пути.
- Checkpoints имеют явные политики `auto_continue`, `interactive` и
  `require_explicit`. В non-interactive режиме нет неявного согласия.
- Перед review controller публикует candidate workspace digest, changed paths,
  file fingerprints и tracked patch SHA-256. Tester только пишет tests; их
  фактический typed execution добавляется в финальный candidate evidence для
  verifier.
- Для Git-проекта controller создаёт отдельный detached worktree от exact
  baseline commit. AI-этапы, checks, review и delivery работают только внутри
  этого candidate; ветка, HEAD и source-файлы live checkout не меняются.
  Metadata с base commit/tree и workspace hash хранится в control target и
  проверяется при resume. Human approval содержит exact candidate hash:
  решение становится stale после любой последующей mutation.
- Обратное ребро graph создаёт новые attempt IDs и инвалидирует прежнюю
  downstream-ветку; invalidated/skipped/warning не отображаются как passed
  или failed. Каждое non-terminal ребро требует approval — негативный
  вердикт без rejected-ребра останавливает run (fail-closed).

## Workflow graph v4

Schema v4 разделяет определения узлов (`pipeline`) и маршрут (`workflow`).
Compiled graph содержит entry, уникальные `(from, outcome)` edges, terminal
targets, edge approval policies и `max_visits`. Каждый non-terminal edge
требует human roles/quorum/actions; resolved action выбирает exact target, а
не просто разрешает увеличение индекса.

Compiler до run проверяет references, достижимость, terminal path и циклы.
Каждый узел цикла обязан иметь положительный `max_visits`; счётчик включает
все реальные AI calls и восстанавливается из immutable attempts при resume.
Выбранное ребро записывается событием `transition_selected` в evidence и
SQLite/WebSocket projection, а lifecycle сохраняет exact следующий узел.
Resolved `workflow.json` содержит фактически исполненный graph; web detail
читает этот immutable snapshot и показывает текущую позицию и policies.

## Статусы этапов и история посещений (S11)

`pkg/stagestatus` — одна **чистая** функция `Project(Input) Projection`: из
журнала событий, согласований, checkpoint жизненного цикла и закреплённого
шаблона она выводит, где находится каждый этап. Ничего не хранится и не
редактируется: проекция пересчитывается на каждом чтении, после перезапуска даёт
тот же ответ, не меняет вход, а журнал без `transition_selected` (старые run)
тоже читается — посещения тогда выводятся из попыток. `pkg/web`
(`stage_status_api.go`) только собирает вход из SQLite-ленты задачи, approval
store и lifecycle и отдаёт результат в `GET /api/runs/{id}` (`stage_statuses`),
в карточках `GET /api/runs` (`stage_status`, только для запрошенной страницы),
в `GET /api/runs/{id}/stage-statuses` и MCP `get_task_stage_statuses`. Записи
статуса нет ни в одном API; те же события, пропущенные через verified
`events.jsonl`, дают те же статусы (проверяется в сквозном тесте).

Модель разделяет то, что раньше смешивал один «running»:

| Измерение | Значения | Откуда |
|---|---|---|
| `readiness` | `not_reached`, `ready`, `engaged`, `left` | есть ли открытое посещение и началась ли работа |
| `phase` | `none`, `working`, `checking`, `awaiting_human` | открытая попытка; гейты (`gate_attempt_*`) и проверки после попытки; ожидающее approval |
| `executor` | `human`, `internal_agent`, `external_agent` + `source` | `attempt_finished.executor*` (факт), шаблон/`adapter` (по умолчанию), `executor_changed` / lifecycle-override (выбор человека) |
| `outcome` | исход попытки (`passed`, `rejected`, …; `pending`) | `attempt_finished`, `attempts_invalidated` |
| посещения | `visits[]`, `visited`, `visit_count` | `transition_selected` (вход/выход), `attempt_*`, `approval_*`, `stage_taken` |

`status` — их свёртка. Посещение — это один вход в этап: `transition_selected`
закрывает посещение `from` и открывает посещение `target`; переход на ту же
стадию (`stay`) нового посещения не создаёт, возврат (индекс назад или действие
`return_to_*`) помечает новое посещение `entered_via: return`. Закрытое
посещение остаётся неактивным навсегда вместе с попытками (инвалидированные
помечаются `superseded`), поэтому статус вернувшегося этапа — это статус его
**открытого** посещения, а прежняя попытка не делает этап «завершённым»; этап,
откуда вернулись, получает `not_reached` с `visited: true`. Незавершённая
попытка при `interrupted`/`stopped` — это `ready` с `blocked_by: run_state`, а не
«в работе» (в ленте SQLite нет `attempt_abandoned`, проекция берёт это из статуса
запуска, поэтому ленточная и evidence-проекции равны); посещение без попыток,
закрытое переходом, — `passed` (вперёд) или `rejected` (возврат), а закрытое
провалом задачи — `failed`. Ожидание человека
выводится из pending approval этапа (`from_stage`): ввод (`human_input`) — это
готовность (`ready`, а после `stage_taken` — работа человека), подтверждение,
решение по проверке, вопросы grill и план доставки — `awaiting_human` с
`blocked_by` (вид, approval, роли, действия, провалившиеся обязательные
гейты). Подтверждение результата — решение о переходе, поэтому checkpoint и
action-маршруты уже указывают на следующий этап (`action_stage_id`), а ждущим
остаётся подтверждаемый.

Журнал approval store перекрывает ленту: решение, принятое до resume, ещё не
записано как `approval_decided`, но approval уже `resolved`. UI (карточка,
маршрут, таймлайн) показывает проекцию одним набором слов
(`web/src/stageStatus.ts`); красный — только отказ/ошибка/обход, янтарный —
рекомендации гейтов.

## Гейты: контракт и библиотека

Гейт проверяет одну неизменяемую версию результата этапа (по хешу) и возвращает
исход с доказательствами; следующий этап выбирает только контроллер по
конфигу. Модель разведена на три сущности, чтобы не смешивать «что это»,
«как этап это использует» и «что произошло»:

- **`workflow.GateDefinition`** — ID, точная версия, вид (`builtin`, `llm`,
  `human`, `external`, `docker`), схема параметров, применимость к типу
  результата. Живёт в Go-реестре `pkg/gatelib` (`Default()`), меняется через
  PR, неизменяема после публикации: изменение — новая версия, старые версии
  остаются. Каталог читается через `GET /api/gates/catalog`, записи нет.
- **`workflow.GateRef`** — ссылка этапа: `gate`, точная `version` (без
  «latest»), `params`, `name`, `mode` (`required|advisory`). Цепочка этапа —
  `exit_gate_groups` (`GateGroups`): группы последовательны, внутри группы
  гейты независимы. Валидация (`gatelib.Registry.ResolveGroups`) отклоняет
  неизвестные ID/версии/параметры до запуска.
- **`workflow.GateAttempt`** — попытка на конкретном хеше результата с раздельными
  `Execution` (`pending|running|succeeded|error|timeout|canceled`) и `Verdict`
  (`pass|fail|not_applicable`, только при `succeeded`), идентификацией (задача,
  этап, посещение, хеш, определение, `attempt_id`, группа, время, исполнитель)
  и ссылками на очищенные evidence. `error`/`timeout` — технические сбои,
  допускающие повтор того же гейта на том же хеше; `Blocking()` кодирует
  правило required/advisory.

Схема конфига v6 заменяет разрозненные `required_sections`, `check` и `confirm`
этапа одним `exit_gate_groups`; v5 грузится совместимым загрузчиком
(`TemplateStage.LegacyGateGroups`, `Config.MigrateToCurrent`). Шаблон закрепляет
версии определений своим YAML (`Config.GatePins` даёт `id`, версию и SHA-256
определения), поэтому новая версия библиотеки не меняет идущие задачи.

### Рекомендации, повторы и обход (S06)

Три вещи поверх планировщика, и ни одна не переписывает исход попытки:

- **Рекомендательные гейты.** `GateAttempt.Blocking()` для `advisory` всегда
  `false`; `Result.Warnings` собирает рекомендации, которые не прошли (`fail`,
  `error`, `timeout`, `canceled`). `pipeline.waveReport` не даёт такой попытке
  изменить статус проверки (правило получает статус `advisory_failed|error`),
  `check_passed` несёт `warnings`, события попыток — `mode` и
  `advisory_warning`.
- **Технический повтор.** `check_decision` предлагает `retry_gate` (в payload —
  `retryable_gates` и `artifact_sha256`), когда у обязательного гейта последняя
  попытка `error`/`timeout`. `control.RetryStageGates` разрешает решение с
  именами гейтов; `pipeline.prepareGateRetry` проверяет, что результат тот же,
  и пишет `gate_retry_requested` (привязка к хешу и approval), после чего цепочка
  идёт заново с `Wave.Retry`: пересчитывается только названный гейт,
  успешные гейты группы и сам этап не повторяются. Эффективная проверка
  версии — чистая функция `foldStageCheck`: запрос повтора отменяет проверку до
  него, пока цепочка не отработала снова; запрос, записанный до падения,
  применяется при следующем возобновлении. Каждый повтор, закончившийся новым
  сбоем, поднимает своё решение (раунд входит в ID approval), а отчёт раунда
  пишется рядом (`<stage>-retry-N.md`), потому что файлы контроллера
  неизменяемы.
- **Админский обход.** Системная роль `admin` (`cloudidentity.RoleAdmin`,
  permission `gate:override`) — не функция процесса и не следует из
  `product_owner`; `FunctionRole("admin")` не сопоставляется ни одной роли,
  поэтому шаблон не может назвать её функцией этапа. Проверка — на сервере
  (`requireGateOverrideRight`, 403 раньше любой другой проверки; общая точка
  входа решений для `override` закрыта, 409). `control.OverrideStageCheck`
  записывает в решение `authority: system_admin`; `applyCheckOverride` пишет
  неизменяемое `check_overridden` с хешем результата, посещением и точными
  блокирующими попытками (`gatesched.OverriddenGates`), и отказывается, если
  результат изменился после проверки. Обход, у которого нет хеша
  (записанный до S06), ничего не покрывает.

`gatesched.Resolve` — чистая проекция «можно ли перейти и почему» над попытками
одной версии и её обходом (`passed`, `passed_with_warnings`, `overridden`,
`blocked`, `error`, `waiting`, `incomplete`); ею пользуются
`GET /api/runs/{id}/gates` и MCP `get_task_gates`. Состояние `overridden`
разрешает переход, но `GateStatus.Attempt` остаётся тем, чем был (`fail` —
красный во всех представлениях). Схема SQLite 17 выдаёт `admin` владельцу
команды (`BootstrapTeamAdmin` делает это для новой команды; миграция — для
существующей, не затрагивая остальных PO).

### Планировщик групп гейтов

`pkg/gatesched` исполняет цепочку `exit_gate_groups` одного результата этапа и
ничего не знает об этапах, агентах и файлах evidence: виды гейтов исполняют
`Runner`-ы, которые подставляет вызывающий, а персистентность идёт через порт
`Journal`. Группы идут последовательно, гейты группы — параллельно (предел
`gate_concurrency`, по умолчанию 4). После отказа обязательного гейта следующие
группы не стартуют, но уже запущенные гейты группы доводятся до исхода.

```text
Wave{Key{task, stage, visit, scope=попытка этапа, sha256 результата}, Groups, Definitions}
  └─ Freezer.Freeze → Lease.Verify до волны, после каждого гейта и в конце
  └─ Journal.Load → восстановление: running без исхода → canceled(прервана)
  └─ для каждой группы: needsRun(гейт) → Runner.Run (≤ MaxParallel) → Record
```

- **Хеш и заморозка.** Волна привязана к SHA-256 объявленных выходов попытки.
  `Freezer` не может поставить ОС-блокировку на чужие файлы, поэтому заморозка
  детективная и fail-closed: расхождение хеша отменяет волну
  (`gate_wave_aborted`, `ErrResultChanged`) и ни один исход не засчитывается.
  Исходы читаются только для точных stage/visit/scope/hash, поэтому успех
  старой версии не засчитывается новой: новая версия исполняет все группы с
  первой.
- **Durable-журнал.** `EventJournal` пишет `gate_attempt_started` до вызова
  гейта, `gate_attempt_waiting` (ожидание ответа, та же попытка) и
  `gate_attempt_finished` в hash-chained `events.jsonl`; ID попытки
  детерминирован (`gate-<stage>-v<visit>-<sha12>-<gate>-<n>`), терминальная
  запись финальна, повторно доставленная ранняя запись её не открывает.
  `gatesched.AllAttempts` сворачивает журнал в историю попыток со временем и
  исходом. При старте задачи пишется `gate_pins_recorded` (ID, версия, вид и
  digest закреплённых определений), при возобновлении digest сверяется.
- **Recovery.** Завершённый гейт не вызывается повторно, технический сбой
  (`error`/`timeout`) не повторяется неявно (повтор — `Wave.Retry`, см.
  «Рекомендации, повторы и обход» ниже: новая попытка, прежняя остаётся), прерванная попытка закрывается как
  `canceled`, ожидающая ответа продолжается. Паника гейта — это падение
  контроллера, а не исход: она поднимается в вызывающую горутину после того,
  как остальные гейты группы закончили.

`pkg/pipeline/gate_wave.go` — адаптер к исполнителю этапа: кодовые гейты
исполняет `stagecheck.RunHard` по одному правилу на гейт (один запрос PR на
волну, общая закреплённая идентичность PR), LLM-гейты — прежний проверяющий
агент (по одному за раз) или раунды grill с ожиданием ответа; итог волны
проецируется на прежние `check_*`-события, отчёт и запрос `check_decision`
(`check_*` теперь несут хеш результата, а записанная проверка другого хеша
игнорируется). Вердикт LLM-гейта разбирает `pkg/llmgate` (схема `verdict_marker`
или `json`, число попыток, ответ вне схемы и отказ рантайма — `error`; см.
`gate_llm.go`). `human_approval` — это подтверждение перехода этапа, поэтому он
допускается только в последней группе и в автоматической части волны не
участвует: человека спрашивают после её окончания, а ожидание и решение
записываются в ту же историю попыток гейта (`human_gate.go`; решение, принятое
пока процесс остановлен, фиксируется при возобновлении). Решение принимает один
сервис `pkg/humandecision` (роли, точный хеш, самоподтверждение, аудит), общий
для Web, REST и MCP. Заголовок `Idempotency-Key` (`RequestKey`; для MCP — `idempotency_key`) пишется в
аудит этого сервиса и им не дедуплицируется (дедупликацию делает слой ключей запроса REST/MCP): повтор распознаётся по (актор, действие, хеш,
роль) и отвечает только в роли, которую актор по-прежнему держит. Для кода, которому нужно «что
проверяет этап», есть производные методы `TemplateStage`: `SectionsRequired`,
`CodeRules`, `AgentChecks`, `GrillCheck`, `NeedsConfirmation`; проекции v6 на
поля v5 больше нет.

## Evidence и наблюдаемость

Live-артефакты находятся в `.ai-team/artifacts/{feature}/`, но доказательством
конкретного запуска служит immutable layout:

```text
.ai-team/runs/{run_id}/
├── run.json
├── config.json
├── workflow.json
├── events.jsonl
├── attempts/{attempt_id}/
│   ├── manifest.json
│   └── artifacts/...
├── logs/...
└── reports/...
```

Run manifest schema v7 связывает exact config/workflow snapshots, SHA-256
исполняемого controller и Go/VCS identity. Attempt manifest содержит
execution/decision/outcome, blocker, SHA-256/provenance артефактов, checks,
mutations и delivery result. Публикация attempt атомарна, events связаны
SHA-256 hash chain и проверяются перед каждым append (splice/reorder другой
цепочки событий обнаруживается при replay). Корень цепочки выводится из
`run_id` прогона, а не общий для всех прогонов: поэтому целый валидный
`events.jsonl` другого прогона отвергается даже если в его событиях
перештамповать `run_id` и пересчитать все дайджесты — принадлежность лога
своему прогону является частью цепочки, а не отдельной проверкой поля. Typed replay проверяет порядок
lifecycle transitions, terminal status, invalidations и exact digest каждого
опубликованного attempt manifest.

Изменяемый execution checkpoint хранится отдельно в
`.ai-team/state/runs/<run_id>.json`. `RunEngine` атомарно обновляет его на
 границах stages и может после restart открыть проверенную evidence chain для
append. Resume сохраняет исходный `run_id`, добавляет `run_resumed` и
продолжает ordinal sequence попыток; terminal state возобновить нельзя.

Resume перед продолжением выполняет fail-closed проверку применимой evidence
(OPS-3): `evidence.VerifyResumeEvidence` сверяет manifest identity/schema,
digest config/workflow snapshot'ов против manifest, целостность event hash
chain и digest attempt manifest'ов по replayed chain. Любое расхождение
отклоняет resume со структурированной причиной (`ResumeEvidenceError` +
`ErrResumeEvidence`), а при аппендабельном логе фиксирует событие
`resume_blocked` с полем `reason`, — чтобы причина отказа осталась в
tamper-evident evidence, а не только в стд-ошибке.


Человеческое решение о переходе — отдельная typed сущность в
`.ai-team/state/approvals/<run_id>/<approval_id>.json`. Она привязана к
точному subject hash, attempt, исходному и целевому этапу, набору actions,
ролям и quorum. Delivery plan использует тот же примитив: pending approval
с `trigger: "delivery_plan"`, subject = SHA-256 canonical plan и ролью
`release_manager`; canonical JSON плана хранится в payload approval для
осознанного решения из web. Non-TTY запуск атомарно переходит в `waiting`,
а CLI `decision` записывает actor/role/action/comment. Только resolved
approval разрешает `resume`; stale или конфликтующее решение не меняет
evidence. Решения сериализуются in-process mutex и flock на каталоге run,
поэтому конкурентные CLI/web-решения не теряются. События
`approval_requested` и `approval_decided` входят и в immutable hash chain,
и в SQLite/WebSocket projection.

### Выкладка через CI/CD

`pkg/cicd` задаёт интерфейс `Provider` (trigger, status, cancel) и адаптер
TeamCity REST с bearer-токеном из окружения контроллера. Этап с агентом
`kind: deploy` (встроенный `cicd-deploy`) исполняется контроллером без
runtime модели: из controller receipt PR-этапа берутся URL и голова PR,
контроллер ждёт мержа (`gh pr view`, `headRefOid` = опубликованный коммит).
Вершину ветки сообщает GitHub API для репозитория, зафиксированного при старте
run; проверка идёт во временном bare-репозитории, который скачивает ветку и
`refs/pull/<N>/head` по URL этого репозитория с явным credential helper `gh`. Git доказывает,
что merge commit входит в вершину и содержит изменения PR (предок или
совпадающий diff без textconv для squash). Выкладывается вершина ветки, а не
голова PR, чтобы не откатить смёрженное позже. Этап имеет собственный бюджет
`merge_timeout + timeout + 5m` вместо `stage_timeout`. Ожидание сборки идёт с таймаутом,
отменой и повтором временных ошибок опроса (часы инжектируются).
Build ID сохраняется в `.ai-team/state/deploy/`, поэтому повтор не ставит
вторую сборку; при повторе сборка всегда перечитывается у провайдера. Каждый
запуск несёт случайный `env.AI_TEAM_DEPLOY_ID`, записанный в квитанцию до
запроса: при потерянном ответе повтор ищет сборку по нему
(`TriggeredBuildFinder`) и принимает только единственную сборку настроенной
build configuration с той же ревизией; иначе нужен оператор.
Ссылка на сборку — обычный output попытки; сбой проецируется в поток задачи
как `integration_error`. Провайдер передаётся в pipeline только опцией локального контроллера, поэтому sandboxed worker не получает токен.

### Интеграция с трекером задач

`pkg/tracker` определяет абстрактный интерфейс `Tracker` (проверка формата ключа,
получение карточки, допустимые переходы, идемпотентный переход, комментарий с
ключом идемпотентности, чтение изменений с курсором для будущей входящей
синхронизации) и реестр адаптеров по имени из конфига (`jira`, `fake`).
Чистая функция `tracker.Plan` превращает событие задачи в операции:
`attempt_started` → переход `on_enter`; `attempt_finished` → переход `on_pass`
только для успешной попытки и всегда комментарий с честным исходом
(`Outcome` — расширяемый тип, непройденный этап никогда не выдаётся за
`passed`); `check_overridden` → только комментарий.

Связь «задача — карточка» принадлежит web-слою и лежит в SQLite
(`tracker_links`, строго 1:1 через `PRIMARY KEY(task_id)` и
`UNIQUE(tracker, issue_key)`), а не выводится из `external_links`. Web server
читает новые события из durable cursor интеграции, для связанных задач строит
операции и выполняет их в отдельном worker. Исход каждой доставки пишется в
`tracker_deliveries` (статус, попытки, санитизированная ошибка); сбой
дополнительно становится warning-событием `integration_error`, а курсор идёт
дальше: ошибка трекера не откатывает прогон, но остаётся видимой и
повторяется вручную через REST. Ключ идемпотентности комментария —
`<run_id>#<event_id>`, поэтому повтор события и ручной повтор не создают
дубль; общей транзакции между SQLite и трекером нет, доставка at-least-once.
Существующие ссылки из `external_links` один раз переносятся в `tracker_links`
при старте (`ImportTrackerLinks`); неоднозначные записываются в
`tracker_link_conflicts` и видны оператору, но не угадываются. Встроенный Jira
adapter говорит по REST API v3 (ADF) или v2 (простой текст), Jira
`Changes` читает changelog через JQL-поиск (`/rest/api/N/search/jql`, при 404 —
`/search` для Server/Data Center; окно времени относительное, `-Nm`, без
зависимости от часового пояса) и помечает собственные переходы
(`origin: ai-team`), определяя автора по учётной записи из `/myself`, а не по
`jira.email`. Ссылка в `external_links` у новых задач карточку не связывает:
нужен явный `PUT` связи. Неудавшаяся доставка снятой связи становится
`abandoned`, устаревший повтор перехода — `superseded`.

#### Входящая синхронизация (S15)

Включается блоком `tracker.inbound` (карта `rules` «внешний статус → запрос» и
`poll_interval`, по умолчанию 1 минута, не меньше 5 секунд). Сервер опрашивает
`Tracker.Changes` с курсором из `tracker_sync_state` (первый проход только
запоминает хвост трекера, историю до включения не проигрывает); ручной
`POST /api/tracker/sync` («sync now») делает тот же проход под тем же
мьютексом. Курсор сдвигается только после записи исхода каждого изменения; сбой
трекера или хранилища оставляет курсор на месте и виден в `GET /api/tracker/sync`
(последний опрос, задержка, число сбоев, ошибка), а повтор прочитает ту же
страницу без потерь и дублей.

Каждое внешнее изменение — одна строка `tracker_inbound_changes` с ключом
`(tracker, change_id)`: это идемпотентность по ID внешнего изменения. Исходы:
`applied`, `pending` (условия не выполнены), `conflict` (неоднозначный статус,
нет маршрута возврата), `ignored` (собственное эхо `origin: ai-team`, статус вне
карты), `superseded` (задача ушла с этапа или связь снята), `dismissed`
(закрыто оператором с причиной). Собственные исходящие переходы ai-team
приходят обратно с `origin: ai-team` и не становятся запросами, поэтому петли
исходящих/входящих обновлений нет.

Входящее изменение не мутирует граф само. Единственное, что оно запускает
автоматически, — `RunStageAgent` для этапа в фазе `resumable` (тот же сервисный
путь, что действие `run-agent`; актор `tracker:<adapter>`). Всё, что
требует человека (подтверждение этапа, решение по проваленному гейту, ввод
результата, возврат по `cfg.Returns`), остаётся `pending`/`conflict` с причиной
и текущими `allowed_actions`; оператор выполняет штатное действие этапа под
своей личностью и ролью либо закрывает запрос (`POST
/api/runs/{runID}/tracker/inbound/{changeID}/dismiss`). Поэтому статус «Done»
не обходит гейт, подтверждение человека и лимиты посещений: ни один из этих
кодовых путей не вызывается.

Доверие и аудит. Право запустить этап по статусу даёт конфигурация
`tracker.inbound`: оператор сам решил, какой статус продолжает этап. Внешний
автор не сопоставляется с пользователем ai-team, права этапа (RBAC) по нему не
проверяются; его отображаемое имя Jira меняет сам пользователь, поэтому оно
хранится и показывается только как недоверенная информация (очищенная от
управляющих символов, ≤256 байт) и в актор не входит. Перед вызовом контроллера
в журнал run пишется событие `tracker_inbound_launch` (актор `tracker:<adapter>`,
ID изменения, этап, статус); без записи запуска нет. Это событие не считается
авторством этапа: запуск от трекера не добавляет автора и не снимает авторство с
человека, поэтому `forbid_self_approval` и проверка роли подтверждающего не
меняются. Запрос применяется только к этапу, на котором статус поставлен:
статус, поставленный до входа на этап, и незавершённый после сбоя запрос при
уходе задачи с этапа получают `superseded`. Ключи связей уходят в JQL пачками
по 50; Jira-адаптер возвращает ошибку (курсор не двигается), если не удалось
узнать свою учётную запись через `/myself`. «Sync now» чаще раза в 5 секунд
возвращает результат последнего успешного ручного прохода (`coalesced`).

Ограничения, которые стоит знать. Отказ контроллера (активный run, бюджет,
дрейф конфигурации) оставляет запрос `pending` до действия оператора, автоповтора
нет. Первый проход запоминает хвост трекера, и пока нет связей, курсор уходит
вперёд: изменения, сделанные до создания связи, не видны и не проигрываются. Состояние видно в `GET /api/tracker`,
`/api/tracker/sync`, `/api/tracker/inbound`, `/api/runs/{runID}/tracker` и MCP-
инструментах `get_tracker_status`, `get_tracker_sync`, `list_tracker_inbound`,
`get_task_tracker`.

### Уведомления

`pkg/notifier` кроме консольного `Notifier` содержит внешние каналы
(`Channel`: webhook, Slack incoming webhook, SMTP e-mail), подписки по типу
события и функции этапа и `Dispatcher`. Web server читает события из того же
durable SQLite cursor, что и трекер (отдельный курсор `notifier`), и передаёт
их `Dispatcher`. Журнал `notification_deliveries` фиксирует пару «канал —
событие» после отправки или после записи `integration_error`, поэтому replay
после переподключения не дублирует уведомление, а сбой одного канала не
блокирует остальные. Если не удалось сохранить журнал или предупреждение,
событие остаётся непрочитанным и повторяется. Ошибки каналов не содержат URL и
учётных данных.

Deferred-гейты (APF-1): для стандартного и fast профилей forward-gate рёбра
помечаются `deferred: true` — переход не паузит и не решается по отдельности,
а аттестуется отдельным approval с точным subject и разрешается одним
consolidated delivery-решением run'а (`--approve-plan` или интерактивный
delivery). Rоль/кворум отдельного гейта в этом случае осознанно замещаются
единым delivery-контрпоинтом (роль `release_manager`); regulated-профиль
сохраняет пошаговые approvals. Повторный проход того же перехода с тем же
subject не создаёт новое ожидание и не перевалидируется (событие
`approval_reused`).

Test-mutation provenance (V0-1): mutation guard дополнительно классифицирует
каждый attributed change: режим изменения (added/modified/removed относительно
baseline этапа) и класс пути (`pkg/scope.ClassifyMutation` —
source/tests/meta/infra/generated/unknown, приоритет tests > generated > infra >
meta > source, остальное — явно видимый unknown). Классифицированные изменения
попадают в attempt manifest (`mutation_changes`) и typed-событие
`test_mutations` с политикой агента. Политика `test_modify_policy` задаётся в
definition агента (`required`/`warn`/`off`); default fail-closed: source-агент
по умолчанию `required`. При required на любую tests-мутацию создаётся
deferred-approval с точным subject = SHA-256 канонического списка
тест-изменений (без attempt ID — повторный проход с идентичной мутацией не
создаёт новый гейт) и действиями approve/reject, который разрешается тем же
consolidated delivery-решением, что и прочие deferred-гейты (APF-1). Так
изменение существующего теста source-этапом по умолчанию требует явного
человеческого решения на delivery; warn и off фиксируют тот же evidence без
гейта. Отчёт этапа показывает Test mutations review (path/class/kind).

Provenance manifest v1 (V0-2): `pkg/provenance` сериализует authority-bearing
identity каждого источника истины — `runtime` (digest исполняемого бинарника
контроллера через `evidence.ControllerIdentity`), `config` (resolved surface
cli/model/effort по этапам), по каждому этапу `agent_definition` (canonical
срез контракта: runtime/cli/kind/mutation/allowed_paths/require_diff/
test_modify_policy/inputs/outputs/ask_questions/read_scope/checks/verdict/preconditions),
`prompt` (digest содержимого prompt), `check_suite` (canonical digest
отсортированного списка checks) и `provider_model`. Base identity фиксируется
по стабильному {base_commit, base_tree}; candidate — по control metadata
(run_id/base/worktree), а не по mutable workspace-хэшу (содержимое worktree
меняется между попытками и охраняется approval'ами с exact CandidateSHA256).
Неперечислимое значение честно помечается `unknown` без догадок. Manifest
хранится inline в run.json (`RunManifest.Provenance`, opaque RawMessage) и
при resume сверяется с заново построенным (evident `CheckDrift`): известноe
поле, изменившееся/пропавшее/ставшее unknown, и возникновение нового
известного источника блокируют resume fail-closed; unknown никогда не дрифтит.
Runtime-digest даёт сигнал, не покрытый config/workflow snapshots: замена
бинарника контроллера между start и resume останавливает run.

Attestation predicate v1 (V0-3): `pkg/attest` строит in-toto compatible
statement (`_type: https://in-toto.io/Statement/v0.1`, subject = candidate
workspace digest, `predicateType: https://ai-team.dev/attestation/run/v1`) на
terminal-завершении run'а в `{RunDir}/attestation.json`. Predicate (schema v1)
связывает candidate с run identity (evidence schema, config/workflow sha,
digest всего event log, controller executable sha, attempt count), spec
(resolved workflow evidence+sha), check summaries (stage/name/class/policy/
status/exit_code/reason по attempts), typed mutations (V0-1), approvals c
решениями (из approval store), verdicts/outcome по попыткам и provenance
manifest v1 (V0-2). Canonical JSON (фиксированный порядок полей), golden
fixture-тест и строгий `Parse` (DisallowUnknownFields + проверка
_type/predicateType/schema_version) — compatibility policy; `Digest` даёт
стабильный sha256 statement'а. За рамки warning/usage не утверждает ничего,
что не может вычислить deterministically (subject пуст вне Git).

export/verify (V0-4): `pkg/export` собирает самодостаточный deterministic
portable bundle терминального run в `ai-team export <run_id>` (whitelisted
typed records — run/config/workflow snapshots, hash-chained event log, anchor,
attestation v1, attempt manifests и их input/output artifacts; при наличии
также terminal delivery и containment records; без raw logs/stdout и reports).
`export` сначала выполняет полную live-проверку, включая sidecars. index.json несёт sha256
каждого record без тайм-меток — identical evidence даёт байт-в-байт одинаковый
bundle (BundleDigest). Все файлы собранного bundle (records, index.json и
dsse.json) пишутся одинаковыми правами только на чтение
(`safeio.ReadOnlyFileMode`) — и в run-bundle, и в gate-bundle; каталоги при
этом остаются writable владельцем, поэтому фактическую неизменяемость даёт не
режим файлов, а digest и verify. Перед публикацией bundle обязан пройти
полную verify:
records против своих sha256, run identity/schema, config/workflow snapshots
против run manifest, event chain + anchor (VerifyAnchor), attempt manifests
против manifest_sha256 в attempt_finished событиях (файлы↔events связка, которой
VerifyAnchor не даёт), размеры и sha256 всех опубликованных attempt artifacts,
attestation v1 против events/config/workflow/
attempt_count/provenance. Только после успешной verify пишется verified-запись
в state/exports/<runID>.json (контракт V0-0), открывающая право `gc --prune-runs`.
`ai-team verify <bundle-dir>` проверяет bundle самодостаточно без repo и
.ai-team; `ai-team verify <run_id>` — та же full-свёртка live evidence.
Экспорт отказастся от non-terminal run и от run без attestation.json.

Signed bundle (P1-5, DSSE): `pkg/dsse` реализует минимальный DSSE-envelope на
stdlib-only (PAE — Pre-Authentication Encoding `"DSSEv1"` с длинами payloadType
и payload + ed25519 sign/verify из `crypto/ed25519`; ноль внешних зависимостей).
`export.SignBundle` и `gate.SignBundle` подписывают детерминированный
`BundleDigest` (sha256 канонического index.json) и пишут `dsse.json` в bundle
(рядом с index.json); сам signature-файл не попадает в index.Records, чтобы не
влиять на детерминизм. Ключи загружаются из файлов каждая команда через
`--sign-key <path>` (PEM PKCS8 ed25519 или raw), никогда не коммитятся и не
попадают в evidence. Верификация fail-closed: `ai-team verify --verify-key
<pub>` требует наличия и валидности подписи для каждого проверяемого bundle
(export/gate `VerifyBundle` принимают variadic verify-key); без ключа —
integrity-only как раньше, с ключом при отсутствии/несовпадении подписи —
ошибка. Подпись связывает digest с автором («кто создал») поверх integrity
(«не изменено»), поддерживая future audit.

gate MVP (V0-5): `pkg/gate` + `ai-team gate` — deterministic diff-policy гейт
для trusted local base/candidate без .ai-team и без runtime. Диф между двумя
локальными ref'ами (или ref и WORKTREE) парсится в типизированные мутации
(kinds added/modified/removed сразу с V0-1 классификацией через scope),
политика test_modify (required/warning/off) отклоняет изменение source без
сопутствующих изменённых/добавленных тестов. Typed checks переиспользуют
pkg/checks Runner (evidence digests, workspace immutability). Вердикт пишется
в самодостаточный attestation bundle (gate.json + checks/*.json + детерминиро-
ванный index.json; BundleDigest — sha256 канонического индекса; каждый record
связан со своим sha256 в индексе). Стабильные exit codes:
0 PASS, 1 FAIL (policy/required checks), 2 BLOCKED. Fail-closed по умолчанию:
ref, который не разрешается в локальный объект, или --allow-untrusted дают
BLOCKED — untrusted mode запрещён до P1-4. Bundles проверяются самодостаточно:
`gate.VerifyBundle` перечитывает каждый record и сверяет digest с index.json,
реагирует на лишние файлы (за пределами checks/*+gate.json), на отсутствующие
records и на расхождение заявленного bundle_sha256; `ai-team verify <dir>`
автоматически различает run-bundle (V0-4) и gate-bundle по наличию gate.json.
WORKTREE-кандидат идентифицируется в index как ref "WORKTREE" (commit пуст).

Demo + CI action + truthful README (V0-7): `docs/demo/` — самодостаточное демо
`gate → bundle → verify` на не-Go репозитории. `run-demo.sh` строит изолирован-
ный Python-репозиторий и прогоняет три сценария (PASS / test_modify violation /
JUnit failure при exit code команды 0), каждый со своим bundle и verify;
`ci-gate-demo.yaml` — один version-pinned workflow (ai-team по зафиксированному
SHA, actions по SHA), который на PR считает merge-base, выполняет gate, затем в
любом случае verify и upload bundle артефактом. README честно разделяет run и
gate, integrity (гарантируется цепочкой хэшей) и authenticity (P1-5: DSSE-ed25519
подпись BundleDigest через `--sign-key`/`--verify-key`, см. ниже), а также фиксирует отсутствие
OS sandbox. Внешняя верификация «три человека проходят демо» — за владельцем.

Risk-signals (V0-8): `pkg/risk` — детерминированный классификатор
чувствительных путей (env / secrets / credentials: `.env*`, ключи и
cert-расширения, id_*/secret-каталоги, credentials файлы; `.env.example` —
шаблон, не сигнал). `gate.Result.Signals` собирает в bundle измеренные
signals: размер diff (added/removed lines через `git diff --numstat`, файловый
разбив added/modified/removed), количество тестовых изменений (class tests ×
added/modified), чувствительные пути, `checks_run` и `failed_checks`. Сигналы
записываются как данные и НЕ маршрутизируют pipeline (никакого
автоматического решения на их основе); routing придёт отдельно в P2-1/P2-2
вместе с продуктовым решением владельца. CLI summary печатает однострочный
срез signals; в index.json и gate.json сигналы входят в record digest.

JUnit XML typed adapter (V0-6): `pkg/junit` — bounded strict parser JUnit-отчётов
(корни testsuite/testsuites, вложенные suite). Counts suites/testcases/
failures/errors/skips выводятся только из реальных `<testcase>` элементов, а не
из атрибутов-агрегатов (jest/maven/gradle заполняют их непоследовательно);
zero-test/zero-passed отчёты отклоняются (не evidence). DOCTYPE/entity и
прочие processing instructions запрещены, глубина ≤32, размер ≤64 MiB. В
pkg/checks новый adapter `junit-xml` с `report_file` (repo-relative обязательный
путь внутри target): command запускается в confined workingDir и пишет отчёт,
adapter читает его строгим parser'ом и дигирует сырой файл (StructuredOutput-
Bytes/SHA256), #тестов (discovered/passed/failed/errored/skipped) попадают в
Result. Семантика Maven/Gradle: exit code команды не является авторитетным —
failure/error в XML фейлит required check даже при `sh` exit 0. report_file —
объявленный side-effect проверки и исключается из workspace-immutability
детекции; всё остальное остаётся read-only. Golden fixtures: pytest, Jest,
Gradle, Maven (плюс zero/doctype/entity). Снимает ограничение «только Go» для
демо (V0-7) — проверки любых языковых флотов работают с одним adapter.

Локальный `RunController` связывает HTTP с тем же `RunEngine`, резервирует
run ID до запуска worker goroutine и запрещает дублирующий active worker.
`POST /api/runs`, `/resume`, `/cancel` и approval `/decisions` возвращают
асинхронный command result; durable lifecycle остаётся источником истины после
restart. Write routes требуют случайную HttpOnly SameSite session-cookie и
независимый `X-CSRF-Token` поверх fixed loopback Host/Origin policy.

До резервирования run controller выполняет типизированный runtime preflight.
Required checks fail closed: доступны OpenCode/version и Git repository, а
для pipeline с delivery — remote origin и authenticated GitHub CLI.
Model/provider и имена явно разрешённых credential variables отображаются
без секретных значений. `GET /api/preflight` возвращает тот же report, который
повторно применяется при `POST /api/runs`.

Сетевой MCP (`pkg/web/mcp*.go`, SDK `go-sdk`, спецификация 2026-07-28) — один
stateless endpoint `POST /mcp` в том же роутере. Bearer-токен (персональный `aitm_…`
из таблицы `mcp_tokens` или токен `IdentityVerifier`) разрешается в участника команды
через `resolveTeamPrincipal`; инструменты — декларативный реестр, исполняемый внутренним
запросом к тому же `chi`-роутеру с личностью в контексте, поэтому RBAC и бизнес-логика
не дублируются. Реестр содержит и чтения, и действия (`mcp_read_tools.go`, `mcp_action_tools.go`): действие
отправляет тот же `POST`/`PUT`/`PATCH`/`DELETE` с CSRF-токеном личности токена, а слой MCP добавляет только
`idempotency_key` (область — личность, таблица `api_idempotency`) и аудит `mcp_action`; канал решения человека `mcp`
берётся из контекста вызова (`decisionOrigin`). Каждый маршрут роутера обязан иметь строку в матрице паритета
(`mcp_parity.go`, проверяется `chi.Walk`). Контракт — `openspec/specs/mcp-server`, руководство — `docs/reference/mcp.md`.

SQLite `.ai-team/web.db` — восстанавливаемая projection для web UI, а не
источник истины. Сервер по умолчанию слушает `127.0.0.1`, проверяет
same-origin для HTTP (`pkg/web/security.go`) и WebSocket. Lifecycle events
имеют versioned contract, сохраняются в SQLite и передаются через production
WebSocket bridge с cursor/replay; редкий polling остаётся recovery fallback.
Сервер отдаёт bounded файлы только из разрешённого live или immutable run root
без symlink traversal. Production frontend встроен в бинарник (`go:embed`);
`--dist` позволяет явно подменить его локальной сборкой.

В cloud mode versioned HMAC token устанавливает immutable actor ID,
канонические роли и bounded expiry. После проверки token заменяется
уникальной HttpOnly browser-session; token не попадает в SQLite или evidence.
API reads, commands и WebSocket требуют session, а write-команды —
дополнительно session-bound CSRF. Decision всегда получает actor ID из
server-side principal, выбранная роль обязана принадлежать principal.
Отдельные RBAC policies защищают start и cancel. Без signing secret остаётся
совместимый zero-config loopback mode.

При `--worker-command` web controller использует process-backed RunEngine.
Он сериализует строгий job schema v1 для одной операции start/resume/cancel,
передаёт его через stdin без shell и ограничивает diagnostics. Subcommand
`ai-team worker` повторно загружает config/agent registry внутри exact
mounted target, подключает общий SQLite recorder и вызывает обычный
RunEngine. Поэтому candidate, approvals, checks, delivery и evidence не имеют
альтернативной cloud-семантики. Локальный subprocess — reference launcher;
настоящую process/filesystem/network isolation обеспечивает disposable
container/job инфраструктуры.

Distributed mode (`--scheduler-db`) заменяет direct ProcessEngine на
queue-backed RunEngine. Persistent SQLite queue хранит strict worker Job,
idempotent active identity, attempts, cancel flag и случайный ownership token.
Claim/renew/complete выполняются conditional SQL updates: global limit и
per-target lock проверяются атомарно, истёкший lease возвращается в очередь,
а stale worker не может завершить re-claimed job. Claim ограничивается
каноническим путём persistent workspace; после повторного claim start-job
получает операцию `recover`, которая проверяет checkpoint и выбирает Start
только если создание lifecycle ещё не состоялось. Поддерживаемая схема
непрерывности и требования к общему тому описаны в [руководстве по
восстановлению worker](guides/worker-recovery.md).

`ai-team scheduler-worker` поддерживает one-shot platform job и loop poller.
Он продлевает lease, передаёт cancel в context disposable process и после
execution архивирует immutable run tree. Reference artifact backend хранит
blobs по SHA-256 и отдельную content-addressed manifest reference; restore
повторно проверяет digest, size, relative path и запрещает symlink/non-regular
entries. Local SQLite/CAS реализуют cloud contracts, но не подменяют managed
queue/object storage для multi-host production.

Stage projection сохраняет checks, mutations и delivery как typed JSON,
который detail page показывает без необходимости открывать HTML-report.
Лог раскрытого attempt читается отдельным exact-identity endpoint только как
хвост до 64 KiB; пока attempt активен UI обновляет этот хвост коротким
polling. Это узкий поток изменяемого текста: lifecycle и approvals остаются
в durable WebSocket с cursor/replay.

## Deployer и canonical delivery plan

`deployer` — не исполнитель произвольных команд от LLM. Контроллер строит
строгий JSON plan с push URL remote и только с workspace-relative файлами,
изменение которых атрибутировано актуальным попыткам текущего run. Затем он проверяет:

1. review verdict — `APPROVED`;
2. test-report result — `PASS`;
3. verifier verdict — `APPROVED`;
4. наличие успешного required typed test check;
5. approval точного SHA-256 canonical plan (`--approve-plan`);
6. пустой staged git index;
7. защиту базовой ветки (нельзя делать delivery поверх protected branch
   напрямую).

Только после всех семи условий выполняются exact-file commit, push и создание
PR через `gh`. Перед commit повторно сверяются staged paths, blob SHA-256 и
file modes; после commit — parent, tree, blobs и modes. Состояние каждого шага
сохраняется в attempt evidence. Если процесс оборвался сразу после `git
commit` (например, процесс убит между commit и push), controller при retry
принимает уже существующий commit только после повторной сверки message,
parent, paths, modes и blob hashes с canonical plan — так что штатный retry не
создаёт дублирующий commit или PR. Перед side effects controller повторно
сверяет push URL remote с одобренным планом; каждый Git вызов отключает hooks,
fsmonitor и commit signing.

### Deferred delivery и PR-stage delivery — V0-9 / B-35

Обычно deferred delivery выполняется после terminal finalize: это позволяет
включить детерминированный attestation digest в commit trailers. Но стандартный
граф требует `pr_exists`, `pr_open` и `pr_base_branch` до перехода к review и
QA. Для agent stage с PR result и hard `pr_exists` controller поэтому
выполняет одобренную delivery внутри run сразу после публикации immutable
attempt manifest и до этих hard checks. Остальные delivery stages сохраняют
post-terminal поведение.

Stage-scoped вызов `delivery.stage.execute` принимает только run, attempt,
manifest SHA-256, canonical plan hash и runtime identity. Контроллер сверяет
их с pinned config, strict event replay, controller-owned approval и точным
candidate digest, затем выполняет `delivery.Execute` с controller-derived
trailers `ai-team-run` и `ai-team-runtime`. Worker не передаёт URL, plan body
или receipt. Контроллер durably сохраняет self-digesting receipt, а затем
публикует URL как `result.link` в project artifacts и candidate artifacts.
Retry сначала читает receipt и чинит обе проекции; если удалённый PR создан,
но receipt ещё не записан, delivery executor находит и проверяет PR по branch
и commit до продолжения. Этот controller-owned `pull-request` artifact
snapshot'ится как immutable input следующему review/QA stage, включая resume
непосредственно на таком stage.

Без интерактивного подтверждения agent stage останавливается в `awaiting_plan`
после coder, controller checks и `delivery.Prepare`. `run --resume <id>
--approve-plan <sha256>` продолжает этот stage отдельной controller-only
попыткой: runtime агента повторно не вызывается, guard требует неизменный
candidate workspace, checks перезапускаются, план перестраивается из prepared
state и авторизуется ровно по тому же SHA-256. Иначе новый прогон модели дал бы
план, которого человек не видел.

Attempt manifests не переписываются после delivery. Terminal finalize позднее
создаёт attestation и run-level terminal record, который ссылается на точный
stage receipt, attempt manifest SHA-256, attestation digest и runtime identity.
Так как PR уже проверен человеком, поздний attestation digest не добавляется в
commit и не вызывает повторный delivery с изменёнными trailers.

Для delivery flows без stage-scoped PR проверки post-terminal hook сохраняет
прежний контракт: он перегружает canonical plan из prepared state, проверяет
attestation и runtime identity, выполняет `delivery.Execute` и записывает
tamper-evident `{RunDir}/delivery.json`. Трейлеры персистятся в delivery state
до commit, поэтому crash-recovery сверяет message с
`FullCommitMessage(plan.CommitMessage, savedTrailers)`.

`FindDelivered` сначала читает controller terminal record, а при его отсутствии
падает на attempt manifests. Если post-terminal hook упал, доставка повторяема
командой `ai-team deliver --run <run_id> [--target <dir>] [--feature <name>]`;
run должен быть терминальным `completed`, plan обязан совпасть с
`delivery_deferred`, а существующий controller stage receipt переиспользуется
без повторного commit или PR.

## Layered agent registry

`ai-team list` показывает источник победившего definition агента. Registry
разрешает агентов слоями, от наиболее специфичного к built-in:

```text
project .ai-team/agents/  →  AI_TEAM_AGENT_PATH каталоги  →  user config  →  built-ins
```

Невалидный override не скрывается fallback-слоем: если project- или
plugin-слой объявляет агента, но его definition невалиден (например, битый
YAML или неизвестный mutation scope), registry возвращает ошибку вместо
молчаливого отката к built-in версии того же имени. Это осознанный выбор —
тихий fallback на built-in agent мог бы означать, что запущен не тот код,
который автор override ожидал увидеть.

`config.Default()` строит имена стадий из `agent.Registry.DefaultPipeline()` —
единственного места, которое перечисляет built-in порядок стадий; конфиг и
CLI не дублируют этот список независимо.
