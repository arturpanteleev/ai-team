# Участие в разработке ai-team

Этот файл — для человека (или агента вне Claude Code), который хочет
предложить изменение в сам ai-team. Он описывает **процесс**. Если вы работаете
через Claude Code, разработка ai-team, а не пользователей ai-team, поэтому
инструкции для агента лежат в [`CLAUDE.md`](CLAUDE.md) (маппинг на конкретные
инструменты); этот файл — тот же цикл, но без привязки к инструменту.

## Процесс

Обычный путь изменения: issue → ветка → PR с `Closes #<номер>` → зелёный CI →
merge. Ничего сверх этого не требуется.

### OpenSpec — инструмент, а не обязанность

Репозиторий использует [OpenSpec](https://github.com/Fission-AI/OpenSpec):
`openspec/specs/` содержит принятые контракты (по одному капабилити на
директорию), `openspec/changes/` — проектную проработку изменений.

Пользоваться этим **необязательно**. Заводить change имеет смысл, когда
изменение того стоит:

- затрагивает несколько подсистем или меняет контракт между ними;
- требует сравнения альтернатив, которое кто-то потом будет оспаривать;
- меняет нормативное поведение, о котором важно договориться до кода.

Для обычной правки — исправления, теста, рефакторинга, документации, небольшой
фичи — достаточно issue и PR.

Если change заводится, артефакты (`proposal.md`, `design.md`, `specs/`,
`tasks.md`) пишутся по мере надобности. Строгого порядка и подтверждения на
каждом шаге нет: пишите то, что действительно помогает. Учтите только, что
change без хотя бы одной delta-спеки не проходит `make specs`, поэтому
незавершённый change лучше не коммитить.

### Что остаётся обязательным

**Если ваш PR изменил нормативное поведение, `openspec/specs/` не должен ему
противоречить.** Как именно вы это сделаете — правкой спеки напрямую или через
полноценный change с последующим `openspec archive` — ваше дело.

Исключение, для которого ничего делать не нужно: правки, не меняющие
наблюдаемое поведение — опечатки, тесты на уже специфицированное поведение,
рефакторинг без изменения контракта, документация.

### Цикл OpenSpec, когда вы им пользуетесь

1. **Explore** — прочитать релевантный код, уточнить требования.
2. **Propose** — `openspec/changes/<name>/proposal.md`: зачем (Why), что
   меняется (What Changes), какие капабилити затронуты (Capabilities), что
   задето (Impact).
3. **Design** — `design.md`: контекст, Goals/Non-Goals, решения с
   рассмотренными альтернативами, риски, migration plan.
4. **Specs** — `specs/<capability>/spec.md` с ADDED/MODIFIED Requirements.
   Каждый Requirement — на одной строке с MUST/SHALL (валидатор проверяет это
   буквально, перенос на новую строку ломает парсинг) и минимум одним
   `#### Scenario:` блоком в формате КОГДА/ТОГДА.
5. **Tasks** — `tasks.md`: чеклист шагов реализации.
6. **Apply** — реализация.
7. **Archive** — `openspec archive <name> -y` сливает delta в
   `openspec/specs/` и переносит change в `openspec/changes/archive/`.

Шаги можно пропускать. Слэш-команды `/opsx:*` в Claude Code и OpenCode
покрывают тот же цикл.

### Runtime-инвариант продукта

К процессу разработки отношения не имеет и не ослабляется: подтверждение
checkpoint во время `ai-team run` никогда не разрешает delivery —
commit/push/PR выполняет контроллер только после deterministic checks и
approval точного SHA-256 canonical plan (`--approve-plan`). Это два независимых
уровня контроля: один над тем, как разрабатывается сам ai-team, другой над тем,
что делает собранный ai-team с пользовательским репозиторием.

### Валидация

```bash
make specs
```

запускает `openspec validate --all --strict --no-interactive` — строгую
проверку структуры и формата всех capability-спек и незаархивированных changes.

### Issue и change

Issue — короткий указатель на задачу. Если по задаче есть change, источником
истины является он, а issue его **не повторяет**: то, что нигде не
продублировано, разойтись не может. Если change нет, описание живёт в issue и в
PR. PR закрывает issue строкой `Closes #<номер>`.

## Локальная разработка

### Предварительные требования

| Инструмент | Минимально | Зачем |
|---|---|---|
| Go | 1.26.5+ | сборка и тесты ядра |
| Node.js + npm | 22+ (npm 10+) | сборка/тест web-фронтенда (`web/`) |
| OpenSpec CLI | через `npx` автоматически | строгая валидация specs (`make specs`) |
| `opencode` | в `PATH` | только для запуска полного `ai-team run` (LLM-артефакты) |

Go-модуль и веб-фронтенд — два независимых сопрягаемых блока:
`cmd/` + `pkg/` собираются как обычный Go-бинарник, а `web/` — отдельный
npm-проект, чья прод-сборка (`web/dist`) встраивается в бинарник через
`go:embed` (`embed_agents.go` встраивает и `agents/`).

### Быстрый старт разработчика

```bash
# 1. Ядро
make build                  # go build -o bin/ai-team ./cmd/ai-team
make test                   # go test ./...
make test-coverage          # с coverage-гейтом 60%

# 2. Отдельно — только один пакет (быстрее итераций)
go test ./pkg/pipeline/...
go test -run TestRun_Loopback ./pkg/pipeline/...

# 3. Web-фронтенд (отредактировали web/src → пересоберите dist)
cd web && npm ci
npm run lint && npm test && npm run build

# 4. Полная проверка (всё, что гоняет CI локально)
make verify
```

> После изменения `web/src` обязательно выполните `npm run build` и
> закоммитьте обновлённый `web/dist` — CI проверяет, что встроенный dist
> соответствует свежей сборке (`git diff --exit-code -- dist` в job `frontend`).

### E2E-тесты через mock-opencode

`e2etest/` запускает реальные subprocess-сценарии поверх `mock-opencode.sh`
(фейковый LLM), поэтому не требует API-ключей:

```bash
make test-e2e              # go test -run TestE2E ./e2etest/...
```

> Используйте `go test -count=1 ./...`, а не голый `go test ./...`: некоторые
> пакеты (`e2etest`) не импортируют движок напрямую, и Go-кэш тестов не
> инвалидируется автоматически при изменении `pkg/runtime` и т.п. — только
> `-count=1` форсирует реальный перезапуск.

## Make-таргеты

```bash
make build           # сборка cmd/ai-team
make test            # go test ./... (все пакеты)
make test-coverage   # go test с coverage gate 60%
make test-e2e        # e2etest/ — mock-opencode + subprocess-level сценарии
make specs           # строгая OpenSpec-валидация
make verify          # specs + mod verify + vet + govulncheck + race tests +
                     # frontend audit/lint/tests/build
make clean           # очистка build-артефактов
```

`make verify` — это полная проверка, как её гоняет CI: она **включает**
gofmt-проверку (через `gofmt -l .`), строгую OpenSpec-валидацию, `go mod
verify`, `go vet`, `govulncheck`, race-тесты, coverage gate 60% (`make
test-coverage`), E2E (`make test-e2e`) и frontend audit/lint/tests/build с
проверкой, что встроенный `web/dist` соответствует исходникам фронта. Перед PR
достаточно прогнать локально `make verify`; если какая-то проверка не пройдена,
именно она указывает, что доработать (команды покрыты отдельными шагами в
[Make-таргеты](#make-таргеты)).

Один пакет или один тест:

```bash
go test ./pkg/pipeline/...
go test -run TestRun_Loopback_DefaultTargetIsMetadataDrivenNotNamedCoder ./pkg/pipeline/...
```

Если ваше изменение может повлиять на поведение, которое `e2etest/` проверяет
на уровне реального subprocess (например, что видит `opencode` в окружении),
используйте `go test -count=1 ./...`, а не голый `go test ./...`: `e2etest`
статически не импортирует пакеты вроде `pkg/runtime`, поэтому Go test cache не
инвалидируется автоматически при их изменении — только `-count=1` форсирует
реальный перезапуск.

## Как добавить нового built-in агента

Built-in агенты лежат в `agents/{name}/` и встраиваются в бинарник через
`go:embed all:agents` (`embed_agents.go`) — новая директория подхватывается
автоматически, без отдельной регистрации файлов.

1. Создайте `agents/{name}/def.yaml`:

   ```yaml
   name: my-agent
   description: Короткое описание роли
   runtime: agentcli
   cli: opencode
   prompt_file: prompt.md
   mutation: none        # допустимо: none | source | tests | external
   # (source/tests требуют allowed_paths, external — только kind: delivery)
   verdict:
     required: true
     marker: Verdict
     values: [APPROVED, CHANGES_REQUESTED]
   inputs:
     specs: '{feature}/specs'
   outputs:
     review: '{feature}/my-report.md'
   ```

   Значение `mutation` проверяется контроллером: `scope` **не является**
   допустимым режимом, валидатор принимает только `none`, `source`, `tests` и
   `external` (см. `validateDefinition` в `pkg/agent/registry.go`). Если этап
   должен иметь право менять код, используйте `source` или `tests` и объявите
   разрешённые пути в `allowed_paths`.

2. Создайте `agents/{name}/prompt.md` — системный промпт агента.

3. Если агент должен быть частью **дефолтного** конвейера, добавьте его имя в
   `Registry.DefaultPipeline()` (`pkg/agent/registry.go`) — это единственное
   место, которое перечисляет built-in порядок стадий; `config.Default()`
   строит имена стадий из этого списка, а не дублирует его.

4. Проверьте: `ai-team list` должен показать нового агента с источником
   `embedded`; `go test ./pkg/agent/... ./pkg/config/...` — что registry и
   default-конфиг видят его корректно.

Как именно резолвятся конфликты между project/plugin/user/built-in слоями —
см. [ARCHITECTURE.md](docs/ARCHITECTURE.md#layered-agent-registry).

## Slash-команды `/opsx:*`

Explore/propose/apply/archive/sync цикла выше доступны как команды в обоих
основных агентских CLI:

- Claude Code: `.claude/commands/opsx/*.md`, вызываются как `/opsx:explore`,
  `/opsx:propose`, `/opsx:apply`, `/opsx:archive`, `/opsx:sync`.
- OpenCode: `.opencode/commands/opsx-*.md` + `.opencode/skills/openspec-*/`.

Оба набора реализуют один и тот же цикл; при изменении шагов цикла
поддерживайте синхронизацию между обоими каталогами.

## Onboarding-observations

Чтобы улучшать первый опыт, мейнтейнеры записывают и публикуют обезличенные
onboarding-наблюдения внешних участников. Процесс такой:

- Приглашается внешний разработчик (не участник проекта) выполнить один из
  runnable-сценариев README (например, быстрый старт).
- С согласия участника фиксируются: **время** до результата/стоп-точки,
  **где именно** он остановился (шаг, команда, ошибка) и **что получилось**.
- Наблюдение публикуется **без личных данных** (имя, контакты, содержимое
  непубличных репозиториев) в этом разделе.
- Утверждения вида «первая фича за N минут» не публикуются, пока не измерены —
  наблюдаемая длительность приписывается конкретным наблюдениям, а не
  рекламируется как гарантия.

> Раздел пока **не заполнен**: внешние onboarding-прогоны ещё не проводились.
> Цель — минимум три обезличенных наблюдения; как только они появятся, они
> публикуются здесь по указанному процессу.
