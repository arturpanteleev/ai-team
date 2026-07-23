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
добавляет `.ai-team/` в `.gitignore` и автоматически включает typed Go-проверки
(`go test -json -count=1` + `go vet`), если находит `go.mod`. Для стеков без
typed adapter (Rust, Python, Node, неизвестный) `init` выводит warning:
delivery остаётся запрещённым, пока вы не настроите required unit/integration
check вручную (см. [«Конфигурация»](#конфигурация)).

```bash
ai-team run --feature add-jwt-auth --task "Реализовать JWT авторизацию"
```

Это самый частый способ запуска: в интерактивном терминале checkpoints
(`analyst`, `architect`) сами спросят подтверждение перед продолжением.

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

3. **Второй запуск** передаёт SHA-256 именно этого плана — и только тогда
   контроллер выполняет commit, push и создаёт PR:

   ```bash
   ai-team run --feature add-jwt-auth --retry-from deployer \
     --approve-gates --approve-plan <sha256-из-шага-1>
   ```

   Если план изменился (другой коммит поверх, другие файлы) — старый SHA-256
   не подойдёт, и контроллер откажется выполнять delivery. Это осознанное
   поведение, а не баг: подтверждение одноразовое и привязано к конкретному
   плану.

4. **Проверьте результат** — `ai-team web` открывает дашборд со статусом
   запуска, деталями по каждому этапу и артефактами; сырые evidence того же
   run лежат в `.ai-team/runs/{run_id}/` (см.
   [«Evidence и наблюдаемость»](docs/ARCHITECTURE.md#evidence-и-наблюдаемость)),
   если поднимать web не хочется.

Если что-то пошло не так раньше (например, reviewer вернул
`CHANGES_REQUESTED`), pipeline завершится с exit code `1`, и `--retry-from`
позволит перезапустить с конкретного этапа, не проходя всё заново.

## CLI-справочник

| Команда | Назначение |
|---|---|
| `ai-team init [--target <dir>]` | создать `.ai-team/config.yaml`, каталоги artifacts/reports/logs |
| `ai-team run --feature <name> --task "<desc>" [...]` | провести фичу через конвейер |
| `ai-team list` | список доступных агентов (имя, runtime, источник в layered registry) |
| `ai-team eval --agent <name> --artifact <path> [--samples N]` | независимая LLM-оценка артефакта |
| `ai-team web [--port 8080] [--host 127.0.0.1]` | dashboard на localhost |
| `ai-team version` / `ai-team help` | версия / usage |

Флаги `run`: `--feature`, `--task`, `--target` (по умолчанию `.`),
`--retry-from <agent>`, `--approve-gates`, `--approve-plan <sha256>`.

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

`ai-team init` создаёт строгий schema v3 config:

```yaml
schema_version: 3
pipeline:
  - name: analyst
    checkpoint_after: require_explicit
  - name: architect
    checkpoint_after: require_explicit
  - name: coder
    max_retries: 2
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
cli: opencode
effort: medium
stage_timeout: 30m
```

Unknown/duplicate YAML-поля, несколько документов, неподдерживаемые
schema/CLI, невалидные переходы, пути, checks или loopback targets отклоняются
до первого LLM-вызова. Как именно резолвятся агенты между
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

- **checkpoint** — точка в pipeline после этапа (обычно `analyst`,
  `architect`), где нужно явное решение продолжать. Политика — `auto_continue`,
  `interactive` или `require_explicit`; в non-interactive среде без
  `--approve-gates` требующий подтверждения checkpoint fail-closed.
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

`make verify` выполняет строгую OpenSpec-валидацию, module verification, vet,
govulncheck, race tests, frontend audit/lint/tests/build. CI дополнительно
отдельными job'ами проверяет gofmt и coverage gate 60% (`make test-coverage`) —
если меняете код, перед PR стоит запустить их локально отдельно, `make
verify` их не покрывает.
