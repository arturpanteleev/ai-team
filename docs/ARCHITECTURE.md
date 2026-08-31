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
test_modify_policy/inputs/outputs/ask_questions/checks/verdict/preconditions),
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
attestation v1, attempt manifests; без raw logs/stdout). index.json несёт sha256
каждого record без тайм-меток — identical evidence даёт байт-в-байт одинаковый
bundle (BundleDigest). Перед публикацией bundle обязан пройти полную verify:
records против своих sha256, run identity/schema, config/workflow snapshots
против run manifest, event chain + anchor (VerifyAnchor), attempt manifests
против manifest_sha256 в attempt_finished событиях (файлы↔events связка, которой
VerifyAnchor не даёт), attestation v1 против events/config/workflow/
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

### Отложенная (deferred) delivery и Git trailers — V0-9

Git-история меняется ТОЛЬКО после terminal finalize run'а (commit, push, PR),
когда attestation digest детерминирован.  Причины:

- attestation.json пишется в finalize ПОСЛЕ delivery-стадии; выполнить
  in-run delivery и при этом вшить в commit честный digest было бы циклично;
- попытка-манифесты immutable: `CommitSHA` заполнять внутри run нельзя, иначе
  погибает доказательство «что именно одобрено» при переживании потери bundle.

Внутри run delivery-стадия делает `delivery.Prepare` (state на диске), затем
`authorizeDelivery` (в интерактивном режиме run может встать в ожидание
approval `delivery_plan`, в resume выполняется `--approve-plan <sha>`), а не
`delivery.Execute`. Результат — deferred marker `{StatePath, PlanHash}` + event
`delivery_deferred`. После финального `finalize` при outcome `completed` и
наличии маркера post-terminal хук (`pkg/pipeline/delivery_deferred.go`):

1. перегружает canonical plan из prepared state и сверяет `plan.Hash()` с
   маркером стадии и с `approvedPlanHash`;
2. читает `attestation.json` → `attest.Parse` → `attest.Digest`;
3. вычисляет runtime identity = `sha256(run.json.Provenance)` (raw bytes);
4. собирает controller-derived trailers:
   - `ai-team-run: <run_id>`, `ai-team-runtime: <hex>`,
     `ai-team-attestation: <digest>`;
5. исполняет реальный `delivery.Execute(context.Background(), …)` рабочей
   копии (workspace lock всё ещё удержан — хук внутри `RunWithResult`);
6. записывает tamper-evident `{RunDir}/delivery.json` (self-integrity
   `record_sha256`, строгая валидация, однократная запись, idempotent retry).

Трейлеры не входят в canonical plan и subject approval (они controller-derived,
не из LLM). Commit message = plan.CommitMessage + trailers. Трейлеры персистятся
в delivery state ДО commit, поэтому crash-recovery (retry после commit) сверяет
message с `FullCommitMessage(plan.CommitMessage, savedTrailers)`. При расхождении
траилей восстановленный commit отклоняется.

`FindDelivered` (evidence/query) сначала читает `delivery.json` (канонический
источник), при его отсутствии падает на attempt-манифесты. Если post-terminal
хук упал (например, сетевой сбой push), доставка повторяема командой
`ai-team deliver --run <run_id> [--target <dir>] [--feature <name>]`: enforcement
детерминированный — run обязан быть терминальным `completed`, plan обязан
совпасть с `delivery_deferred` event, запись `delivery.json` однократная.

## Containment threat model — V0-P1-4

`pkg/containment` задаёт четырёхосевую модель изоляции run и детерминированный
record их фактического состояния (receipt). Оси:

- **fs** — filesystem: symlink rejection (safeio), worktree isolation, credential deny
- **net** — network: tool deny, env isolation
- **proc** — process: process-group kill, cleanup verification
- **env** — environment: env allow-list, config dir isolation, credential file deny

Уровни: `ENFORCED` (OS-level: bubblewrap/landlock/sandbox-exec), `PARTIAL`
(application-level mitigations из control-plane), `UNAVAILABLE` (mitigations
отсутствуют).

Профиль `trusted-local` подразумевает application-level controls — все оси
`PARTIAL`. Профиль `strict` требует OS-sandbox backend; без него (V1 backend не
включён) receipt = все оси `UNAVAILABLE` и run fail-closed отклоняется.

Receipt пишется отдельным evidence-файлом `{RunDir}/containment.json` на
terminal-завершении (`pkg/pipeline/finalize.go`), sibling `usage.json`: `run.json`
immutable с момента `Start`, потому receipt не поле manifest, а параллельный файл.
Legacy-раны без файла валидны — `verify`/gate трактуют оси как `UNAVAILABLE`
без fail. `ai-team usage` печатает per-axis статус.

Процессная ось верифицируется через `pkg/process.TrackAndCleanup`: SIGKILL
процессной группе + ожидание завершения ≤2s → `CleanupReceipt{Verified, Timeout}`.

Gate `--allow-untrusted` разрешён только при наличии receipt, у которого ни одна
ось ≠ `UNAVAILABLE` (fail-closed: без receipt или с `UNAVAILABLE` осью → BLOCKED).
Отсутствие receipt для legacy baseline остаётся backward-compat allow.

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
