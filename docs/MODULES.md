# Карта модулей

Это индекс к коду: найдите ответственность, затем откройте пакет.
Все 58 верхнеуровневых пакетов `pkg/` перечислены ниже; внутренние типы и
полный поток описаны в [архитектуре](ARCHITECTURE.md).

## Поток одной задачи

```text
CLI / HTTP → config + preflight → control / RunEngine
                                      ↓
                         pipeline ↔ workflow + lifecycle
                            ↓                ↓
                  runtime / candidate    approval
                            ↓                ↓
                   scope + checks + verdict
                                      ↓
                          canonical plan → delivery
                                      ↓
                          evidence → export / report
```

Runtime создаёт смысловые артефакты. Контроллер решает, допустим ли следующий
переход. Checks измеряют результат исполнения. Approval разрешает точный
subject. Delivery исполняет уже проверенный план. Нельзя заменять один из
этих уровней положительным ответом модели.

## Точки входа и исходники

| Каталог | Ответственность | Когда открывать |
|---|---|---|
| [cmd/ai-team](../cmd/ai-team/) | CLI parsing, composition, exit codes | Меняется команда или её вывод |
| [agents](../agents/) | Встроенные def.yaml и prompt.md | Меняется контракт или инструкция роли |
| [web](../web/) | React dashboard, встроенный dist | Меняется операторский UI |
| [docsgen](../docsgen/) | Markdown → статический сайт, навигация и поиск | Меняется GitHub Pages документация |
| [e2etest](../e2etest/) | CLI subprocess + mock runtime | Нужна проверка полного пути без API-ключа |
| [openspec](../openspec/) | Принятые контракты и история изменений | Меняется нормативное поведение |
| [.opencode/skills](../.opencode/skills/) | Инструкции разработки для OpenCode | Нужна автоматизация разработки самого проекта |

## Пакеты Go: контракт и граница

### agent — Definition и Registry

Загружает определения ролей и разрешает слои registry. Передаёт проверенный контракт агента runtime/pipeline; не выполняет доставку.

Исходники: [pkg/agent](../pkg/agent/).

### approval — Решения человека

Хранит pending/resolved approvals, subjects, роли и решения. Pipeline связывает решение с конкретным переходом или планом.

Исходники: [pkg/approval](../pkg/approval/).

### artifactstore — Content-addressed storage

Хранит immutable blobs по digest и manifest архива run. Это хранилище, не решение об успешности run.

Исходники: [pkg/artifactstore](../pkg/artifactstore/).

### attest — Attestation statement

Описывает subjects, digests и сведения о run в in-toto-compatible statement. Не заменяет проверку или подпись.

Исходники: [pkg/attest](../pkg/attest/).

### autopickup — Политика автоподбора задач

Статусы элемента бэклога, переходы, разрешённые человеку, и чистое решение «брать ли следующую задачу» по лимиту параллельных задач и дневному бюджету токенов. Хранение очереди и запуск остаются за web store и контроллером.

Исходники: [pkg/autopickup](../pkg/autopickup/).

### candidate — Candidate worktree

Создаёт и проверяет рабочую копию Git-кандидата от baseline, хранит metadata и identity. Изоляция worktree не является OS sandbox.

Исходники: [pkg/candidate](../pkg/candidate/).

### checks — Исполнение проверок

Принимает typed definitions, запускает argv с timeout и bounds, фиксирует outcome и workspace digest. Required failure сильнее verdict LLM.

Исходники: [pkg/checks](../pkg/checks/).

### ciimport — Импорт checks из CI

Распознаёт ограниченный набор GitHub Actions steps и превращает в definitions. Неподдерживаемые шаги объясняет; не исполняет произвольный workflow YAML.

Исходники: [pkg/ciimport](../pkg/ciimport/).

### cloudidentity — Identity и RBAC

Определяет actor, роли и проверяемые cloud tokens. Identity участвует в авторизации, но не заменяет exact subject approval.

Исходники: [pkg/cloudidentity](../pkg/cloudidentity/).

### config — Конфигурация и профили

Читает и валидирует config, defaults, fast/standard/regulated и resolved настройки. Некорректный контракт должен быть отклонён до исполнения.

Исходники: [pkg/config](../pkg/config/).

### containment — Статус ограничений

Валидирует receipt для fs/net/proc/env. PARTIAL/UNAVAILABLE явно отличаются от ENFORCED; пакет не реализует OS backend.

Исходники: [pkg/containment](../pkg/containment/).

### control — Управление RunEngine

Связывает долговечный engine и process-local workers, preflight, start/resume/stop. Не вычисляет доменные verdict вместо pipeline.

Исходники: [pkg/control](../pkg/control/).

### delivery — Canonical plan и Git-эффекты

Нормализует план, проверяет prerequisites и исполняет разрешённые commit/push/PR. Pipeline передаёт авторизованный план; LLM не определяет произвольную команду executor.

Исходники: [pkg/delivery](../pkg/delivery/).

### dsse — Подпись envelope

Кодирует и проверяет DSSE envelope и Ed25519-подпись. Подлинность зависит от доверенного verify key, а не только от наличия envelope.

Исходники: [pkg/dsse](../pkg/dsse/).

### eval — Независимая оценка качества

Запускает judge для артефакта, собирает результат оценки. Оценка LLM не заменяет deterministic checks или delivery approval.

Исходники: [pkg/eval](../pkg/eval/).

### evidence — История и replay

Публикует run/attempt manifests, append-only hash-chained events, проверяет целостность и последовательность. Источник исторических фактов, не mutable checkpoint.

Исходники: [pkg/evidence](../pkg/evidence/).

### export — Portable bundle

Собирает whitelist records терминального run, проверяет bundle и публикует verified export record. CLI применяет privacy policy перед публикацией.

Исходники: [pkg/export](../pkg/export/).

### gate — Самостоятельный diff gate

Из base/candidate и checks строит verdict и attestation bundle. Commit candidate требует совпадающего checkout; gate не генерирует код и не доставляет его.

Исходники: [pkg/gate](../pkg/gate/).

### junit — Разбор JUnit XML

Парсит test suites и failures для typed adapter. Ненулевые failures не становятся pass из-за exit0 команды, подготовившей XML.

Исходники: [pkg/junit](../pkg/junit/).

### lifecycle — Execution checkpoint

Хранит изменяемое состояние run и следующий шаг для восстановления. Отделён от immutable evidence; resume проверяет оба уровня.

Исходники: [pkg/lifecycle](../pkg/lifecycle/).

### logging — Машинный и человеческий вывод

Управляет normal/quiet/JSON records и выводом CLI. Machine stdout должен оставаться парсируемым, без примеси human summary.

Исходники: [pkg/logging](../pkg/logging/).

### metrics — Usage envelope

Агрегирует попытки, стадии, длительности и известный usage. Неизвестные provider значения не должны выдаваться за измеренные.

Исходники: [pkg/metrics](../pkg/metrics/).

### notifier — События для наблюдателей

Передаёт сообщения pipeline в console и другие sinks. Представление события не даёт права менять исход workflow.

Исходники: [pkg/notifier](../pkg/notifier/).

### pipeline — Оркестрация исполнения

Соединяет graph, runtime, scope, checks, verdict, approvals и delivery. Владеет attempts, loopback, candidate evidence, enforcement и post-terminal hook.

Исходники: [pkg/pipeline](../pkg/pipeline/).

### preflight — Предварительная диагностика

Проверяет runtime и prerequisites до дорогого исполнения. Разделяет обязательные runtime и контекстные delivery условия; не заменяет финальную авторизацию.

Исходники: [pkg/preflight](../pkg/preflight/).

### process — Жизненный цикл subprocess

Контролирует process group, отмену и завершение дочерних процессов. Сам по себе не ограничивает доступ процесса к сети и файловой системе.

Исходники: [pkg/process](../pkg/process/).

### provenance — Identity источников

Фиксирует controller/config/definition/prompt/check suite/provider identity и обнаруживает drift при resume. Unknown остаётся отдельным значением.

Исходники: [pkg/provenance](../pkg/provenance/).

### redact — Privacy scan

Ищет секреты, применяет policy и строит detached-копию с заменами. Heuristic scanner не доказывает отсутствие всех возможных секретов.

Исходники: [pkg/redact](../pkg/redact/).

### report — HTML run report

Формирует отчёт по результатам run и evidence. Это представление результата, не публикация GitHub Pages документации.

Исходники: [pkg/report](../pkg/report/).

### retention — План уборки

Строит и применяет gc plan; защищает evidence без verified export record. Dry-run позволяет оценить удаление заранее.

Исходники: [pkg/retention](../pkg/retention/).

### risk — Измеримые risk signals

Классифицирует чувствительные пути и признаки diff. Сигнал помогает review, но не является LLM verdict или универсальным security score.

Исходники: [pkg/risk](../pkg/risk/).

### runtime — Адаптер LLM CLI

Готовит prompt, argv, изолированное окружение и артефакты OpenCode/Codex/Claude. Агентские инструменты могут менять candidate в рамках контракта стадии.

Исходники: [pkg/runtime](../pkg/runtime/).

### stageexec — Исполнитель этапа

Интерфейс `StageExecutor` создаёт результат этапа: `InternalAgent` оборачивает `runtime.Runtime`, `External` запускает доверенный argv из `executor_adapters` по JSON-контракту (лимиты времени и вывода, allowlist окружения, redaction лога); при `docker:` в адаптере тот же контракт исполняет `DockerLauncher` (pkg/dockerrun: входы только на чтение, приватный каталог результата, сеть none, образ по digest). Отделён от гейтов: исполнитель только создаёт выходы, а тип результата, mutation scope и verdict проверяет контроллер.

Исходники: [pkg/stageexec](../pkg/stageexec/).

### llmgate — Контракт LLM-гейта

Вызов агента-проверяющего с фиксированным входом и объявленной схемой вердикта (`verdict_marker` или `json`). Ответ вне схемы и отказ рантайма — техническая ошибка, а не `pass`/`fail`; `max_attempts` повторяют только такие сбои, каждая попытка остаётся в трассе. Не зависит от режима `grill`.

Исходники: [pkg/llmgate](../pkg/llmgate/).

### humandecision — Решение человека

Единый сервисный путь решения о переходе этапа для Web, REST и MCP: действие из разрешённых, точный хеш предмета, роль, запрет самоподтверждения, сохранение решения и аудит `human_gate_decided`; повтор доставки — `human_gate_decision_replayed`, а не второе решение.

Исходники: [pkg/humandecision](../pkg/humandecision/).

### safeio — Файловые primitives

Ограничивает regular-file чтение и проверяет пути; immutable write отвергает существующий файл и статические symlink-компоненты. ReadRegularFile отдельно защищает leaf; полную защиту родителей нельзя предполагать.

Исходники: [pkg/safeio](../pkg/safeio/).

### scheduler — Persistent queue

Хранит jobs и leases, выполняет claim/retry через poller и worker protocol. Не меняет approval policy самого run.

Исходники: [pkg/scheduler](../pkg/scheduler/).

### scope — Mutation path policy

Нормализует repository-relative пути, сопоставляет glob и классифицирует source/tests/meta/infra/generated. Pipeline сравнивает фактическую mutation с этой policy.

Исходники: [pkg/scope](../pkg/scope/).

### strictjson — Строгий JSON decode

Ограничивает вход, отклоняет лишние/невалидные структуры по decoder-контракту. Используется на границах доверенных typed данных.

Исходники: [pkg/strictjson](../pkg/strictjson/).

### tokenbudget — Бюджет токенов

Пересчитывает расход токенов по attempt-манифестам за сутки UTC и решает, исчерпан ли бюджет задачи или суток. Неизвестный расход при заданном лимите считается исчерпанием.

Исходники: [pkg/tokenbudget](../pkg/tokenbudget/).

### ui — Консольное представление

Цвета и progress для человека. Бизнес-решение принадлежит workflow/pipeline, а не UI.

Исходники: [pkg/ui](../pkg/ui/).

### verdict — Парсер verdict marker

Извлекает канонический verdict или BLOCKED-протокол из артефакта. Отсутствующий/неоднозначный marker не подменяется догадкой.

Исходники: [pkg/verdict](../pkg/verdict/).

### web — HTTP control plane

Предоставляет API, auth/session, WebSocket и dashboard projection. Подпакет store хранит SQLite projection; это не замена immutable evidence.

Исходники: [pkg/web](../pkg/web/).

### worker — Disposable worker protocol

Описывает job/result, process launcher и передачу run в отдельный процесс. Disposable process сам по себе не является контейнером.

Исходники: [pkg/worker](../pkg/worker/).

### workflow — Доменные типы и переходы

Определяет state/outcome, graph и чистые решения переходов. Не вызывает LLM или Git; pipeline исполняет принятое решение.

Исходники: [pkg/workflow](../pkg/workflow/).

### cicd — CI/CD deploy-адаптеры

Controller-owned адаптеры доставки (TeamCity): запуск сборки точного уже влитого коммита, ожидание финального статуса, URL сборки как результат этапа. Агент не получает учётные данные провайдера.

Исходники: [pkg/cicd](../pkg/cicd/).

### dockerrun — Запуск программы в контейнере

Один примитив `docker run` без shell с безопасными умолчаниями (сеть none, вход только на чтение, без Docker socket, образ по digest, удаление контейнера по имени). Его используют Docker-адаптер гейта (`gaterun`) и Docker-исполнитель этапа (`stageexec`); о гейтах и этапах пакет ничего не знает.

Исходники: [pkg/dockerrun](../pkg/dockerrun/).

### gatelib — Библиотека гейтов

Версионированный реестр определений гейтов (`required_sections`, `verdict_marker`, `pr_*`, `project_checks`, `required_phrases`, `agent_review@1/2`, `human_approval@1/2`, `external_program`, `docker_program`). Меняется только через PR; определение неизменяемо, новая семантика — новая версия. Каталог читают UI, REST (`GET /api/gates/catalog`) и MCP.

Исходники: [pkg/gatelib](../pkg/gatelib/).

### gatephrases — Гейт required_phrases

Детерминированная проверка наличия слов и фрагментов в Markdown-результате: регистр, область (весь результат или раздел) и правило совпадения (все, любое, не менее N; подстрока или слово) заданы явно, RE2 только по флагу.

Исходники: [pkg/gatephrases](../pkg/gatephrases/).

### gaterun — Раннер гейта внешней программы

JSON-протокол гейта (вход, результат, строгая валидация), запуск доверенной команды без shell под лимитами времени и вывода, evidence; Docker-адаптер `docker_program` использует тот же протокол. Валидный `fail` — успешный ответ процесса; ненулевой код, неверный JSON и потеря процесса — техническая ошибка.

Исходники: [pkg/gaterun](../pkg/gaterun/).

### gatesched — Планировщик групп гейтов

Исполняет цепочку гейтов выхода: группы последовательно, гейты внутри группы параллельно с пределом конкуренции. Каждая попытка и переход пишутся в журнал до и после запуска, поэтому после рестарта завершённый вызов не повторяется, а прерванный честно учитывается. На `fail` следующие группы не стартуют.

Исходники: [pkg/gatesched](../pkg/gatesched/).

### humanartifact — Правки человека

Append-only ревизии и комментарии человека к артефактам run; артефакты агента и evidence не перезаписываются.

Исходники: [pkg/humanartifact](../pkg/humanartifact/).

### oidc — Вход через SSO

Сторона relying party для OpenID Connect: discovery, authorization code с PKCE (S256), проверка ID token по JWKS. Только RS256/ES256, точный issuer, audience, срок и nonce; отказ по умолчанию.

Исходники: [pkg/oidc](../pkg/oidc/).

### prchecks — Проверки pull request

Controller-side проверки, которые привязывают PR к неизменяемым идентификаторам коммитов до анализа diff и запуска project checks в отдельном worktree.

Исходники: [pkg/prchecks](../pkg/prchecks/).

### runid — Валидация идентификатора run

Общая валидация run ID для хранилищ, которыми владеет контроллер.

Исходники: [pkg/runid](../pkg/runid/).

### stagecheck — Декларативные проверки этапа

Проверки результата этапа после попытки (`required_sections`, `verdict_marker`, PR contract, project checks) и отдельный человекочитаемый отчёт. Для схемы конфига v6 те же проверки задаются гейтами через `gatelib`.

Исходники: [pkg/stagecheck](../pkg/stagecheck/).

### stagestatus — Статусы этапов

Единственная серверная проекция «где стоит каждый этап» (S11): чистая функция от durable-журнала событий, approvals, checkpoint жизненного цикла и закреплённого шаблона. Ничего не хранится и не редактируется вручную; API, MCP и UI показывают одну и ту же проекцию.

Исходники: [pkg/stagestatus](../pkg/stagestatus/).

### tracker — Трекер задач

Абстрактный `Tracker` (Jira и подобные), адаптеры `jira` и `fake`, чистое планирование исходящих операций из событий этапов (`on_enter`/`on_pass`) и входящих изменений. Связь «задача — карточка» 1:1, журнал доставок и курсор принадлежат web-слою; изменение статуса в трекере двигает граф только через штатный контроллер.

Исходники: [pkg/tracker](../pkg/tracker/).

## Где искать конкретный инвариант

| Вопрос | Владелец | Смежная проверка |
|---|---|---|
| Можно ли перейти после verdict? | workflow + pipeline | Required checks и approval edge |
| Тот ли код проверили? | candidate + checks + gate | Workspace digest, tracked/index/untracked identity |
| Можно ли доставить? | pipeline delivery authorization | approval + delivery plan + evidence |
| Можно ли продолжить после рестарта? | lifecycle + RunEngine | evidence replay + provenance drift |
| Можно ли передать bundle наружу? | export / CLI gate publisher | redact policy + optional DSSE signature |
| Можно ли удалить run? | retention | Verified export record |

При изменении пакета запускайте его тесты и тесты связанной границы.
Практический цикл — в [руководстве разработчика](DEVELOPMENT.md).
