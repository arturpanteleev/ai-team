# Карта модулей

Это индекс к коду: найдите ответственность, затем откройте пакет.
Все 40 верхнеуровневых пакетов `pkg/` перечислены ниже; внутренние типы и
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
