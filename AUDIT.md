# Технический аудит ai-team

Дата среза: 2026-07-20. Область: локальный Go control plane, agent runtime,
checks, delivery, evidence, SQLite/web, evals, OpenSpec и CI.

## Итоговый вердикт

**Система заметно усилена, но пока не готова называться high-assurance или
hermetic workflow.** Для доверенного локального репозитория она уже даёт
полезный fail-closed контроль вердиктов, typed Go-test evidence, точный delivery
plan и подробную историю. Для недоверенного кода, секретов, production delivery
или формального утверждения «проверен именно доставленный кандидат» текущих
гарантий недостаточно.

Главные блокеры релиза строгого профиля:

1. Нет OS-level sandbox: агент и verification commands работают с правами
   пользователя на host.
2. Агент меняет live workspace; mutation guard обнаруживает нарушение после
   факта, а не предотвращает его. Проверки также выполняются в live workspace.
3. Нет единого immutable candidate tree/worktree, который последовательно
   проходит review, tests, verification и затем без изменения доставляется.
4. Reviewer/verifier теперь получают controller-owned candidate metadata и
   executed-check summary, но ещё нет immutable candidate tree и машинной трассы
   acceptance criteria.
5. Event log имеет hash chain и typed lifecycle replay, но не
   signature/внешнего якоря; replay ещё не подключён как recovery/source для
   SQLite и side-effect state.

Текущий безопасный positioning: **локальный orchestration/control prototype для
доверенного проекта, с сильными детерминированными гейтами на отдельных
границах, но без изоляции hostile workload.**

## 1. Фактическая архитектура

```text
strict config + layered registry
              |
              v
workflow state machine -> stage runtime -> live target/artifacts
              |                 |
              |                 `-> OpenCode app permissions
              |
              +-> mutation snapshots / verdict parser / checkpoints
              +-> deterministic checks (typed Go test adapter)
              +-> immutable run/attempt evidence + events.jsonl
              +-> canonical delivery plan -> exact git/GitHub executor
              `-> HTML / SQLite / web projections
```

Контроллер владеет переходами, исходами, checks и delivery effects. LLM создаёт
смысловые артефакты и verdict markers, но не должен самостоятельно выполнять
commit/push/PR. Это правильное базовое разделение ответственности.

## 2. Что уже обеспечено кодом

| Область | Реальная гарантия | Ограничение |
|---|---|---|
| Verdict | Ровно один допустимый marker; missing/unknown/duplicate fail closed | Markdown остаётся control channel |
| BLOCKED | Отдельный status signal, обязательная причина, exit code 2, immutable evidence | Нет структурированного общего stage-result schema |
| State | Execution, decision и outcome разделены; typed event replay восстанавливает lifecycle | Execution resume и side-effect recovery не выводятся из ledger |
| Config | Strict YAML, duplicate/unknown field rejection, schema v3, layered registry | Legacy v1/v2 ещё принимаются и мигрируются не во всех аспектах |
| Mutation | Clean Git baseline для нового run; scopes; read-only и require-diff enforcement | Guard detective; изменения происходят в live workspace |
| Checks | argv без shell, timeout, process-group kill, bounded output, tool fingerprint, workspace before/after | Код проекта исполняется без OS sandbox и с inherited environment |
| Tests | Go JSON parser доказывает discovered/passed test cases; `-count=1` | Rust/Python/Node typed adapters отсутствуют |
| Checkpoints | Non-TTY fail closed; reached checkpoint получает subject SHA evidence | `--approve-gates` — blanket approval, не exact approval каждого subject |
| Delivery | Canonical plan SHA; exact files, blobs, modes, parent, message, remote head и PR verification; exact post-commit recovery | Не доказана полнота recovery на каждой I/O boundary и нет fresh remote-base precondition |
| Evidence | Run/attempt IDs, config/workflow/controller identity, SHA/provenance, atomic attempt publication, event hash chain и typed lifecycle replay | Файлы изменяемы владельцем; нет подписи/внешнего якоря и effect-level recovery replay |
| Web | Loopback bind, same-origin WS, bounded/confined artifacts, SQLite migrations, polling fallback | Projection не полна и не является replay engine |
| Eval | Exact parser, samples/statistics, LLM quality advisory, hard suite failures возвращают error | Нет поставляемого calibrated corpus и regression baseline |

## 3. Критические находки

### C-01. Нет настоящей границы исполнения

OpenCode получает deny rules для bash, network tools, tasks, plugins и edits;
`.env`, `.git` и `.ai-team` закрыты для штатного read tool, кроме exact immutable
inputs. Но это политика приложения внутри того же процесса и того же user
account. Она не ограничивает произвольный/скомпрометированный CLI, его дочерние
процессы, filesystem syscalls или сетевой доступ на уровне ОС.

Отдельно deterministic checks намеренно запускают код проекта. Вредоносный test,
compiler plugin, build script или dependency может читать HOME, credentials,
SSH agent, environment, другие проекты и сеть. Сам факт успешного check не
доказывает безопасное выполнение.

Требуемое исправление:

- интерфейс `SandboxBackend` с явными capabilities;
- strict backend на Linux container/bwrap и эквивалентная стратегия для macOS;
- read-only source + отдельный writable candidate/artifact mount;
- deny network по умолчанию, явные host/port grants;
- минимальный environment allowlist и отдельная передача provider credentials;
- CPU/memory/pid/disk/time limits;
- evidence полей backend/image digest/mounts/network/env policy;
- strict profile обязан fail closed, если sandbox недоступен.

До этого нельзя запускать ai-team на недоверенном репозитории или рядом с
ценными секретами.

### C-02. Live workspace не является транзакционным candidate

Агент пишет прямо в target. Controller сравнивает snapshots после выполнения и
может отклонить запрещённую mutation, но повреждение/чтение/утечка уже могли
произойти. Cancel/crash может оставить частичное состояние. Ignored files и
tool-generated caches усложняют ownership.

Нужен isolated candidate на каждый run:

1. Зафиксировать base commit и policy для untracked inputs.
2. Создать отдельный worktree/copy-on-write root.
3. Выполнять все source agents и checks только там.
4. Представлять controller-owned tree/diff downstream stages.
5. После финальной проверки доставлять ровно candidate commit/tree hash.
6. Live target не мутировать; promotion делать отдельной явной операцией.

### C-03. Нет end-to-end доказательства «проверено то, что доставлено»

Сейчас delivery хорошо перепроверяет approved file bytes/modes и workspace
digest. Reviewer получает controller-owned workspace digest, changed paths,
file fingerprints и hash/содержимое bounded tracked patch; tester получает тот
же reviewed identity, а verifier — финальный candidate и actual typed-check
summary. Tester теперь явно только пишет tests и не имеет права выдумывать
execution logs. Однако цепочка остаётся привязана к live workspace, untracked
patch представлен metadata, а schemas ещё не образуют формальную AC trace.

Нужен immutable `candidate.json`:

- base tree, candidate tree/commit и exact patch digest;
- список changed paths/modes/blob SHA;
- executed checks с adapter/tool identity/output digest;
- review/test/verification decisions, связанные с candidate hash;
- delivery plan обязан ссылаться на тот же candidate hash;
- любое изменение candidate инвалидирует все downstream approvals.

## 4. Высокие риски и логические пробелы

### H-01. Cross-run retry не имеет строгой lineage

`--retry-from` читает live artifacts предыдущих стадий. Immutable delivery resume
существует, но общего `--resume-run <run_id>` для source stages нет. После нового
процесса прежние изменения могут стать user-owned, а consumed inputs не обязаны
принадлежать одному одобренному run/attempt graph.

Решение: resume только по immutable run ID, exact attempt IDs, config/workflow
hashes и candidate worktree. Нельзя молча смешивать latest live artifacts.

### H-02. Approval обычных gates слишком широк

Checkpoint events уже имеют subject SHA, но CLI-флаг `--approve-gates`
разрешает все будущие checkpoints запуска. Пользователь не подтверждает exact
subject каждого конкретного gate.

Решение: двухфазный resume с `--approve-gate <gate-id>:<subject-sha>`, actor,
timestamp и reason. Approval должен быть одноразовым и инвалидироваться при
изменении subject.

### H-03. Нет машинной трассы acceptance criteria

Markdown proposal/spec/design/tasks/tests может быть качественным, но controller
не знает ID требований и не проверяет связи:

`AC -> design decision -> task -> test ID -> executed test evidence -> verdict`.

Из-за этого «все тесты зелёные» не означает «каждый acceptance criterion
проверен». Нужны versioned JSON schemas, стабильные IDs, referential integrity и
coverage gate по AC. Narrative Markdown должен остаться projection, не control
source.

### H-04. Event log hash-chained и lifecycle-replayable, но не anchored

`events.jsonl` содержит `previous_sha256`/`sha256`; controller проверяет всю
цепочку перед append и обнаруживает изменение, удаление, вставку и перестановку
records. Typed replay дополнительно проверяет start/finish transitions,
invalidation, terminal status/count и digest каждого attempt manifest. Но
владелец файлов может переписать всю цепочку с новыми hashes: нет terminal
external anchor/signature и durable projector offset. Replay пока не
восстанавливает SQLite и не управляет resume side effects после crash.

Решение: terminal manifest с root hash, replay tests, idempotent projectors и
corruption detection. Для сильной аттестации — подпись или внешний append-only
store.

### H-05. Finalization имеет неоднозначную границу источника истины

HTML report может быть опубликован до terminal `run_finished`. Если запись
terminal event не удалась, уже существует immutable report со старым status.
Ошибка публикации report, terminal event и SQLite projection имеют разные
семантические последствия.

Решение: двухфазный finalize. Сначала подготовить projections, затем атомарно
зафиксировать terminal state/root hash, после этого публиковать projections;
projection health хранить отдельно и не переопределять доменный run outcome.

### H-06. Delivery ещё не полностью транзакционен

Post-commit gap теперь закрывается fail-closed recovery: если branch HEAD ушёл от
baseline, controller заново сверяет commit message, parent, paths, modes и blob
hashes и только после этого записывает `CommitSHA`. Открытые случаи:

- base branch не сверяется с актуальным remote base непосредственно перед push;
- feature-keyed branch/state затрудняет повторное использование feature;
- completed state архивируется, но существующая local/remote feature branch всё
  равно может конфликтовать с новым plan;
- preconditions связаны hash/size/verdict, но не producer run/attempt IDs;
- нет formal compensation/recovery table и fault injection для каждой
  persist/effect boundary, включая ошибки durable directory sync.

Нужны plan-specific branch/state IDs, recovery через обнаружение commit по
signed trailer/tree hash, remote-base lease и fault-injection matrix на каждом
persist/effect boundary.

### H-07. Typed test evidence реализован только для Go

Это честно отражено в init: Rust/Python/Node больше не получают guessed green
profile. Но до появления adapters продукт не является универсальным
multi-stack workflow.

Минимальный набор: pytest JSON/JUnit, Vitest/Jest JSON, Cargo/libtest JSON или
стабильный JUnit bridge. Каждый adapter обязан доказывать discovery, pass/fail,
zero-tests failure, schema validity и exact raw evidence digest.

### H-08. Runtime/toolchain identity неполна

Run manifest связывает controller executable, Go и VCS metadata. Checks пишут
resolved tool path/version. Но OpenCode binary/provider/model identity не
фиксируется как проверенный digest на каждый stage; совместимость permission
schema не probe-ится. `opencode` из PATH может измениться между attempts.

Решение: resolve + hash binary один раз на run, capability/version probe,
allowlisted version range, model/provider identity в attempt manifest и запрет
смены runtime identity внутри run.

### H-09. Data/control separation в agent artifacts слабая

Eval prompt явно оборачивает artifact как untrusted data. Основной runtime
вставляет outputs предыдущих агентов в общий prompt. Это ожидаемый канал
кооперации, но инструкции внутри артефакта могут переопределять роль downstream
агента или скрывать требования.

Нужны typed inputs, отдельные data delimiters, запрет tool instructions в
artifact schema и controller-owned summaries/diffs. Это не устраняет prompt
injection полностью, но делает границу проверяемой.

### H-10. Система всё ещё SDLC/GitHub-специфична

Заявленная цель шире разработки, но встроенный workflow и final effect жёстко
связаны с source/tests/Git/GitHub PR. Для общих IT-задач нужны typed effect
adapters: read-only diagnostics, filesystem patch, ticket update, cloud change,
database migration и т.д.

Каждый adapter должен объявлять `plan schema -> approval -> preconditions ->
execute -> verify -> compensate`, а не предоставлять агенту общий shell.

## 5. Средние риски

### M-01. OpenSpec governance нарушена

Большое число файлов внутри `openspec/changes/archive/` изменено задним числом.
Даже если текст стал точнее, это разрушает ценность архива как исторической
записи. Нельзя считать такой archive immutable evidence.

Правило: архивы не редактируются; исправления оформляются новым change/errata со
ссылкой на исходный requirement и conformance evidence. Текущие изменения нужно
отдельно атрибутировать, а не молча выдавать за первоначальную историю.

### M-02. Evals не имеют поставляемого calibrated corpus

Layer API корректно различает hard и advisory; hard failures теперь возвращают
error. Но suite состоит из переданных функций, без versioned задач, golden
outcomes, seed/model metadata, inter-rater agreement и regression thresholds.

Нужны реальные false-green/fault cases и отдельный prompt-quality benchmark.
LLM judge остаётся advisory до калибровки на human-labelled dataset.

### M-03. CI supply chain и permissions

Workflow использует mutable action tags (`@v4`, `@v5`) и не фиксирует
least-privilege permissions явно. Aggregate coverage gate 60% допускает слабое
покрытие safety-critical packages. Downloaded `npx`/`go run` tools требуют
отдельной provenance policy.

Нужно pin actions по commit SHA, задать `permissions: contents: read`, включить
dependency review/SBOM при необходимости и ввести per-package thresholds для
pipeline/delivery/evidence/checks/safeio/runtime.

### M-04. Cross-platform filesystem semantics не формализованы

Нормализация paths не закрывает case-insensitive collisions, Unicode aliases,
Windows device names и filesystem-specific executable behavior. Unix lock
проверяет regular inode, owner и single hard link; fallback platforms имеют
другую семантику.

Либо официально ограничить strict profile конкретными OS/filesystem, либо
добавить platform-specific canonicalization/conformance suite.

### M-05. Performance snapshot не масштабируется

Полный workspace hashing перед/после stages и checks может повторно читать
тысячи ignored/dependency files. На больших monorepo это превращается в высокий
I/O cost и создаёт нестабильность из-за caches.

Candidate worktree, Git tree hashes, explicit dependency inputs и content cache
по inode/size/mtime с безопасной повторной валидацией снизят стоимость.

### M-06. Web projection неполна

Frontend встроен и polling показывает новые CLI runs, но UI ещё не раскрывает
всю структуру check/mutation/delivery/precondition evidence. WebSocket —
in-process transport; внешние CLI runs появляются через polling, не через общий
event bus. Absolute/live artifact links и invalidation semantics нужно
проверять на каждой странице.

### M-07. Logs могут содержать чувствительные данные

stdout/stderr агента и tool errors сохраняются в evidence logs/reports. Нет
redaction policy, classification, retention и безопасного export режима.

## 6. Контроль и verification: рекомендуемый целевой порядок

```text
task/config/runtime identity
  -> isolated candidate
  -> analyst/architect typed artifacts
  -> source change
  -> controller exact diff + static checks
  -> reviewer exact candidate decision
  -> test authoring
  -> controller typed test execution
  -> verifier receives AC trace + diff + actual check bundle
  -> exact per-subject approval
  -> immutable candidate attestation
  -> typed delivery plan
  -> effect execution
  -> post-effect verification
  -> terminal ledger root
  -> projections
```

Ключевой принцип: проверяющий агент может давать смысловой verdict, но факты
`какой diff`, `какие tests обнаружены`, `что исполнялось`, `какой exit code` и
`какие bytes доставлены` создаёт только controller.

## 7. Приоритизированный план улучшений

### P0 — блокеры строгого режима

1. Зафиксировать threat model и два профиля: `trusted-local` и `strict-sandbox`.
2. Реализовать `SandboxBackend`; strict profile fail closed без backend.
3. Перенести run в isolated candidate worktree и запретить runtime доступ к live
   target на запись.
4. Ввести candidate identity и binding всех checks/verdicts/delivery к нему.
5. Перестроить порядок tester/check/verifier и передавать controller evidence.
6. Добавить exact gate approvals и immutable `resume-run` lineage.
7. Сделать event ledger hash-chained и replayable; определить crash recovery.

Definition of Done P0:

- hostile fixture не может прочитать host secret, выйти в сеть или изменить live
  target;
- timeout/cancel не оставляет descendants;
- mutation candidate после approval инвалидирует downstream decisions;
- crash в каждой точке state/effect resume-ится без duplicate или false green;
- replay из ledger даёт тот же terminal state и выявляет tampering.

### P1 — полнота контроля

1. Versioned schemas для stage result, AC, design/tasks и trace links.
2. Typed test adapters для поддерживаемых stacks; zero-test всегда failure.
3. Runtime binary/model/provider pinning и per-attempt identity.
4. Delivery remote-base lease, plan-specific branch/state и producer lineage.
5. Secrets/env policy, log redaction и retention.
6. Per-package safety coverage + fault-injection matrix.

### P2 — универсализация и эксплуатация

1. Declarative workflow schema без зависимости от семи имён ролей.
2. Typed effect adapter SDK вместо GitHub-only финала.
3. Общий projector/event bus для web и CLI runs.
4. Calibrated eval corpus и versioned regression dashboard.
5. Archive/errata governance и автоматический conformance report OpenSpec.
6. Performance cache и официальная platform support matrix.

## 8. Release gates

Нельзя заявлять `strict`, `secure`, `hermetic`, `verified delivery` или
«подходит для недоверенного кода», пока не закрыты C-01—C-03 и H-04—H-06.

Для текущего `trusted-local` preview перед релизом обязательны:

- strict OpenSpec validation;
- gofmt, vet, module verification, vulnerability scan;
- unit + E2E + race;
- typed zero-test/large-output/timeout/cancel/mutation/delivery fault cases;
- frontend test/lint/build/audit;
- чистый requirement-to-test evidence report;
- отдельный список известных ограничений, совпадающий с README и CLI help.

## 9. Непредвзятый вывод

Рефакторинг уже устранил наиболее опасные false-green дефекты первоначального
каркаса: негативные verdict больше не рекомендации, тестовый label не заменяет
test evidence, delivery не доверяет `git add .`, а run history привязана к
идентичности и hashes. Это существенный прогресс.

Но следующий скачок качества достигается не ещё одним prompt или дополнительным
LLM reviewer. Он требует архитектурной границы: **isolated immutable candidate +
controller-owned evidence + exact approvals + replayable ledger**. Пока этой
границы нет, система должна честно оставаться trusted-local, а не выдавать
детектирование нарушений за предотвращение.
