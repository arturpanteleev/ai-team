# Архитектура ai-team

Этот документ — глубокое техническое описание внутреннего устройства
контроллера. Если вы ищете, как установить и запустить ai-team, начните с
[README.md](../README.md); этот файл предполагает, что вы уже знаете базовый
конвейер (`analyst → architect → coder → reviewer → tester → verifier →
deployer`) и термины из [глоссария](../README.md#глоссарий).

## Карта пакетов

```text
cmd/ai-team/        точка входа CLI
pkg/
├── config/         конфигурация, строгая валидация, layered registry lookup
├── agent/          Agent struct + Registry (резолюция definition-слоёв)
├── runtime/         Runtime interface + AgentCLI (prompt, логи, model/effort)
├── pipeline/        оркестрация, enforcement вердиктов, гейты, loopback
├── workflow/        доменные state/outcome типы и чистые переходы
├── verdict/         verdict-контракт: парсер маркеров, BLOCKED-протокол
├── checks/          deterministic verification runner и evidence
├── scope/           repository-relative mutation path policy (glob-матчинг)
├── delivery/        строгий canonical plan и controller-owned executor
├── evidence/        immutable run/attempt manifests, append-only events
├── safeio/          no-follow filesystem primitives (symlink rejection)
├── process/         process-group supervision и kill (Unix/Windows/plan9)
├── eval/             независимая LLM-оценка артефактов
├── notifier/         уведомления о событиях pipeline
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

Run manifest schema v6 связывает exact config/workflow snapshots, SHA-256
исполняемого controller и Go/VCS identity. Attempt manifest содержит
execution/decision/outcome, blocker, SHA-256/provenance артефактов, checks,
mutations и delivery result. Публикация attempt атомарна, events связаны
SHA-256 hash chain и проверяются перед каждым append (splice/reorder другой
цепочки событий обнаруживается при replay). Typed replay проверяет порядок
lifecycle transitions, terminal status, invalidations и exact digest каждого
опубликованного attempt manifest.

Изменяемый execution checkpoint хранится отдельно в
`.ai-team/state/runs/<run_id>.json`. `RunEngine` атомарно обновляет его на
границах stages и может после restart открыть проверенную evidence chain для
append. Resume сохраняет исходный `run_id`, добавляет `run_resumed` и
продолжает ordinal sequence попыток; terminal state возобновить нельзя.

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

Deferred-гейты (APF-1): для стандартного и fast профилей forward-gate рёбра
помечаются `deferred: true` — переход не паузит и не решается по отдельности,
а аттестуется отдельным approval с точным subject и разрешается одним
consolidated delivery-решением run'а (`--approve-plan` или интерактивный
delivery). Rоль/кворум отдельного гейта в этом случае осознанно замещаются
единым delivery-контрпоинтом (роль `release_manager`); regulated-профиль
сохраняет пошаговые approvals. Повторный проход того же перехода с тем же
subject не создаёт новое ожидание и не перевалидируется (событие
`approval_reused`).

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
а stale worker не может завершить re-claimed job.

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
строгий JSON plan только из workspace-relative файлов, изменение которых
атрибутировано актуальным попыткам текущего run. Затем он проверяет:

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
parent, paths, modes и blob hashes — так что штатный retry не создаёт
дублирующий commit или PR.

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
