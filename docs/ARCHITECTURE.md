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
- Loopback создаёт новые attempt IDs и инвалидирует прежнюю downstream-ветку;
  invalidated/skipped/warning не отображаются как passed или failed. Целевой
  этап loopback вычисляется из метаданных pipeline (ближайший предшествующий
  этап с `mutation: source`), а не жёстко закодированного имени — см.
  `pkg/pipeline/workflow.go:defaultLoopbackTarget`.

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

SQLite `.ai-team/web.db` — восстанавливаемая projection для web UI, а не
источник истины. Сервер по умолчанию слушает `127.0.0.1`, проверяет
same-origin для HTTP (`pkg/web/security.go`) и WebSocket, периодически
обновляет dashboard и отдаёт bounded файлы только из разрешённого live или
immutable run root без symlink traversal. Production frontend встроен в
бинарник (`go:embed`); `--dist` позволяет явно подменить его локальной
сборкой.

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
