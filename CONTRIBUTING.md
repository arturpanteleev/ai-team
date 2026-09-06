# Участие в разработке ai-team

Этот файл — для человека (или агента вне Claude Code), который хочет
предложить изменение в сам ai-team. Он описывает **процесс**. Если вы работаете
через Claude Code, разработка ai-team, а не пользователей ai-team, поэтому
инструкции для агента лежат в [`CLAUDE.md`](CLAUDE.md) (маппинг на конкретные
инструменты); этот файл — тот же цикл, но без привязки к инструменту.

## Репозиторий разрабатывается spec-first, через OpenSpec

Любая продуктовая фича — не только код, но и её нормативное поведение —
проходит через [OpenSpec](https://github.com/Fission-AI/OpenSpec):
`openspec/specs/` содержит принятые контракты (по одному капабилити на
директорию), `openspec/changes/` — активные, ещё не заархивированные
изменения. Правило простое: **если после вашего PR поведение системы стало
другим, где-то в `openspec/` должен появиться соответствующий delta**, до или
вместе с кодом, а не постфактум.

Исключение — правки, которые ничего не меняют в наблюдаемом поведении:
опечатки, добавление теста на уже специфицированное поведение, рефакторинг без
изменения контракта, документация (как этот файл). Для них formal change не
нужен.

### Цикл

1. **Explore** — прочитать релевантный код, задать вопросы, уточнить
   требования. Ничего не пишется в `openspec/` на этом шаге.
2. **Propose** — создать `openspec/changes/<name>/proposal.md`: зачем (Why),
   что меняется (What Changes), какие капабилити новые/модифицированные
   (Capabilities), что затронуто (Impact).
3. **Design** — `design.md`: контекст, Goals/Non-Goals, ключевые решения с
   рассмотренными альтернативами, риски, migration plan.
4. **Specs** — `specs/<capability>/spec.md` с ADDED/MODIFIED Requirements.
   Каждый Requirement — на одной строке с MUST/SHALL (валидатор проверяет это
   буквально, перенос на новую строку ломает парсинг) и минимум одним
   `#### Scenario:` блоком в формате КОГДА/ТОГДА.
5. **Tasks** — `tasks.md`: чеклист конкретных шагов реализации.
6. **Apply** — реализация по tasks.md.
7. **Archive** — `openspec archive <name> -y` сливает spec delta в
   `openspec/specs/` и переносит change в `openspec/changes/archive/`.

### Gate rule

Ни один следующий артефакт (design после proposal, specs после design, tasks
после specs, реализация после tasks) не пишется без явного подтверждения от
человека, который ведёт review. Единственное исключение — когда ревьюер прямо
сказал делать всё сразу.

Это правило про **обычные checkpoints** цикла разработки. Оно не пересекается
с runtime-инвариантом ai-team как продукта: подтверждение checkpoint во время
`ai-team run` никогда не разрешает delivery — commit/push/PR выполняет
контроллер только после deterministic checks и approval точного SHA-256
canonical plan (`--approve-plan`). Это два независимых уровня контроля: один
— над тем, как разрабатывается сам ai-team, другой — над тем, что делает уже
собранный ai-team с пользовательским репозиторием.

### Валидация

```bash
make specs
```

запускает `openspec validate --all --strict --no-interactive` — строгую
проверку структуры и формата всех capability-спек.

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
