# Design: control-plane-hardening

## Context

Pipeline сейчас одновременно выполняет state transitions, CLI execution,
filesystem validation, prompting, report generation и persistence. Доменная
модель находится в notifier/runtime packages, а outcome повторно интерпретируется
каждым presentation layer. Artifact tree привязан к feature, а не к run.

## Goals

- Fail-closed control contracts.
- Immutable evidence и воспроизводимая история.
- Одна state model для всех projections.
- Явные capabilities и side-effect policy.
- Переиспользуемое workflow-ядро, не привязанное к семи SDLC-ролям.
- Инкрементальная миграция без микросервисов и внешней БД.

## Architecture

```text
Workflow Definition -> Validator -> State Machine -> Stage Executor
                                             |-> Agent CLI adapter
                                             |-> Command runner
                                             `-> Delivery executor

Stage Executor -> Artifact/Evidence Store -> Append-only Events
                                      Events -> Console/HTML/SQLite/Web
```

## Decisions

### D1. Разделённая доменная модель

Вводятся независимые поля:

- execution: pending/running/succeeded/infra_failed/timed_out/canceled;
- decision: not_applicable/approved/rejected/blocked/waived;
- outcome: passed/failed/rejected/blocked/canceled/skipped/warning.

Итоговый run outcome вычисляется state machine один раз. Reporters и store не
имеют права самостоятельно решать, был ли run успешен.

### D2. Declarative stage contracts

Agent definition объявляет outputs, verdict contract, mutation scope,
capabilities, checkpoint policy и deterministic checks. Эвристики по имени
агента удаляются.

### D3. Run/attempt identity

Каждый run получает sortable opaque ID. Каждый повтор stage получает attempt.
Live workspace может оставаться удобным для агента, но evidence каждой попытки
копируется/публикуется атомарно в immutable run directory и описывается manifest.
При retry downstream evidence считается invalidated.

### D4. Artifact contract

ArtifactRef содержит logical name, path, producer, run/attempt, size, modtime и
SHA-256. Output проходит confinement, freshness и объявленную contract validation,
после чего атомарно публикуется. Структурная семантика markdown контролируется
verdict contract; универсальный запрет пустых файлов не вводится без declaration.

### D5. Structured control result

Verdict-bearing stage создаёт единственный control result по схеме. На первом
этапе сохраняется совместимость с markdown marker, но controller требует ровно
один допустимый marker. Следующий migration step — `stage-result.json`, где
markdown остаётся human report, а JSON является control channel.

### D6. Checkpoints fail-closed

Policy задаёт `interactive`, `require_explicit`, `auto_continue`. В non-TTY
`require_explicit` возвращает policy-denied, если отсутствует заранее переданное
approval. Delivery всегда требует explicit policy.

### D7. Deterministic checks

Command runner запускает настроенные команды с timeout и сохраняет evidence.
LLM tester/reviewer не заменяют exit codes инструментов.

### D8. Delivery as transaction log

Контроллер создаёт delivery plan только из атрибутированных mutations актуальных
attempts. Executor проверяет baseline/diff/file allowlist,
branch и approvals, затем по шагам записывает commit SHA, push ref и PR URL.
Повторный запуск продолжает с подтверждённого шага.

### D9. Append-only events

Lifecycle сохраняется как события с run/attempt IDs. SQLite и HTML являются
projections. Это исключает расхождение статусов и позволяет reconciliation после
аварийного завершения.

### D10. Security boundaries

Target и artifact paths абсолютные и confined. Web bind по умолчанию localhost;
symlink traversal запрещён; responses ограничены по размеру. Stage capabilities
ограничивают разрешённые мутации и внешние действия.

## Migration Order

1. Закрыть false-green в текущем layout.
2. Сделать OpenSpec/CI валидными.
3. Ввести identity, manifest и evidence без смены UX CLI.
4. Выделить state machine/domain packages.
5. Добавить command checks и delivery executor.
6. Перевести reports/web/evals на события и immutable evidence.

## Risks

- Большой migration diff. Митигация: каждый шаг сохраняет работающий CLI и
  покрывается contract/E2E tests.
- Совместимость старых `.ai-team` каталогов. Митигация: read-only legacy import и
  явная schema version.
- Разные CLI имеют разные invocation semantics. Митигация: отдельные adapters,
  неизвестный CLI не получает неподдерживаемые аргументы.
- Dirty workspace. Митигация: baseline manifest и policy, а в строгом профиле —
  isolated git worktree.
