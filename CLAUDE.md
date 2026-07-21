# ai-team — проект

## Структура

- `cmd/ai-team/main.go` — точка входа CLI
- `pkg/` — внутренние пакеты
  - `config/` — конфигурация + валидация
  - `checks/` — deterministic verification runner и evidence
  - `delivery/` — строгий plan и controller-owned executor
  - `evidence/` — immutable run/attempt manifests и append-only events
  - `verdict/` — verdict-контракт (парсер вердиктов, BLOCKED-протокол)
  - `runtime/` — Runtime interface + AgentCLI (промпт, логи, model/effort)
  - `agent/` — Agent struct + Registry
  - `pipeline/` — оркестрация, enforcement вердиктов, гейты, loopback
  - `workflow/` — доменные state/outcome типы и чистые переходы
  - `notifier/`, `report/`, `ui/` — уведомления, HTML-отчёты, консоль
  - `web/` — HTTP API + SQLite store + StoreRecorder (дашборд)
- `agents/{name}/` — встроенные агенты (def.yaml + prompt.md)
- `web/` — React-фронтенд дашборда
- `e2etest/` — mock-opencode и E2E-тесты
- `openspec/` — OpenSpec change history

## Разработка через OpenSpec

### Цикл (строгий порядок)

1. **`/opsx:explore`** — исследовать код, задавать вопросы, уточнять требования.
   Дождаться, пока пользователь скажет «ок, хватит explore».
2. **`/opsx:propose "feature-name"`** — создать change, написать **только proposal**.
   Дождаться approval пользователя.
3. **Design** — написать design.md. Дождаться approval.
4. **Specs** — написать specs/. Дождаться approval.
5. **Tasks** — написать tasks.md. Дождаться approval.
6. **`/opsx:apply`** — реализовать.
7. **`/opsx:archive`** — архивировать.

### Gate rule

**CRITICAL**: ни один следующий артефакт не пишется без явного «ок, давай дальше»
от пользователя. Исключение — пользователь прямо сказал «сделай всё сразу».
То же для apply: не начинать реализацию, пока все 4 артефакта не утверждены.

Runtime-инвариант отдельный: approval обычных checkpoints никогда не разрешает
delivery. Commit/push/PR выполняет только контроллер после deterministic checks и
approval точного SHA-256 canonical plan через `--approve-plan <sha256>`.

## Команды

```bash
make build    # сборка
make test     # тесты всех пакетов
make specs    # строгая OpenSpec-валидация
make verify   # полный race/security/frontend verification
make clean    # очистка
```
