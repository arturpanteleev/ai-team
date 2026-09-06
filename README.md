# ai-team

[![CI](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml/badge.svg)](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml)
[![Docs](https://img.shields.io/badge/docs-site-blue)](https://arturpanteleev.github.io/ai-team/)
[![Release](https://img.shields.io/github/v/release/arturpanteleev/ai-team)](https://github.com/arturpanteleev/ai-team/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/arturpanteleev/ai-team)](https://go.dev/dl/)
[![License](https://img.shields.io/github/license/arturpanteleev/ai-team)](LICENSE)

Локальный control plane для решения IT-задач цепочкой AI-агентов. LLM создаёт
артефакты (proposal, design, код, тесты, review) и предлагает вердикты;
переходы между этапами, проверки, mutation scopes, evidence и delivery
исполняет детерминированный Go-контроллер — **не LLM**.

AI-агенты — это «мозг» с ограниченными «руками», а delivery контролирует
контроллер. Здесь три разных зоны ответственности:

1. **Candidate и артефакты** агенты (coder/tester и др.) записывают в рамках
   разрешённого mutation scope *до* человеческого подтверждения; результаты
   затем проходят review, тесты и verification.
2. **Проверки и evidence** исполняет детерминированный контроллер: scope guard,
   typed checks, immutable records — не LLM.
3. **Delivery (commit/push/PR)** — controller-owned: оно выполняется только
   после детерминированных проверок и явного подтверждения человеком точного
   canonical delivery-плана по его SHA-256.

Публичный сайт документации: **<https://arturpanteleev.github.io/ai-team/>**

Если вы новый читатель, читайте разделы по порядку: этот README проведёт вас
от установки до первой поставленной фичи. Глубокое описание внутреннего
устройства — в [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); процесс участия в
разработке самого ai-team — в [CONTRIBUTING.md](CONTRIBUTING.md).

## Для кого этот инструмент

**Основной пользователь** — соло-maintainer или небольшая команда (1–5
человек), которая ведёт один доверенный репозиторий и хочет, чтобы каждое
изменение проходило детерминированный путь с явным человеческим approval:
идея → proposal → код → тесты/review → верификация → delivery. Обычная боль,
которую закрывает ai-team: при использовании coding-агента трудно быть
уверенным, что между «агент написал код» и «код попал в main» действительно
был строгий review и проверки, а не доверие «похоже, ок». ai-team превращает
это в обязательный протокол.

Один узкий **pain→result** сценарий:

- **Боль:** вы даёте кодирующему агенту задачу, и он напрямую коммитит с
  частично проверенным кодом; отдельной стадии ревью, тестов и верификации по
  контракту нет.
- **Результат с ai-team:** агент проходит `analyst → architect → coder →
  reviewer → tester → verifier`, каждая смысловая стадия фиксирует verdict
  marker, `deployer` не исполняет произвольные команды — он выполняет ровно
  **canonical delivery plan**, который контроллер построил сам, и только после
  того, как вы подтвердили точный SHA-256 этого плана через `--approve-plan`.
  Доставка (commit/push/PR) выполняется контроллером, а не LLM.
- **Ожидаемый результат:** PR открыт с проверенным артефактом; evidence
  каждого этапа лежит в `.ai-team/runs/<run_id>/`.
- **Границы (что НЕ делает):** ai-team не является sandbox — агент и команды
  проверок исполняются с правами вашего OS-пользователя (см.
  [«Граница безопасности»](#граница-безопасности)); он не проверяет общее
  качество кода за вас и не является benchmark-сравнением со скоростью или
  качеством других инструментов — это control plane для утверждённого процесса.

**Не подходит (пока):** недоверенный или сторонний код, секреты, к которым
агент не должен иметь доступ, production delivery без человеческого review.
Система пока не является hermetic sandbox: см. раздел
[«Граница безопасности»](#граница-безопасности).

## Предварительные требования и установка

| Зависимость | Зачем | Проверка |
|---|---|---|
| Go 1.26.5+ | сборка и запуск `ai-team` | `go version` |
| Один из CLI-рантаймов в `PATH` (opencode / codex / claude) | LLM runtime, который вызывают агенты | `opencode --version` / `codex --version` / `claude --version` |
| [`gh`](https://cli.github.com) CLI, авторизованный (`gh auth login`) | deployer использует его для `pr create`/`pr view` | `gh auth status` |

Рантайм выбирается полем `cli` в `.ai-team/config.yaml` (`opencode` — значение
по умолчанию). Каждый рантайм устанавливается и настраивается независимо от
ai-team:

- **OpenCode** — [opencode.ai/install](https://opencode.ai/install) (например,
  `curl -fsSL https://opencode.ai/install | bash`) + настройка минимум одного
  LLM-провайдера, см. [opencode.ai/docs](https://opencode.ai/docs).
- **Codex** — OpenAI Codex CLI: [github.com/openai/codex](https://github.com/openai/codex).
- **Claude Code** — [docs.anthropic.com](https://docs.anthropic.com/en/docs/claude-code).

Как передать provider credentials в изолированный runtime — в разделе
[«Runtime и credentials»](#runtime-и-credentials).

`gh` нужен только на шаге delivery (последний агент, `deployer`); если вы не
планируете, чтобы контроллер сам открывал PR, шаги до этого работают без
него.

Установка `ai-team`:

```bash
go install github.com/arturpanteleev/ai-team/cmd/ai-team@latest
```

## Runtime и credentials

Агентный runtime запускается в **изолированном окружении**: у каждого рантайма
свой временный config home (у OpenCode — `XDG_CONFIG_HOME`, у Codex —
`CODEX_HOME`, у Claude — `CLAUDE_CONFIG_DIR`), создаваемый на время запуска.
В субпроцесс передаются только базовые OS/locale переменные (`PATH`, `HOME`,
`LANG`, `TMPDIR`, ...) плюс **имена** переменных, явно разрешённых opt-in.

Поэтому наличие API-ключа в вашем shell-окружении **не означает**, что агент
его увидит: если имя переменной не добавлено в allow-list, run оборвётся на
auth, хотя standalone CLI в том же shell работает. Чтобы передать провайдерский
credential, разрешите его **по имени** через `AI_TEAM_HARNESS_ENV_ALLOW`
(список имён через запятую):

```bash
export ANTHROPIC_API_KEY='<YOUR_API_KEY_HERE>'
AI_TEAM_HARNESS_ENV_ALLOW=ANTHROPIC_API_KEY ai-team run --feature add-jwt-auth --task "…"
```

Значение переменной никуда не пробрасывается автоматически: в субпроцесс
попадает только сам факт «переменная с разрешённым именем существует в
окружении родителя». Значение никогда не логируется и не пишется в evidence —
гарантию даёт allow-list по имени, а не публикация секрета. Устаревший алиас
`AI_TEAM_OPENCODE_ENV_ALLOW` работает так же ради обратной совместимости.

Per-runtime заметки:

| Рантайм | CLI-бинарник (config `cli:`) | Выбор модели | Ожидаемый preflight |
|---|---|---|---|
| OpenCode | `opencode` (по умолчанию) | `-m <model>` / `auto` | `opencode --version`, наличие provider-credentials и их allow-list, Git-repository |
| Codex | `codex` | `-m <model>` / `auto` | `codex --version`; аутентификация Codex (access token / `CODEX_API_KEY`), sandbox `workspace-write` |
| Claude Code | `claude` | `--model <model>` / `auto` | `claude --version`; `ANTHROPIC_API_KEY` (или подписка), `--permission-mode acceptEdits` |

Точные флаги запуска и политики изоляции каждого адаптера — в
[ARCHITECTURE.md](docs/ARCHITECTURE.md) и в исходниках `pkg/runtime/{opencode,codex,claude}.go`.
Если у run нет авторизованного provider (или вы просто хотите попробовать без
LLM), прогоните no-LLM demo: `bash docs/demo/run-demo.sh` — см.
[docs/demo/README.md](docs/demo/README.md).

## Быстрый старт

Подготовка проекта — **один вызов**:

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

Запустите фичу: от идеи до готового к доставке кода. Количество запусков
зависит от окружения:

- **Non-TTY** (CI, скрипт): контроллер останавливается перед delivery с exit
  code `3`, печатая canonical plan и его SHA-256; подтверждение — отдельный
  `--resume` со строкой `--approve-plan` того же SHA-256.
- **Интерактивный TTY**: `authorizeDelivery` спрашивает «Продолжить commit/
  push/PR? [y/N]» и после `y` доставляет в том же первом процессе — exit `3`
  перед delivery не возникает. Отказ (`n`) останавливает run (delivery
  отклонён человеком).

Оба пути подтверждают **ровно тот** canonical plan, который показал контроллер
(другой SHA-256 не подойдёт), — и только тогда будут созданы commit/push/PR.

```bash
# Non-TTY: первый запуск проводит фичу по конвейеру, останавливается перед
# delivery и печатает canonical plan + его SHA-256 (exit code 3).
ai-team run --feature add-jwt-auth --task "Реализовать JWT авторизацию"
```

```bash
# Non-TTY: подтвердить именно тот план, что показал контроллер (другой
# SHA-256 не подойдёт), — и только тогда будет создан commit/push/PR.
ai-team run --resume <run_id> --approve-plan <sha256-из-шага-1>
```

В интерактивном терминале checkpoints спрашивают ваше решение сами, без
флагов `--approve-*`.

Обратите внимание на **forward approvals**: профиль по умолчанию `standard`
(как и `fast`) *откладывает* подтверждения на смысловых рёбрах конвейера до
момента delivery — вы подтверждаете их одним consolidated delivery-решением.
Только профиль `regulated` спрашивает approval на каждом checkpoint пошагово.
После доставки результат виден на дашборде (`ai-team web`) или в директории
`.ai-team/runs/<run_id>/`.

Полный путь с пояснением каждого шага — в разделе
[«Как поставить фичу от начала до конца»](#как-поставить-фичу-от-начала-до-конца).

## Как поставить фичу от начала до конца

Ключевой момент: **delivery подтверждается отдельно и всегда** — commit/push/PR
выполняет только контроллер после явного подтверждения человеком точного
canonical plan (SHA-256). Как именно записывается подтверждение, зависит от
окружения.

**Сценарий A — non-TTY (CI, скрипт): два запуска.**

1. **Первый запуск** проводит фичу через весь конвейер до `deployer` и
   останавливается перед delivery с exit code `3`, напечатав canonical delivery
   plan и его SHA-256:

   ```bash
   ai-team run --feature add-jwt-auth \
     --task "Реализовать JWT авторизацию"
   ```

2. **Прочитайте план.** Он перечисляет ровно те файлы, которые будут
   закоммичены, ветку и сообщение коммита. Это единственный момент, где стоит
   остановиться и проверить, что candidate действительно то, что вы ожидали.

3. **Продолжение того же run** передаёт SHA-256 именно этого плана — и только
   тогда контроллер выполняет commit, push и создаёт PR из сохранённого
   candidate-worktree:

   ```bash
   ai-team run --resume <run_id> --approve-plan <sha256-из-шага-1>
   ```

**Сценарий B — интерактивный TTY: один процесс, delivery после `y`.**

Контроллер сам консолидирует deferred forward approvals и при достижении
`deployer` печатает canonical plan и спрашивает
«Продолжить commit/push/PR? [y/N]». Ответ `y` записывает persisted approval с
subject = SHA-256 показанного плана и ролью `release_manager` — и delivery
выполняется в **том же** первом процессе, без `--resume` и без exit `3`.
Ответ `n` останавливает run: delivery отклонён человеком.

В обоих сценариях delivery approval — обычная persisted approval с subject =
SHA-256 плана и ролью `release_manager`. То же решение можно записать без
CLI-resume: через web UI (`POST /decisions`) или командой `ai-team decision`,
после чего достаточно `ai-team run --resume <run_id>`. Если план изменился
(другой коммит поверх, другие файлы) — старый SHA-256 не подойдёт ни одним
из путей, и контроллер откажется выполнять delivery. Это осознанное поведение,
а не баг: подтверждение одноразовое и привязано к конкретному плану.

`--approve-gates` (если он вам нужен) подтверждает **только** pipeline gates в
non-interactive среде — это не delivery approval. Delivery по-прежнему требует
отдельного подтверждения точного плана через `--approve-plan`, interactive
`y`/`n`, web UI или `ai-team decision`. Профили `standard`/`fast` откладывают
forward approvals до момента delivery, поэтому для них consolidated
delivery-решение покрывает и deferred gates; профиль `regulated` спрашивает
approval на каждом checkpoint пошагово, и `--approve-gates`/интерактивные
решения понадобятся на протяжении run до его завершения.

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
| `ai-team init [--target <dir>] [--write-gitignore] [--profile fast\|standard\|regulated]` | создать `.ai-team/config.yaml`, каталоги artifacts/reports/logs; по умолчанию использовать локальный Git exclude; профиль по умолчанию — `standard` (см. [«Профили init»](#профили-init)) |
| `ai-team run --feature <name> --task "<desc>" [...]` | провести фичу через конвейер |
| `ai-team decision --run <id> --approval <id> --actor <id> --role <role> --action <action> --subject <sha256>` | записать точное решение человека |
| `ai-team auth-token --actor <id> --roles <csv> [--ttl 1h]` | выпустить короткоживущий подписанный token для cloud web |
| `ai-team worker --target <dir> --db <path>` | исполнить один strict worker job из stdin (обычно вызывается launcher-ом) |
| `ai-team list` | список доступных агентов (имя, runtime, источник в layered registry) |
| `ai-team ci-import [--target <dir>] [--format github-actions]` | импортировать ограниченный объяснимый набор checks из project CI (GitHub Actions) без исполнения произвольного YAML; показывает effective suite и fingerprint перед запуском |
| `ai-team redact <verify\|scan\|redact> [--target <dir>] [--run <id>] [--path <rel>] [--out <dir>]` | P1-6 privacy-контракт: secrets-скан evidence (subcommand — перед флагами), fail-closed verify для экспорта или detached-копия с заменой секретов на `[REDACTED:...]` (--out вне source) |
| `ai-team gate --target <dir> --base <ref> --candidate <ref|WORKTREE> [--config <file>] [--out <dir>]` | детерминированный diff-policy вердикт + typed checks + attestation bundle; exit 0/1/2 |
| `ai-team export [--target <dir>] [--out <path>] <run_id>` | собрать проверенный portable bundle терминального run |
| `ai-team verify <bundle-dir>` / `verify --target <dir> <run_id>` | самодостаточная проверка run-/gate-bundle или локальной evidence |
| `ai-team deliver --run <run_id> [--target <dir>] [--feature <name>]` | повторить отложенную (deferred) доставку терминального run — commit/push/PR с trailers (run id, runtime identity, attestation digest), однократная запись delivery.json |
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
повторяет тот же gate непосредственно при `Start`: проверяет выбранный CLI
рантайм и его версию, model/provider, credential allow-list и Git repository;
для workflow с delivery дополнительно требует `origin`, `gh` и успешный
`gh auth status`. Значения credentials никогда не попадают в report.

### Контроллер и preflight

CLI `run`, web-контроллер и worker используют один и тот же классификатор
preflight (`pkg/preflight`) и одинаково именуют отсутствующие prerequisites
(`cli`, `model`, `credentials`, `git_repository`, `git_branch`,
`delivery_remote`, `github_auth`); модель для диагностики берётся из
фактически сконфигурированного CLI-рантайма, а не захардкожена. Отличается
только жёсткость применения (зафиксированное различие):

- **web-контроллер (dashboard) и worker** — жёсткий gate: при `Start`
  отсутствие любого `required` checks (в т.ч. `origin`/`gh` для delivery)
  останавливает run до создания артефактов.
- **CLI `run`** — read-only: печатает тот же полный отчёт (runtime и
  delivery-предусловия) до старта дорогого run, но блокирует только
  невозможность запустить runtime вовсе (`cli` check failed). Git/`origin`/`gh`
  — предусловия поздних стадий (delivery), и они fail-closed проверяются на
  самой   delivery-стадии, поэтому CLI не отказывает в локальном run из-за их
  отсутствия. Так `gh` остаётся нужным только на шаге delivery.

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
а маршрут и обязательные человеческие approvals принадлежат рёбрам.

> ⚠️ **Пример, а не полный default.** YAML ниже — сокращённый иллюстративный
> фрагмент, чтобы показать структуру графа и role-based approvals.
> Полный конфиг, который реально создаёт `init`, обычно длиннее: он содержит
> approval-политику **(включая `deferred: true` на forward edges для профилей
> `standard`/`fast`)** для **каждого** passed-ребра (не только `analyst` и
> `reviewer`), loopback-рёбра и `max_visits`. Считайте фактический
> `.ai-team/config.yaml` после `init` единственным источником истины.

### Профили init

`--profile <name>` определяет, с какой частотой человеческого review
собирается workflow и как подтверждаются forward-переходы. Все публичные флаги
`init` — `--target`, `--write-gitignore` и `--profile` — совпадают со справочной
справкой подкоманды (`ai-team init --help`); таблица выше показывает, какой
профиль выбрать для какой задачи.

| Профиль | Стадии | Forward-approvals | Когда выбирать |
|---|---|---|---|
| `standard` (по умолчанию) | полный конвейер `analyst → architect → coder → reviewer → tester → verifier → deployer` | **отложенные (deferred):** все forward-гейты подтверждаются **одним consolidated delivery-решением** в конце, `quorum: any` | сбалансированный режим по умолчанию: одно решение человека на фичу, минимум кликов |
| `fast` | без `verifier` — `reviewer` совмещает ревью и верификацию | **отложенные (deferred):** те же consolidated approvals, меньше стадий и `max_visits` | прототипы и внутренние фичи, где хочется быстрее, но delivery всё равно контролируется человеком |
| `regulated` | полный конвейер как `standard` | **пошаговые (не deferred):** каждый смысловой переход спрашивает человека отдельно, `quorum: all` на рёбрах | рискованные workflow, где нужен review на **каждом** смысловом ребре — один человек на переход, а не одно consolidated решение на всю фичу |

Разница между `consolidated` и `пошаговым` подтверждением принципиальна:
в `standard`/`fast` forward-переходы не паузят run — их approvals откладываются
и разрешаются одним `--approve-plan <sha256>` (или эквивалентным web/decision
решением) вместе с delivery. В `regulated` каждый forward-переход требует
отдельного решения человека до продолжения. `loopback`-рёбра (например,
`reviewer rejected → coder`, `tester → coder`) не откладываются ни в одном
профиле.

Хотите пошаговый контроль — выберите `regulated`:

```bash
ai-team init --profile regulated
```

Сгенерированный `standard`-конфиг помечает свои forward edges `deferred: true`
(`pkg/config/load.go`):

```yaml
edges:
  - from: analyst
    outcome: passed
    to: architect
    approval:
      roles: [product_owner]
      quorum: any
      actions: {approve: architect, reject: $stop}
      deferred: true   # forward-гейт: подтверждается одним consolidated delivery-решением
```

`fast` также объявляет forward edges `deferred: true`; `regulated` — нет
(они остаются пошаговыми, с `quorum: all`). Полный перечень профилей и их
поведения — в [ARCHITECTURE.md](docs/ARCHITECTURE.md).

Ниже — **сокращённый иллюстративный пример** структуры конфига (см. предупреждение
в начале раздела): узел-граф остаётся в `pipeline`, а маршрут и обязательные
человеческие approvals принадлежат рёбрам:

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
- **attempt / run** — `run` — это **идентичность одного pipeline-выполнения под
  одним `run_id`**, а не буквально один запуск команды: run может быть
  приостановлен перед delivery (non-TTY exit code `3`) и **продолжен с тем же
  `run_id`** через `--resume`, поэтому «один run» охватывает и первый запуск, и
  последующие `--resume` того же идентификатора. `attempt` — одна попытка
  конкретного этапа внутри run (loopback создаёт новый attempt, а не
  переиспользует старый).

## Граница безопасности

### integrity vs authenticity

- **Integrity (гарантируется):** и run-, и gate-bundle перепроверяются цепочкой
  хэшей — `ai-team verify` подтверждает, что ни одна запись не изменена после
  создания, и работает самодостаточно (без repo и `.ai-team`).
- **Authenticity (P1-5, DSSE):** bundle могут быть подписаны автором через
  DSSE-envelope (ed25519, stdlib-only, PAE по спецификации in-toto). `ai-team
  export --sign-key <priv>` и `ai-team gate --sign-key <priv>` подписывают
  детерминированный `BundleDigest` и пишут `dsse.json` в bundle. `ai-team
  verify --verify-key <pub>` проверяет подпись fail-closed: задан ключ → подпись
  обязана быть и совпадать; нет ключа → integrity-only проверка как раньше.
  Digest остаётся контролем целостности («не изменено»), подпись добавляет
  доказательство авторства («кто создал»). Ключи передаются через CLI-файлы и
  никогда не попадают в evidence.

### Граница для недоверенного кода
 OpenCode получает app-level deny
для shell/network/tasks, ограниченные edit/read rules и отдельный config home,
но сам процесс агента и команды проверок работают с правами текущего OS-user.
Containment receipt (`containment.json`) честно фиксирует уровень каждой оси
(fs/net/proc/env): trusted-local → PARTIAL (app-level mitigations, не OS-enforced),
strict без OS backend → UNAVAILABLE (fail-closed, gate блокирует untrusted).
Поэтому текущий профиль допустим только для доверенного локального проекта;
секреты и недоверенный код должны запускаться во внешнем container/VM sandbox.
Открытые ограничения и направление работ перечислены в
[SECURITY.md](SECURITY.md).

## Разработка

Проект использует OpenSpec (спецификации в `openspec/specs/`, активные
изменения — в `openspec/changes/`) и приветствует контрибьюторов — процесс
целиком описан в [CONTRIBUTING.md](CONTRIBUTING.md), а правила безопасного
раскрытия уязвимостей — в [SECURITY.md](SECURITY.md).

### Локальная сборка и тесты

Требуются Go 1.26+ (ядро) и Node 22+ (web-фронтенд). См. раздел
«[Локальная разработка](CONTRIBUTING.md#локальная-разработка)»
в CONTRIBUTING.md.

```bash
make build          # сборка bin/ai-team
make test           # go test ./...
make test-e2e       # mock-opencode E2E
make specs          # строгая OpenSpec-валидация
make docs           # генерация сайта документации в docs/_site
make verify         # полная проверка (как CI)
```

### Сообщение об ошибках и предложения

- **Ошибки и фичи** — через [GitHub Issues](https://github.com/arturpanteleev/ai-team/issues)
  (используйте шаблоны).
- **Уязвимости** — приватно, через [SECURITY.md](SECURITY.md), не в публичный issue.
- **Изменения поведения** — spec-first через OpenSpec (см. CONTRIBUTING.md).

#### Минимальный fork → branch → PR для внешнего контрибьютора

Полный процесс описан в [CONTRIBUTING.md](CONTRIBUTING.md) (spec-first). Самый
быстрый безопасный путь для небольшой правки:

```bash
# 1. Fork репозитория на GitHub, затем клонируйте свой fork и добавьте upstream
git clone git@github.com:<you>/ai-team.git
cd ai-team
git remote add upstream https://github.com/arturpanteleev/ai-team.git

# 2. Отдельная ветка от актуального upstream/master
git fetch upstream
git checkout -b fix/my-change upstream/master

# 3. Изменяйте и прогоняйте проверки (языковая правка — только docs; код — см. CONTRIBUTING)
# 4. Push ветки в ваш fork и откройте PR в upstream
git push -u origin fix/my-change
```

Если правка меняет наблюдаемое поведение продукта, она должна сопровождаться
OpenSpec-delta (см. CONTRIBUTING.md) — иначе PR не будет принят.

#### Контакты и владелец

Проект ведётся на GitHub-репозитории
[`arturpanteleev/ai-team`](https://github.com/arturpanteleev/ai-team). Вопросы и
предложения — через [GitHub Issues](https://github.com/arturpanteleev/ai-team/issues);
спрашивайте там, а не по приватным каналам, чтобы ответ видело больше людей.
Уязвимости — только приватно через [SECURITY.md](SECURITY.md). Проект на ранней
стадии относится к обратной связи бережно; подробное «как помочь» — в
[CONTRIBUTING.md](CONTRIBUTING.md), включая `onboarding`-процесс в нём же.

`make verify` выполняет gofmt-проверку, строгую OpenSpec-валидацию, module
verification, vet, govulncheck, race tests, coverage gate 60% (`make
test-coverage`), E2E (`make test-e2e`) и frontend audit/lint/tests/build с
проверкой, что встроенный dist соответствует исходникам фронта. CI повторяет
те же проверки отдельными job'ами.

Публичный сайт документации строится генератором
[`docsgen/`](docsgen/) из тех же Markdown-источников (`README.md`, `docs/`,
`CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, ...) и публикуется на
GitHub Pages при push в `master` (`.github/workflows/pages.yaml`). Релиз
бинарников для нескольких платформ создаётся автоматически по push тега
`v*` через `.github/workflows/release.yaml` (`make release-binaries`).
