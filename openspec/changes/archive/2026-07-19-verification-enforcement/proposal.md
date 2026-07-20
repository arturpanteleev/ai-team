## Why

Аудит (см. `AUDIT.md`) показал: заявленные гарантии верификации не обеспечиваются кодом. Вердикты reviewer/tester/verifier никем не проверяются (хотя спеки `agent-reviewer` и `agent-tester` требуют остановку пайплайна), протокол BLOCKED не имеет механизма передачи, loopback читает вердикт из несуществующего пути, в неинтерактивном режиме пайплайн доходит до `git push` без единой контрольной точки, verifier запускается с пустым промптом, eval не парсит оценку. Форматы вердиктов в промптах не совпадают с парсером.

Нужен единый, детерминированный слой контроля: машиночитаемый контракт вердиктов + enforcement в харнессе. Принцип: **LLM производит артефакты и вердикты; решения о переходах принимает только код пайплайна**.

## What Changes

1. **Verdict-контракт** — канонический машиночитаемый формат вердикта в артефактах (`**Verdict:** ...` / `**Result:** ...`, line-anchored) + файл `{feature}/status/{agent}.md` для сигнала BLOCKED. Новый пакет `pkg/verdict` с парсером и contract-тестами. Промпты всех агентов синхронизированы с контрактом; служебные требования (verdict, summary, blocked) харнесс добавляет в промпт сам (одно место — `buildPrompt`).
2. **Verdict-enforcement** — после каждого этапа харнесс парсит вердикт и применяет политику: негативный вердикт (REJECTED / CHANGES_REQUESTED / FAIL) по умолчанию останавливает пайплайн; в интерактивном режиме перед остановкой предлагается loopback (если сконфигурирован); поведение переопределяется `on_negative_verdict: stop|ask|continue`.
3. **BLOCKED end-to-end** — агент сигнализирует блокировку файлом `status/{agent}.md`; харнесс детектирует его до проверки выходов, останавливает пайплайн со статусом `blocked`, exit-код 2, подсказка `--retry-from`.
4. **Loopback починен** — вердикт читается из реальных выходов агента; при retry coder получает review.md дополнительным входом; при исчерпании `max_retries` (конфиг coder-а) пайплайн останавливается с ошибкой.
5. **Таймауты этапов** — `stage_timeout` глобально и `timeout` per-agent; `init` включает 30m по умолчанию.
6. **Exit-коды** — 0 успех, 1 ошибка/негативный вердикт, 2 blocked, 3 остановлен пользователем на gate/confirm. SIGINT обрабатывается корректно (итоговый отчёт генерируется).
7. **Наблюдаемость** — stdout/stderr агентов пишутся в `.ai-team/logs/{feature}/{agent}.log`; запуски и этапы (со свежими вердиктами) записываются в SQLite (`.ai-team/web.db`) при каждом `run` — web-дашборд наполняется без отдельной интеграции.
8. **Model/effort реально применяются** — `model` передаётся флагом CLI (для opencode `-m`), `effort` вшивается служебной секцией в промпт (универсально для любого CLI).
9. **Спека↔код синхронизация** — канонический вызов `opencode run <prompt>` фиксируется в спеке (вместо трёх противоречащих описаний); verifier получает `prompt_file`; формат вердикта verifier приводится к контракту; git-diff-guard становится fail-closed; `init` сериализует полный дефолтный конфиг (с гейтами).

## Capabilities

### New Capabilities

- `verdict-contract`: Машиночитаемый контракт вердиктов и статусов агентов (формат маркеров, файл status/, служебная секция промпта)
- `verdict-enforcement`: Детерминированная обработка вердиктов харнессом — политики остановки/loopback/продолжения, поведение в неинтерактивном режиме

### Modified Capabilities

- `pipeline-blocked-status`: определён механизм сигнала BLOCKED (status-файл), унифицирован exit-код 2
- `workflow-loopback`: вердикт из реальных выходов, review.md во входе при retry, остановка при исчерпании retries
- `stage-summary`: определён производитель summary (служебная секция промпта), путь с `{feature}`
- `opencode-integration`: канонический вызов `opencode run <prompt>`, передача model флагом `-m`
- `role-config`: способ доставки model (флаг CLI) и effort (секция промпта)
- `agent-orchestration`: таймауты этапов, логи агентов, обработка SIGINT
- `cli-interface`: exit-коды, `--help`/`-h`, валидация `--feature`, полный usage
- `deployer-constraints`: обязательный шаг создания ветки, запрет push в default-ветку
- `git-diff-guard`: fail-closed при ошибках git, warning при отсутствии git-репозитория
- `project-init`: сериализация полного дефолтного конфига (гейты, таймаут), создание .gitignore при отсутствии, удаление неиспользуемых каталогов
- `web-pipeline-integration`: запись запусков/этапов в SQLite из CLI-процесса (без WebSocket-хаба)
- `verifier-verification-pass`: формат вердикта приведён к verdict-контракту
- `ci-pipeline`: проверка gofmt, сборка frontend
- `agent-eval`: захват и парсинг вывода судьи, детерминированный вызов без `--resume`

## Impact

- Новый пакет `pkg/verdict`; `pkg/pipeline` (enforcement, blocked, loopback, таймауты, декомпозиция Run); `pkg/runtime` (служебная секция промпта, model/effort, логи); `pkg/config` (валидация, новые поля, сериализация Default); `cmd/ai-team` (exit-коды, сигналы, usage, валидация feature); `pkg/eval` (захват+парсинг); `pkg/web` (StoreRecorder вместо нерабочего WebNotifier, безопасность artifact-endpoint); `agents/*/prompt.md` и `agents/verifier/def.yaml`; `e2etest` (честные сценарии rejected/fail/blocked); `.github/workflows/ci.yaml`; `web/src` (контракты API/WS, статусы).
