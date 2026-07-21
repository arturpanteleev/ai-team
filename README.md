# ai-team

[![CI](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml/badge.svg)](https://github.com/arturpanteleev/ai-team/actions/workflows/ci.yaml)

Локальный control plane для решения IT-задач цепочкой AI-агентов. LLM создаёт
артефакты и предлагает вердикты; переходы, проверки, mutation scopes, evidence и
delivery исполняет детерминированный Go-контроллер.

## Установка

Требуются Go 1.26.5+ и [OpenCode](https://opencode.ai) в `PATH`.

```bash
go install github.com/arturpanteleev/ai-team/cmd/ai-team@latest
```

## Быстрый старт

```bash
cd /my-project
ai-team init

# В интерактивном терминале checkpoints запрашивают подтверждение.
ai-team run --feature add-jwt-auth --task "Реализовать JWT авторизацию"

# В non-interactive среде обычные checkpoints fail-closed.
ai-team run --feature add-jwt-auth --task "Реализовать JWT авторизацию" \
  --approve-gates

# Первый запуск публикует canonical delivery plan и завершается с кодом 3.
# После проверки плана разрешается только его точный SHA-256:
ai-team run --feature add-jwt-auth --retry-from deployer \
  --approve-gates --approve-plan <sha256-из-предыдущего-запуска>

ai-team list
ai-team web --port 8080
ai-team eval --agent analyst \
  --artifact .ai-team/artifacts/add-jwt-auth/proposal.md \
  --samples 3
```

Exit-коды `run`: `0` — completed/completed with warnings, `1` — ошибка или
негативный вердикт, `2` — BLOCKED, `3` — stopped на контрольной точке или перед
delivery.

## Конвейер и зоны ответственности

Порядок по умолчанию:

`analyst → architect → coder → reviewer → tester → verifier → deployer`

Reviewer, tester и verifier обязаны записать ровно один канонический verdict
marker. Отсутствующий, неизвестный или дублирующий marker — ошибка контроллера,
а не рекомендация LLM. BLOCKED передаётся отдельным status-файлом с обязательной
причиной.

Deployer — не исполнитель внешних команд от LLM. Контроллер строит строгий JSON
plan только из
workspace-relative файлов, изменение которых атрибутировано актуальным попыткам
текущего run. Затем он проверяет APPROVED/PASS/APPROVED, наличие успешного
required typed test check, approval точного SHA-256 плана, пустой staged index и
защиту базовой ветки. Только после этого выполняются exact-file commit, push и
создание PR. Перед commit повторно сверяются staged paths, blob SHA-256 и file
modes; после commit — parent, tree, blobs и modes. Состояние каждого шага
сохраняется. Если процесс оборвался сразу после `git commit`, controller
принимает существующий commit только после повторной сверки message, parent,
paths, modes и blob hashes; штатный retry не создаёт дублирующий commit или PR.

## Детерминированный контроль

- Required checks запускаются массивом argv без shell, с confined working dir,
  timeout, bounded stdout/stderr, tool path/version, exit code и timestamps.
- Падение required check переопределяет положительный LLM verdict. Optional
  отсутствующий tool виден как skipped/warning и не маскируется под pass.
- Mutation policy задаётся в definition агента. Baseline фиксируется перед
  попыткой. Новый Git-run требует чистого tracked/untracked workspace; ignored и
  существующие файлы считаются пользовательскими и не могут быть присвоены
  агенту. Вне Git используется полный hash snapshot, поэтому guard не
  пропускается.
- Read-only этап с любой source mutation падает. `require_diff` без фактической
  delta также падает. Delivery получает только нормализованные разрешённые пути.
- Checkpoints имеют явные политики `auto_continue`, `interactive` и
  `require_explicit`. В non-interactive режиме нет неявного согласия.
- Перед review controller публикует candidate workspace digest, changed paths,
  file fingerprints и tracked patch SHA-256. Tester только пишет tests; их
  фактический typed execution добавляется в финальный candidate evidence для
  verifier.
- Loopback создаёт новые attempt IDs и инвалидирует прежнюю downstream-ветку;
  invalidated/skipped/warning не отображаются как passed или failed.

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
SHA-256 hash chain и проверяются перед каждым append. Typed replay проверяет
порядок lifecycle transitions, terminal status, invalidations и exact digest
каждого опубликованного attempt manifest.
SQLite `.ai-team/web.db` — восстанавливаемая projection для web UI, а не источник
истины. Сервер по умолчанию слушает `127.0.0.1`, проверяет same-origin WebSocket,
периодически обновляет dashboard и отдаёт bounded файлы только из разрешённого
live или immutable run root без symlink traversal. Production frontend встроен
в бинарник; `--dist` позволяет явно подменить его локальной сборкой.

## Конфигурация

`ai-team init` создаёт строгий schema v3 config. Сейчас автоматически добавляется
только типизированный Go-профиль (`go test -json -count=1` и `go vet`). Rust,
Python, Node и неизвестные стеки намеренно не объявляются поддержанными, пока для
них нет parser adapter с доказательством обнаруженных и прошедших test cases;
delivery остаётся запрещённым до такой настройки.

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

Unknown/duplicate YAML fields, multiple documents, unsupported schema/CLI,
invalid transitions, paths, checks или loopback targets отклоняются до первого
LLM-вызова. Registry разрешает агентов слоями: project
`.ai-team/agents` → каталоги `AI_TEAM_AGENT_PATH` → user config → built-ins.
Невалидный override не скрывается fallback-слоем; `ai-team list` показывает
источник definition.

## Evals

`ai-team eval` запускает независимые LLM-оценки в изолированном временном
каталоге, требует ровно один score/comment и атомарно сохраняет samples, median,
mean и standard deviation в `.ai-team/evals/`. LLM quality eval намеренно
advisory: некалиброванный судья не является delivery gate. Нормативные гарантии
дают contract, behavioral, fault-injection и deterministic check suites.

## Граница безопасности

Система пока не является hermetic sandbox. OpenCode получает app-level deny
для shell/network/tasks, ограниченные edit/read rules и отдельный config home,
но сам процесс агента и команды проверок работают с правами текущего OS-user.
Проверки исполняют код проекта. Поэтому текущий профиль допустим только для
доверенного локального проекта и доверенных toolchains; секреты и недоверенный
код должны запускаться во внешнем container/VM sandbox. Полный список открытых
рисков и release gates находится в [AUDIT.md](AUDIT.md).

## Разработка и verification

Проект использует OpenSpec: принятые контракты лежат в `openspec/specs/`,
активные изменения — в `openspec/changes/`.

```bash
make build
make test
make test-e2e
make specs
make verify
```

`make verify` выполняет строгую OpenSpec-валидацию, module verification, vet,
govulncheck, race tests, frontend audit/lint/tests/build. CI дополнительно
проверяет gofmt и coverage gate 60%.
