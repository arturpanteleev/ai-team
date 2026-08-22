# План развития ai-team

> **⚠️ Исторический документ.** Большая часть этого плана реализована в
> change «Implement human-controlled cloud workflow» (commit 91e4fda):
> approvals/RBAC, RunEngine, web write API, WebSocket bridge, scheduler,
> candidate worktree. Статус конкретных пунктов не отслеживается здесь —
> актуальное состояние см. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) и
> `TECHNICAL_AUDIT_2026-08-05.html`.

## 1. Целевая концепция

`ai-team` — это управляемый человеком control plane для последовательной
работы AI-агентов. AI выполняет содержательную работу внутри этапа, но переход
между значимыми этапами разрешает человек с соответствующей ролью.

Целевой процесс:

```text
AI Analyst
  → Product Owner approves exact product plan
AI Architect
  → Human Architect approves exact technical design
AI Coder
  → Human Developer approves candidate
AI Reviewer / AI Tester
  → Human Reviewer / QA approves review and test evidence
Delivery
  → Release Manager approves exact delivery plan
PR
```

Человеческие approvals — не временная особенность CLI, а центральная часть
продукта. Они должны одинаково работать через терминал, web-интерфейс и
будущий облачный control plane.

## 2. Главный архитектурный принцип

Сейчас бизнес-решение человека связано со способом его получения:
`Prompter.Interactive()` одновременно определяет наличие TTY и возможность
продолжить workflow. Эти понятия необходимо разделить.

Ядро pipeline должно:

1. Выполнить AI-этап.
2. Зафиксировать точный предмет approval.
3. Создать persisted-запрос на человеческое решение.
4. Перевести run в состояние `waiting_for_approval`.
5. Продолжить тот же run после получения допустимого решения.

CLI, web и будущие интеграции должны быть только способами доставки решения в
одно и то же ядро.

Пример доменной модели:

```go
type PendingApproval struct {
    ID            string
    RunID         string
    FromStage     string
    ToStage       string
    Trigger       string
    SubjectHash   string
    RequiredRoles []string
    Quorum        string // all | any
    Actions       []string
}

type ApprovalDecision struct {
    ApprovalID string
    ActorID    string
    ActorRole  string
    Action     string
    Comment    string
    DecidedAt  time.Time
}
```

Каждое решение должно фиксировать:

- кто принял решение;
- в какой роли он действовал;
- что именно было показано человеку;
- точный SHA-256 предмета approval;
- выбранное действие;
- комментарий;
- timestamp;
- предыдущую и следующую стадии;
- идентичность candidate, если решение относится к коду.

Approval относится не к стадии вообще, а к конкретному предлагаемому
переходу между стадиями. Например, человек разрешает переход
`architect → coder` по точному техническому плану либо переход
`reviewer → coder` после `CHANGES_REQUESTED`.

Один переход может требовать решения нескольких ролей. Политика `all`
означает, что нужны все перечисленные approvals; `any` — что достаточно одного
уполномоченного решения. Это позволяет, например, потребовать Developer и QA
на одном candidate, не привязывая количество человеческих approval-точек к
количеству AI-агентов.

## 3. Семантика review → revise

Человека нельзя исключать из петли исправлений.

Если AI reviewer возвращает `CHANGES_REQUESTED`, pipeline должен перейти в
`waiting_for_approval` и предложить уполномоченному человеку:

- `return_to_coder` — вернуть работу на доработку;
- `reject` — завершить run как отклонённый;
- `request_information` — запросить уточнение;
- `override_approve` — разрешить переход вопреки AI-verdict, если роль имеет
  такое полномочие.

После решения `return_to_coder` технический loopback выполняется
автоматически: controller инвалидирует downstream attempts, передаёт review
coder-у и запускает новую попытку. Однако сам переход всегда санкционируется
человеком.

`max_retries` ограничивает число таких циклов, но не заменяет approval.
Исчерпание retries создаёт отдельное решение или переводит run в `blocked` в
зависимости от политики.

## 4. Подтверждённые проблемы текущей реализации

### 4.1. Сценарий `init → run` не работает

`ai-team init` изменяет `.gitignore`, после чего baseline guard отклоняет новый
run из-за грязного workspace. E2E-тесты скрывают проблему, вручную коммитя
`.gitignore` перед запуском pipeline.

Предлагаемое решение:

- по умолчанию добавлять `.ai-team/` в `.git/info/exclude`;
- не изменять tracked workspace во время обычного `init`;
- добавить явный `--write-gitignore` для пользователя, который хочет
  закоммитить общее правило;
- добавить E2E `init → run` без промежуточного Git commit.

### 4.2. Loopback зависит от TTY

Возврат к coder находится внутри `Prompter.Interactive()`. В non-interactive
режиме, включая будущий web/cloud worker, `CHANGES_REQUESTED` останавливает
pipeline вместо создания человеческого решения.

Нужно вынести approval из TTY и реализовать persisted pause/resume.

### 4.3. Web пока является read-only dashboard

Текущий HTTP API позволяет читать runs, stages и artifacts, но не позволяет:

- создать run;
- подтвердить stage;
- вернуть работу на доработку;
- отклонить или отменить run;
- продолжить blocked/waiting run;
- подтвердить exact delivery plan.

### 4.4. WebSocket не подключён к production events

WebSocket необходимо сохранить как целевой transport живых событий, но
текущая реализация не получает события от CLI/pipeline:

- `BroadcastEvent` вызывается только тестами;
- CLI и web работают в разных процессах;
- in-memory hub web-процесса не видит записи CLI в SQLite;
- frontend и recorder используют разные названия событий;
- часть событий не содержит идентификаторов, необходимых frontend.

Polling остаётся временным fallback, но не целевой архитектурой.

### 4.5. Полная verification-цепочка сейчас красная

Go tests, E2E, race, coverage, OpenSpec, vet и govulncheck проходят. Полный
`make verify` падает на `npm audit --audit-level=high` из-за high severity
advisories в текущей версии `react-router`.

Это необходимо исправить до следующего release.

## 5. Целевая модель состояний

Run должен быть долговечной persisted-сущностью, а не одним непрерывным
вызовом CLI:

```text
queued
  → running
  → waiting_for_approval
  → revising
  → running
  → blocked
  → completed

terminal alternatives:
  failed
  rejected
  canceled
  interrupted
```

Рестарт процесса не должен требовать создания логически нового run через
`--retry-from`. Controller должен восстановить тот же run, его lineage,
актуальные attempts, pending approvals и candidate identity.

## 6. Целевая web-архитектура

### 6.1. Run engine

Необходимо выделить `RunEngine` из CLI-команды. Он должен владеть:

- запуском и восстановлением runs;
- workspace locking;
- переходами state machine;
- созданием pending approvals;
- применением решений;
- cancel/resume;
- event publication.

CLI и HTTP API используют один и тот же `RunEngine`.

### 6.2. HTTP command API

Минимальный набор write endpoints:

```text
POST /api/runs
POST /api/runs/{id}/approvals/{approval_id}/decisions
POST /api/runs/{id}/cancel
POST /api/runs/{id}/resume
```

Read API должен дополнительно возвращать:

- pending approvals;
- allowed actions;
- required role;
- exact subject hash;
- candidate/diff, который человек подтверждает;
- историю решений и actor identity.

### 6.3. Главная web-поверхность

Кроме управления run и approvals, web должен показывать уже собираемые
controller-ом данные:

- live-лог текущего агента;
- deterministic checks с command, status, duration и evidence;
- mutations и exact candidate diff;
- delivery plan и результаты отдельных git-шагов;
- историю переходов, invalidated attempts и человеческих решений;
- текущий workflow graph.

Просмотр workflow config можно добавить вместе с graph schema. Редактирование
конфига через web следует делать только после стабилизации schema v4, чтобы не
строить UI вокруг временной линейной модели. Любое изменение должно проходить
ту же строгую валидацию, что и CLI, показывать diff и требовать отдельного
approval при изменении активного workflow.

### 6.4. WebSocket event stream

WebSocket остаётся основным каналом live-обновлений.

Нужен единый versioned event contract, используемый evidence ledger,
projection, WebSocket и frontend:

```json
{
  "schema_version": 1,
  "sequence": 42,
  "type": "approval_requested",
  "run_id": "run-...",
  "attempt_id": "attempt-...",
  "stage": "architect",
  "timestamp": "...",
  "data": {}
}
```

Минимальные типы событий:

```text
run_queued
run_started
stage_started
stage_finished
approval_requested
approval_decided
attempts_invalidated
run_blocked
run_finished
run_canceled
```

Для связи pipeline и web возможны два последовательных варианта.

**Ближайший вариант:** web-процесс наблюдает за persisted event table по
`sequence`, публикует новые события в WebSocket hub, а polling остаётся
страховочным механизмом восстановления.

**Целевой вариант:** долгоживущий web/control-plane процесс сам владеет
`RunEngine`, получает события напрямую через общий event bus, атомарно пишет
projection и сразу публикует WebSocket event. CLI может работать как клиент
этого control plane либо запускать тот же engine локально.

WebSocket-клиент должен поддерживать:

- reconnect с exponential backoff;
- `last_sequence`/cursor;
- replay пропущенных событий после reconnect;
- дедупликацию по `(run_id, sequence)`;
- fallback fetch состояния через HTTP.

Live-логи также передаются через WebSocket как bounded chunk-события с
sequence/cursor. Полный лог остаётся доступен отдельным HTTP endpoint, чтобы
reconnect не требовал воспроизведения неограниченного потока с начала.

### 6.5. Runtime preflight

До старта run web и CLI должны уметь проверить готовность окружения:

- `opencode` найден и его версия поддерживается;
- настроен доступный provider/model;
- требуемые credential-переменные явно разрешены через environment allow-list;
- `gh` доступен и авторизован, если workflow содержит delivery;
- repository remote и base branch пригодны для выбранного delivery adapter.

Preflight не заменяет fail-closed проверки внутри run, но позволяет показать
понятную ошибку до первого человеческого approval и дорогостоящего AI-вызова.

### 6.6. Защита write API

Появление управляющих endpoints меняет threat model. Одного loopback bind и
Origin/Host checks недостаточно.

Для локальной версии необходимы:

- случайный session token;
- проверка Origin/Host;
- CSRF-защита write endpoints;
- audit actor для каждого решения.

Для облачной версии понадобятся полноценные authentication и RBAC.

## 7. Конфигурируемый граф workflow

Текущая конфигурация задаёт упорядоченный список стадий. Движение вперёд
зашито как переход к следующему элементу, а возврат реализован отдельным
loopback-механизмом. Целевая модель должна представлять workflow как граф:

- вершины — AI/controller stages;
- результат стадии выбирает предлагаемое ребро;
- ребро определяет следующую вершину;
- approval policy на ребре определяет, кто должен разрешить переход;
- terminal-вершины явно завершают workflow.

Пример целевой schema v4:

```yaml
schema_version: 4

stages:
  - name: analyst
    on:
      passed:
        to: architect
        approval:
          roles: [product_owner]

  - name: architect
    on:
      passed:
        to: coder
        approval:
          roles: [architect]

  - name: coder
    on:
      passed:
        to: reviewer
        approval:
          roles: [developer]

  - name: reviewer
    max_visits: 3
    on:
      approved:
        to: tester
        approval:
          roles: [reviewer]
      changes_requested:
        to: coder
        approval:
          roles: [reviewer]
      rejected:
        to: rejected
        approval:
          roles: [reviewer]
      blocked:
        to: blocked

  - name: tester
    on:
      passed:
        to: verifier
        approval:
          roles: [qa]

  - name: verifier
    on:
      approved:
        to: deployer
        approval:
          roles: [reviewer, qa]
          quorum: all

  - name: deployer
    on:
      passed:
        to: completed
        approval:
          roles: [release_manager]
```

Graph validation должна проверять:

- существование всех target-вершин;
- наличие стартовой и terminal-вершин;
- достижимость terminal-исхода;
- соответствие trigger допустимым outcomes стадии;
- отсутствие неограниченного цикла без human approval;
- `max_visits` для циклических маршрутов;
- корректность required roles и quorum;
- детерминированность выбора ребра;
- правила инвалидации downstream attempts после обратного перехода.

При переходе на schema v4 существующие `transition`, `gate_before`,
`gate_after`, `checkpoint_before`, `checkpoint_after`, `loopback_to` и
`max_retries` не следует переносить как независимые механизмы. Они должны быть
нормализованы в graph edges, approval policies и `max_visits`.

Legacy schema можно принимать один переходный release с предупреждением и
детерминированной миграцией. Новые возможности реализуются только в graph
schema, чтобы новая approval-модель не наследовала несколько способов
выразить одно поведение.

## 8. Candidate isolation без преждевременной sandbox-платформы

Git candidate worktree полезен уже на базовом этапе:

- агент не изменяет live workspace пользователя;
- review, tests, approval и delivery относятся к одному candidate;
- retry и crash recovery становятся проще;
- candidate естественно переносится в cloud worker;
- уменьшается число полных filesystem snapshots.

Поэтому candidate worktree следует реализовать до полного OS sandbox.

Каждое изменение candidate должно менять его identity и инвалидировать все
downstream approvals, относящиеся к предыдущему hash.

## 9. Что пока не следует реализовывать

До завершения базового workflow не следует инвестировать в:

- собственный Linux bubblewrap/container backend;
- отдельную macOS sandbox-реализацию;
- Windows container/sandbox backend;
- универсальную capability negotiation между OS backends;
- встроенные CPU/RAM/PID/disk quotas;
- сложную network allow-list внутри `ai-team`;
- дополнительные языковые test adapters;
- новые delivery providers.

Также не следует сейчас сокращать AI-роли с семи до четырёх или пяти.
Независимый tester имеет продуктовую ценность: автор кода не должен быть
единственным автором тестов. Объединение reviewer/tester/verifier допустимо
рассматривать только после того, как approvals отвязаны от стадий и накоплены
метрики качества, стоимости и дублирования выводов.

В облаке process/filesystem/network isolation вероятнее всего должна
обеспечиваться инфраструктурой disposable workers:

```text
Cloud control plane
  → creates isolated worker job/container
  → mounts repository candidate
  → runs ai-team worker
  → receives events and pending approvals
```

Собственный `SandboxBackend` стоит возвращать в roadmap, если появится
подтверждённое требование strict standalone/on-prem исполнения без внешней
container platform.

## 10. Приоритизированный roadmap

### P0. Работающий базовый сценарий

1. Исправить frontend dependency audit.
2. Исправить `init → run` и добавить соответствующий E2E.
3. Ввести typed `PendingApproval` и `ApprovalDecision`.
4. Отвязать approvals и loopback от `Prompter.Interactive()`.
5. Реализовать человеческое решение для `CHANGES_REQUESTED`.
6. Сохранить текущий WebSocket, но унифицировать event schema.
7. Добавить production event bridge между recorder/event store и hub.
8. Оставить polling как fallback до появления replay/cursor.
9. Обновить README так, чтобы quick start выполнялся буквально.

Definition of Done:

- новый пользователь выполняет `init → run` без скрытого Git-шага;
- каждый смысловой переход требует человеческого решения;
- non-interactive worker может приостановиться и ждать decision;
- `CHANGES_REQUESTED` после человеческого решения возвращает работу coder-у;
- web показывает live-события через WebSocket;
- reconnect не теряет итоговое состояние.

### P1. Persisted workflow и web control plane

1. Выделить `RunEngine`.
2. Реализовать persisted state machine и resume того же run.
3. Добавить write API для run, decisions, cancel и resume.
4. Добавить страницу pending approvals.
5. Показывать exact artifact/diff/subject hash перед решением.
6. Добавить actor identity и минимальную role validation.
7. Реализовать WebSocket replay по sequence/cursor.
8. Добавить live-логи через WebSocket и HTTP fallback.
9. Отрисовать checks, mutations, delivery и историю решений.
10. Добавить runtime preflight.
11. Добавить crash/restart и browser-level E2E.

Definition of Done:

- весь путь от создания задачи до подтверждённого draft PR доступен в web;
- каждый approval относится к точному содержимому и actor role;
- рестарт control plane не теряет run или pending approval.

### P2. Workflow graph и schema v4

1. Ввести явные graph edges по stage outcomes.
2. Привязать approval policy и required roles к переходам.
3. Добавить `max_visits` и валидацию циклов.
4. Различать `CHANGES_REQUESTED`, `REJECTED`, `BLOCKED` и другие outcomes при
   выборе маршрута.
5. Нормализовать legacy checkpoint/loopback-поля в graph model.
6. Добавить визуализацию графа в web.
7. После стабилизации schema добавить просмотр и редактирование config из web.

Definition of Done:

- маршрутизация определяется конфигом, а не позицией стадии в slice;
- каждый переход имеет однозначный outcome, target и approval policy;
- циклические маршруты ограничены и аудируемы;
- web показывает фактически исполняемый граф;
- legacy-конфиг мигрируется детерминированно.

### P3. Candidate worktree

1. Создавать отдельный worktree на run.
2. Ввести immutable candidate identity.
3. Привязать review, checks, approvals и delivery к candidate hash.
4. Инвалидировать устаревшие approvals после mutation.
5. Не изменять live workspace до отдельного promotion/delivery шага.
6. Упростить существующий snapshot hashing после перехода на candidate.

### P4. Облачная эксплуатация

1. Разделить cloud control plane и disposable workers.
2. Добавить authentication и RBAC:
   Product Owner, Architect, Developer, Reviewer, QA, Release Manager.
3. Реализовать queue, concurrency и distributed workspace locks.
4. Вынести artifacts/evidence в подходящее persistent storage.
5. Передавать repository/provider credentials worker-у с минимальным scope.
6. Изолировать worker средствами cloud/container platform.

### P5. Контролируемое упрощение

После появления рабочего вертикального сценария провести отдельный
scope-review production-кода и мета-процесса.

Кандидаты:

- неиспользуемые constructors и helpers;
- Vite template assets;
- legacy config paths после завершения migration window;
- повторные filesystem digest passes, ставшие ненужными после candidate;
- UI/report-представления, которые полностью заменены web;
- подсистемы без production-потребителя и без места в cloud architecture;
- устаревшие или дублирующие спецификации.

Удаление разрешается только если:

1. Нет production-вызовов или внешнего контракта.
2. Возможность не нужна approval, recovery, audit или cloud пути.
3. Публичная документация её не обещает либо содержит migration notice.
4. После удаления есть тест, подтверждающий отсутствие регрессии.

`report`, `notifier`, `eval`, evidence replay и отдельные AI-роли нельзя
удалять пакетно только по числу строк или отсутствию текущего frontend-
потребителя. Для каждого требуется отдельное продуктовое решение и данные об
использовании.

### P6. Расширение платформы

После стабилизации основного сценария:

- typed adapters для Python, Node и Rust;
- acceptance-criteria traceability;
- GitLab и другие delivery adapters;
- runtime/model/provider pinning;
- nightly/manual smoke-run с настоящим OpenCode;
- strict standalone sandbox, если он действительно нужен рынку.

## 11. Карта OpenSpec changes

Roadmap реализуется не одним большим change, а последовательностью небольших
OpenSpec changes. Каждый change должен давать один проверяемый пользовательский
или архитектурный результат и не смешивать независимые рефакторинги.

### Волна 0. Разблокировать разработку и первый запуск

| Change | Capabilities | Результат | Зависимости |
|---|---|---|---|
| `restore-green-frontend-verification` | modified: `ci-pipeline` | `make verify` и frontend CI снова проходят high-severity audit без отключения gate | нет |
| `clean-project-initialization` | modified: `project-init`, `e2e-testing` | `ai-team init → ai-team run` работает без ручного commit `.gitignore` | нет |

Эти changes независимы и могут разрабатываться отдельно. Они должны быть
завершены до крупных изменений workflow, чтобы последующая работа проходила
через зелёную verification-цепочку и воспроизводимый quick start.

### Волна 1. Durable run, approvals и рабочий web

| Change | Capabilities | Результат | Зависимости |
|---|---|---|---|
| `unified-live-event-stream` | new: `run-event-stream`; modified: `web-websocket`, `web-websocket-client`, `web-sqlite-store`, `run-evidence` | единый versioned event contract, production bridge в WebSocket, cursor/replay и polling fallback | волна 0 |
| `durable-run-lifecycle` | new: `run-lifecycle`; modified: `workflow-engine`, `workflow-rollback`, `cli-interface`, `run-evidence` | `RunEngine`, persisted state machine и resume того же run после restart | `unified-live-event-stream` |
| `human-transition-approvals` | new: `human-approvals`; modified: `pipeline-gates`, `workflow-loopback`, `run-evidence`, `cli-interface` | exact approval конкретного перехода с actor, role, subject hash, actions и quorum; `CHANGES_REQUESTED` ждёт человека без TTY | `durable-run-lifecycle` |
| `web-run-control-plane` | new: `web-run-control`; modified: `web-api`, `web-http-server`, `web-dashboard`, `web-pipeline-detail` | запуск, approve/reject/return-to-coder, cancel и resume из web с session token/CSRF/audit actor | `human-transition-approvals` |
| `web-observability-and-preflight` | new: `runtime-preflight`; modified: `web-pipeline-detail`, `web-artifact-viewer`, `opencode-integration` | live-логи, checks/mutations/delivery UI и проверка готовности OpenCode/gh/repository до run | `web-run-control-plane` |

`unified-live-event-stream` вводит транспорт и контракт событий, но не
бизнес-решения. `durable-run-lifecycle` вводит долговечный run, но не роли.
`human-transition-approvals` вводит доменную модель решения, а
`web-run-control-plane` только предоставляет web-транспорт для уже готовой
модели. Такое разделение не позволяет HTTP/TTY-деталям снова попасть в ядро.

### Волна 2. Граф и единый candidate

| Change | Capabilities | Результат | Зависимости |
|---|---|---|---|
| `workflow-graph-v4` | new: `workflow-graph`; modified: `role-config`, `workflow-transitions`, `agent-orchestration`, `pipeline-gates` | graph schema v4, outcome edges, approval policies на рёбрах, `max_visits`, migration legacy config и web-визуализация | `human-transition-approvals` |
| `candidate-worktree-isolation` | new: `candidate-isolation`; modified: `control-plane-safety`, `deterministic-checks`, `delivery-executor`, `run-evidence` | отдельный worktree на run и одна candidate identity для code, review, checks, approvals и delivery | `workflow-graph-v4` |

Активный change `strict-sandbox-and-candidate-isolation` не следует применять
в текущем виде: он смешивает нужный candidate worktree с преждевременными
OS-specific sandbox backends. Перед началом волны 2 его нужно явно пометить
superseded и заменить change `candidate-worktree-isolation`. Strict standalone
sandbox при необходимости оформляется отдельным будущим change.

### Волна 3. Облачное исполнение

| Change | Capabilities | Результат | Зависимости |
|---|---|---|---|
| `cloud-auth-and-rbac` | new: `cloud-identity`, `approval-rbac` | authentication, роли Product Owner/Architect/Developer/Reviewer/QA/Release Manager и проверка полномочий решений | волна 2 |
| `cloud-worker-execution` | new: `worker-execution`; modified: `opencode-integration`, `control-plane-safety` | disposable worker выполняет run/candidate в инфраструктурно изолированном job/container | `cloud-auth-and-rbac` |
| `distributed-run-scheduling` | new: `run-scheduling`, `artifact-storage` | queue, concurrency, distributed locks и persistent artifacts/evidence | `cloud-worker-execution` |

### Волна 4. Упрощение и расширение

`controlled-scope-reduction` не создаётся заранее как один массовый change.
После появления telemetry каждый подтверждённый cleanup оформляется отдельным
change с точным удаляемым контрактом. Аналогично Python/Node/Rust adapters,
GitLab delivery, strict standalone sandbox и другие расширения получают
самостоятельные proposals только после базового end-to-end workflow.

### Порядок OpenSpec-gates для каждого change

Для каждого change действует один и тот же обязательный цикл:

1. Explore и подтверждение границ.
2. `proposal.md` — approval пользователя.
3. `design.md` — отдельный approval.
4. Delta specs — отдельный approval.
5. `tasks.md` — отдельный approval.
6. Apply, verification и ручная проверка Definition of Done.
7. Archive после принятия результата.

Одновременно реализуется только change, чьи planning artifacts утверждены.
Следующий change можно исследовать параллельно, но его код не смешивается с
текущим implementation scope.

## 12. Метрики готовности

Работу следует оценивать не числом добавленных абстракций, а вертикальными
пользовательскими результатами:

- `init → first pending approval` без ручного исправления workspace;
- 100% meaningful stage transitions имеют exact human approval;
- live-событие появляется в web не позднее чем через 1–2 секунды;
- WebSocket reconnect восстанавливает все события без дубликатов;
- restart продолжает тот же run;
- stale subject/candidate approval всегда отклоняется;
- маршрут каждого перехода объясняется graph config и recorded outcome;
- циклический маршрут не может превысить `max_visits`;
- live-лог текущего агента доступен в web;
- checks, mutations и delivery evidence видны без HTML-отчёта;
- задача проходит путь до draft PR без обязательного доступа к TTY;
- deterministic checks и delivery guarantees сохраняются.

## 13. Итоговый фокус

Сильная сторона проекта — реальные deterministic checks, evidence и строгая
delivery. Их не нужно упрощать.

Основная работа ближайшего периода должна быть сосредоточена вокруг трёх
сущностей:

```text
Run → Candidate/Artifact → Human Approval
```

WebSocket, CLI, web UI, Slack и будущий cloud должны быть транспортами и
интерфейсами вокруг этой модели. Workflow graph определяет допустимые
переходы, а human approval разрешает конкретное ребро по точному subject hash.
OS-specific sandbox backends и расширение числа платформ следует отложить до
момента, когда базовый human-controlled workflow полностью работает
end-to-end.
