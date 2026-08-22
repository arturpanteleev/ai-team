# ai-team

[![CI](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml/badge.svg)](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml)

Локальный control plane для решения IT-задач цепочкой AI-агентов. LLM создаёт
артефакты (proposal, design, код, тесты, review) и предлагает вердикты;
переходы между этапами, проверки, mutation scopes, evidence и delivery
исполняет детерминированный Go-контроллер — не LLM.

Если вы новый читатель, читайте разделы по порядку: этот README проведёт вас
от установки до первой поставленной фичи. Глубокое описание внутреннего
устройства — в [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); процесс участия в
разработке самого ai-team — в [CONTRIBUTING.md](CONTRIBUTING.md).

## Для кого этот инструмент

**Подходит:** доверенный локальный репозиторий, где вы согласны, что агент и
verification-команды (тесты, vet, линтеры) выполняются с правами вашего
текущего OS-пользователя.

**Не подходит (пока):** недоверенный или сторонний код, секреты, к которым
агент не должен иметь доступ, production delivery без человеческого review.
Система пока не является hermetic sandbox — подробности и открытые риски в
[AUDIT.md](AUDIT.md).

## Предварительные требования и установка

| Зависимость | Зачем | Проверка |
|---|---|---|
| Go 1.26.5+ | сборка и запуск `ai-team` | `go version` |
| [OpenCode](https://opencode.ai) CLI в `PATH` | LLM runtime, который вызывают агенты | `opencode --version` |
| [`gh`](https://cli.github.com) CLI, авторизованный (`gh auth login`) | deployer использует его для `pr create`/`pr view` | `gh auth status` |

OpenCode устанавливается независимо от ai-team, например:

```bash
curl -fsSL https://opencode.ai/install | bash
```

и должен быть настроен как минимум с одним LLM-провайдером — см.
[opencode.ai/docs](https://opencode.ai/docs). `gh` нужен только на шаге
delivery (последний агент, `deployer`); если вы не планируете, чтобы
контроллер сам открывал PR, шаги до этого работают без него.

Установка `ai-team`:

```bash
go install github.com/arturpanteleev/ai-team/cmd/ai-team@latest
```

## Быстрый старт

```bash
cd /my-project
ai-team init
```

`init` создаёт `.ai-team/config.yaml` со строгими настройками по умолчанию,
локально исключает `.ai-team/` через `.git/info/exclude` и автоматически
включает typed Go-проверки (`go test -json -count=1` + `go vet`), если находит
`go.mod`. Поэтому чистый Git workspace остаётся чистым и можно сразу запускать
pipeline. Если правило нужно хранить в репозитории, используйте
`ai-team init --write-gitignore`. Для стеков без typed adapter (Rust, Python,
Node, неизвестный) `init` выводит warning: delivery остаётся запрещённым, пока
вы не настроите required unit/integration check вручную (см.
[«Конфигурация»](#конфигурация)).

```bash
ai-team run --feature add-jwt-auth --task "Реализовать JWT авторизацию"
```

Это самый частый способ запуска. Переходы между смысловыми AI-этапами требуют
решения человека с подходящей ролью. В TTY контроллер спрашивает решение
сразу; без TTY сохраняет pending approval и тот же run можно продолжить после
записи решения.

## Как поставить фичу от начала до конца

Полный путь одной фичи через `run` — два запуска, а не один: контроллер
намеренно останавливается перед любым внешним эффектом (commit/push/PR) и ждёт
явного подтверждения именно того плана, который он показал.

1. **Первый запуск** проводит фичу через весь конвейер до `deployer` и
   останавливается перед delivery с exit code `3`, напечатав canonical delivery
   plan и его SHA-256:

   ```bash
   ai-team run --feature add-jwt-auth \
     --task "Реализовать JWT авторизацию" \
     --approve-gates
   ```

   (`--approve-gates` нужен только в non-interactive среде — например, в CI
   или скрипте; в интерактивном терминале checkpoints спросят подтверждение
   сами, без флага.)

2. **Прочитайте план.** Он перечисляет ровно те файлы, которые будут
   закоммичены, ветку и сообщение коммита. Это единственный момент, где стоит
   остановиться и проверить, что candidate действительно то, что вы ожидали.

3. **Продолжение того же run** передаёт SHA-256 именно этого плана — и только
   тогда контроллер выполняет commit, push и создаёт PR из сохранённого
   candidate-worktree:

   ```bash
   ai-team run --resume <run_id> \
     --approve-gates --approve-plan <sha256-из-шага-1>
   ```

   Delivery approval — обычная persisted approval с subject = SHA-256 плана
   и ролью `release_manager`. То же решение можно записать без CLI-resume:
   через web UI (`POST /decisions`) или командой `ai-team decision`, после
   чего достаточно `ai-team run --resume <run_id>`. Если план изменился
   (другой коммит поверх, другие файлы) — старый SHA-256 не подойдёт ни
   одним из путей, и контроллер откажется выполнять delivery. Это осознанное
   поведение, а не баг: подтверждение одноразовое и привязано к конкретному
   плану.

4. **Проверьте результат** — `ai-team web` открывает дашборд со статусом
   запуска, live-логом, checks/mutations/delivery по каждому этапу и
   артефактами; сырые evidence того же run лежат в
   `.ai-team/runs/{run_id}/` (см.
   [«Evidence и наблюдаемость»](docs/ARCHITECTURE.md#evidence-и-наблюдаемость)),
   если поднимать web не хочется.

Если reviewer вернул `CHANGES_REQUESTED`, pipeline не теряет loopback в
non-interactive режиме: он сохраняет запрос решения с действиями
`return_to_coder`, `override_approve` и `reject`. После решения человека
`--resume` продолжает тот же run без повторного запуска уже завершённых
этапов.

## CLI-справочник

| Команда | Назначение |
|---|---|
| `ai-team init [--target <dir>] [--write-gitignore]` | создать `.ai-team/config.yaml`, каталоги artifacts/reports/logs; по умолчанию использовать локальный Git exclude |
| `ai-team run --feature <name> --task "<desc>" [...]` | провести фичу через конвейер |
| `ai-team decision --run <id> --approval <id> --actor <id> --role <role> --action <action> --subject <sha256>` | записать точное решение человека |
| `ai-team auth-token --actor <id> --roles <csv> [--ttl 1h]` | выпустить короткоживущий подписанный token для cloud web |
| `ai-team worker --target <dir> --db <path>` | исполнить один strict worker job из stdin (обычно вызывается launcher-ом) |
| `ai-team list` | список доступных агентов (имя, runtime, источник в layered registry) |
| `ai-team eval --agent <name> --artifact <path> [--samples N]` | независимая LLM-оценка артефакта |
| `ai-team web [--target <dir>] [--port 8080] [--host 127.0.0.1]` | локальный dashboard и control plane |
| `ai-team version` / `ai-team help` | версия / usage |

Флаги `run`: `--feature`, `--task`, `--target` (по умолчанию `.`),
`--resume <run_id>`, `--approve-gates`, `--approve-plan <sha256>`.

Если процесс был остановлен во время non-terminal run, controller сохраняет
атомарный checkpoint в `.ai-team/state/runs/<run_id>.json`. Команда
`ai-team run --resume <run_id>` проверяет immutable evidence chain и
config/workflow snapshots, после чего продолжает тот же run с сохранённого
этапа. Новый `run_started` при этом не создаётся.

Pending approvals лежат в
`.ai-team/state/approvals/<run_id>/<approval_id>.json`. CLI печатает
`run`, `approval` и точный `subject` для команды `decision`; решение с
устаревшим hash, неподходящей ролью или конфликтующим повтором отклоняется.
`approval_quorum: any|all` позволяет потребовать одну из ролей или все роли.
Флаг `--approve-gates` остаётся совместимым локальным transport: на каждой
достигнутой точке он создаёт те же exact request/decision и evidence, а не
обходит approval-модель.

`ai-team web` использует тот же `RunEngine`: из dashboard можно создать run,
а на detail page — принять exact approval, выполнить resume или cancel.
Команды возвращают сразу, а выполнение и статусы приходят через
SQLite/WebSocket. При загрузке same-origin UI сервер выдаёт случайную
HttpOnly session-cookie и отдельный CSRF token; каждый write request требует
оба значения.

Для cloud/self-hosted режима задайте одинаковый secret длиной не менее
32 байт при выпуске token и запуске web:

```bash
export AI_TEAM_AUTH_SECRET='<случайный-секрет-не-короче-32-байт>'
ai-team auth-token --actor architect-1 --roles architect,reviewer --ttl 1h
ai-team web --host 0.0.0.0
```

Bearer token используется только для создания персональной browser-session.
Все API reads, команды и WebSocket требуют session, actor ID решения берётся
с сервера, а роли проверяются по RBAC. Product Owner и Architect могут
создавать run; Product Owner и Release Manager — отменять; approval можно
принять только ролью, которая одновременно есть у principal и указана в
policy ребра. Для публичного размещения TLS должен завершаться на внешнем
ingress/reverse proxy.

Чтобы вынести AI execution из HTTP process, укажите executable того же
`ai-team` как worker launcher:

```bash
ai-team web --worker-command /opt/ai-team/bin/ai-team
```

Control plane передаёт versioned bounded job через stdin, а subprocess
исполняет ровно один start/resume/cancel через общий `RunEngine` и завершается.
Это reference launcher для development/self-hosted режима. В production тот
же argv contract должен запускаться disposable container/job с exact
repository mount и инфраструктурными filesystem/process/network limits;
сам subprocess не объявляется standalone OS sandbox.

Для нескольких worker replicas включите persistent scheduler:

```bash
# control plane только ставит jobs в очередь
ai-team web --scheduler-db .ai-team/scheduler.db

# один или несколько внешних workers
ai-team scheduler-worker \
  --scheduler-db .ai-team/scheduler.db \
  --artifact-store .ai-team/cloud-artifacts \
  --worker-command /opt/ai-team/bin/ai-team
```

Queue сохраняет jobs до claim, запрещает duplicate active operation,
выдаёт bounded lease с heartbeat и применяет global/per-target concurrency.
Истёкший lease доступен новому worker, а старый lease token уже не может
завершить job. Cancel хранится в queue и отменяет disposable process через
heartbeat. После execution immutable `.ai-team/runs/<run_id>` архивируется в
SHA-256 CAS; manifest содержит exact path/digest/size/mode и позволяет
проверяемое восстановление. SQLite queue и local CAS — reference backends,
которые можно заменить managed queue/object storage через те же contracts.

Перед созданием run dashboard показывает runtime preflight. Controller
повторяет тот же gate непосредственно при `Start`: проверяет OpenCode и его
версию, model/provider, credential allow-list и Git repository; для workflow
с delivery дополнительно требует `origin`, `gh` и успешный `gh auth status`.
Значения credentials никогда не попадают в report.

Exit-коды `run`: `0` — completed/completed with warnings, `1` — ошибка или
негативный вердикт, `2` — BLOCKED, `3` — stopped на checkpoint или перед
delivery.

## Конвейер и зоны ответственности

Порядок по умолчанию:

`analyst → architect → coder → reviewer → tester → verifier → deployer`

Reviewer, tester и verifier обязаны записать ровно один канонический verdict
marker; отсутствующий, неизвестный или дублирующий marker — ошибка
контроллера, а не рекомендация LLM. Любой этап может вместо этого
сигнализировать **BLOCKED** через отдельный status-файл с обязательной
причиной — контроллер останавливает pipeline с exit code `2`.

`deployer` не исполняет произвольные команды от LLM — он выполняет только
**canonical delivery plan**, который контроллер сам построил из файлов,
изменённых в рамках текущего run, и который вы явно подтвердили точным
SHA-256 (см. [«Как поставить фичу»](#как-поставить-фичу-от-начала-до-конца)).
Как именно контроллер проверяет план, blob-хэши и recovery после обрыва —
в [ARCHITECTURE.md](docs/ARCHITECTURE.md#deployer-и-canonical-delivery-plan).

## Конфигурация

`ai-team init` создаёт строгий schema v4 config. Узлы остаются в `pipeline`,
а маршрут и обязательные человеческие approvals принадлежат рёбрам:

```yaml
schema_version: 4
pipeline:
  - name: analyst
  - name: architect
  - name: coder
  - name: reviewer
  - name: tester
    checks:
      - name: go-test
        class: unit
        adapter: go-test-json
        command: [go, test, -json, -count=1, ./...]
        policy: required
        timeout: 20m
  - name: verifier
  - name: deployer
workflow:
  entry: analyst
  max_visits: {coder: 3, reviewer: 3, tester: 3, verifier: 3}
  edges:
    - from: analyst
      outcome: passed
      to: architect
      approval:
        roles: [product_owner]
        quorum: any
        actions: {approve: architect, reject: $stop}
    # остальные passed edges задаются так же
    - from: reviewer
      outcome: rejected
      to: coder
      approval:
        roles: [reviewer]
        quorum: any
        actions:
          return_to_coder: coder
          override_approve: tester
          reject: $stop
cli: opencode
effort: medium
stage_timeout: 30m
```

Unknown/duplicate YAML-поля, несколько документов, неподдерживаемые
schema/CLI, неоднозначные edges, недостижимые узлы, неограниченные циклы,
пути или checks отклоняются до первого LLM-вызова. Поддерживается только
schema v4; конфиги schemas 1–3 отклоняются с подсказкой о миграции. Как именно резолвятся агенты между
project/plugin/user/built-in слоями и что происходит с invalid override —
в [ARCHITECTURE.md](docs/ARCHITECTURE.md#layered-agent-registry).

## Evals

`ai-team eval` запускает независимые LLM-оценки артефакта в изолированном
временном каталоге и сохраняет samples/median/mean/standard deviation в
`.ai-team/evals/`. Это **advisory** сигнал качества, не delivery gate —
нормативные гарантии дают deterministic check suites из конфигурации.

## Глоссарий

Термины, которые встречаются выше и в CLI-выводе, но не всегда очевидны без
контекста:

- **approval** — типизированное решение человека по точному SHA-256 subject:
  содержит допустимые actions, требуемые роли, quorum, actor и комментарий.
  В non-interactive среде run переходит в `waiting_for_approval`, а не
  завершается и не пропускает переход.
- **checkpoint** — прежний совместимый способ объявить точку подтверждения.
  Default workflow использует role-based approvals; legacy checkpoint policy
  по-прежнему валидируется и исполняется fail-closed.
- **verdict marker** — единственная каноническая строка-маркер
  (`**Verdict:** APPROVED` / `CHANGES_REQUESTED` / `**Result:** PASS` / `FAIL`),
  которую reviewer/tester/verifier обязаны записать в свой отчёт. Контроллер
  парсит именно её, а не свободный текст.
- **BLOCKED** — отдельный протокол: этап пишет status-файл с обязательной
  причиной вместо verdict marker, когда не может продолжить (например,
  противоречивые требования). Pipeline останавливается с exit code `2`.
- **mutation scope** — объявленное в definition агента разрешение на то, какие
  файлы этап имеет право менять. Baseline фиксируется перед попыткой; изменения
  вне scope проваливают guard.
- **candidate** — набор файлов, изменённых в рамках текущего run/attempt,
  который проходит review, tests и verification и в итоге становится
  предметом delivery plan.
- **canonical delivery plan** — точный JSON-план (файлы, ветка, сообщение
  коммита), который контроллер строит перед delivery. Подтверждается только
  по точному SHA-256 — не общим "да, делай commit".
- **attempt / run** — `run` — один вызов `ai-team run` для фичи; `attempt` —
  одна попытка конкретного этапа внутри run (loopback создаёт новый attempt, а
  не переиспользует старый).

## Граница безопасности

Система пока не является hermetic sandbox. OpenCode получает app-level deny
для shell/network/tasks, ограниченные edit/read rules и отдельный config home,
но сам процесс агента и команды проверок работают с правами текущего OS-user.
Поэтому текущий профиль допустим только для доверенного локального проекта;
секреты и недоверенный код должны запускаться во внешнем container/VM sandbox.
Полный список открытых рисков и release gates — в [AUDIT.md](AUDIT.md).

## Разработка

Проект использует OpenSpec (спецификации в `openspec/specs/`, активные
изменения — в `openspec/changes/`) и приветствует контрибьюторов — процесс
целиком описан в [CONTRIBUTING.md](CONTRIBUTING.md).

```bash
make build
make test
make test-e2e
make specs
make verify
```

`make verify` выполняет gofmt-проверку, строгую OpenSpec-валидацию, module
verification, vet, govulncheck, race tests, coverage gate 60% (`make
test-coverage`), E2E (`make test-e2e`) и frontend audit/lint/tests/build с
проверкой, что встроенный dist соответствует исходникам фронта. CI повторяет
те же проверки отдельными job'ами.
