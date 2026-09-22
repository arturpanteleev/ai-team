# ai-team — проект

## Структура

- `cmd/ai-team/main.go` — точка входа CLI
- `pkg/` — внутренние пакеты
  - `config/` — конфигурация + валидация
  - `checks/` — deterministic verification runner и evidence
  - `scope/` — repository-relative mutation path policy (glob-матчинг)
  - `delivery/` — строгий plan и controller-owned executor
  - `evidence/` — immutable run/attempt manifests и append-only events
  - `verdict/` — verdict-контракт (парсер вердиктов, BLOCKED-протокол)
  - `runtime/` — Runtime interface + AgentCLI (промпт, логи, model/effort)
  - `agent/` — Agent struct + Registry
  - `pipeline/` — оркестрация, enforcement вердиктов, гейты, loopback
  - `workflow/` — доменные state/outcome типы и чистые переходы
  - `safeio/` — no-follow filesystem primitives (symlink rejection)
  - `process/` — process-group supervision и kill (Unix/Windows/plan9)
  - `notifier/`, `report/`, `ui/` — уведомления, HTML-отчёты, консоль
  - `web/` — HTTP API + SQLite store + StoreRecorder (дашборд)
- `agents/{name}/` — встроенные агенты (def.yaml + prompt.md)
- `web/` — React-фронтенд дашборда
- `e2etest/` — mock-opencode и E2E-тесты
- `openspec/` — OpenSpec change history

Подробная карта пакетов и внутреннее устройство — в
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Человеко-читаемое описание
цикла ниже — в [CONTRIBUTING.md](CONTRIBUTING.md).

## Процесс разработки

Планирование — в GitHub Issues. Реализация — обычным путём: ветка, PR,
`Closes #<номер>` в описании, зелёный CI.

### OpenSpec — инструмент, а не обязанность

`openspec/specs/` содержит принятые контракты капабилити, `openspec/changes/` —
проектную проработку. Пользоваться этим **необязательно**. Заводить change
имеет смысл, когда изменение того стоит: затрагивает несколько подсистем,
требует сравнения альтернатив или меняет нормативное поведение, которое потом
кто-то будет оспаривать. Для обычной правки достаточно issue и PR.

Если change заводится — артефакты пишутся по мере надобности, а не все четыре
подряд; строгого порядка и approval на каждом шаге больше нет. Промежуточный
change без delta-спеки не проходит `make specs`, поэтому доводите его до
состояния с хотя бы одной delta либо не коммитьте вовсе.

Единственное, что остаётся обязательным: **если изменилось нормативное
поведение, `openspec/specs/` не должен ему противоречить**. Править спеку можно
прямо, без обряда с change.

Runtime-инвариант продукта к этому отношения не имеет и не ослабляется:
commit/push/PR внутри `ai-team run` выполняет только контроллер после
deterministic checks и approval точного SHA-256 canonical plan через
`--approve-plan <sha256>`.

### Issue и change

Issue — короткий указатель на задачу; подробности живут там, где их удобнее
держать. Если по задаче есть change, он и является источником истины, а issue
его **не повторяет** — то, что нигде не продублировано, разойтись не может.
Если change нет, описание живёт в issue и в PR.

## Команды

```bash
make build    # сборка
make test     # тесты всех пакетов
make specs    # строгая OpenSpec-валидация
make verify   # полный race/security/frontend verification
make clean    # очистка
```
