## Context

Текущий pipeline (`pkg/pipeline/pipeline.go`) выполняет агентов последовательно и поддерживает два типа переходов: `auto` (немедленный переход) и `by_confirm` (пауза с запросом Y/n/diff/summary). Механизм `by_confirm` привязан к конкретному агенту и предназначен для контроля перехода между этапами.

Для полноценного dev-task-workflow необходимы **workflow-level gate-точки** — точки остановки pipeline на логических фазах workflow с:
- Показом резюме выполненной фазы
- Запросом явного подтверждения пользователя
- Возможностью остановки pipeline при обнаружении блокеров

Текущая архитектура pipeline:
- `pipeline.Run()` — основной цикл выполнения агентов
- `AgentConfig` — конфигурация per-agent (model, effort, cli, transition, max_retries)
- `StageResult` — событие завершения этапа (имя, статус, длительность, входы/выходы)
- `Notifier` — система уведомлений (console, chain)
- `workflow.go` — вспомогательные функции (hasGitChanges, promptContinue, readVerdictFromDir)

## Goals / Non-Goals

**Goals:**
- Добавить `PipelineGate` механизм — остановка pipeline на gate-точках с запросом подтверждения
- Добавить `BLOCKED` статус — агент может сигнализировать о блокере (противоречия, неполные требования)
- При BLOCKED: остановить pipeline, показать блокер, предложить `retry-from <agent>`
- Поддерживать gate-точки в конфигурации: `gate_after`, `gate_before`
- Сохранить обратную совместимость с текущими `auto` и `by_confirm` переходами

**Non-Goals:**
- Автоматический rollback на предыдущие этапы (только остановка + предложение)
- Изменение формата артефактов
- Изменение агентов (промпты, def.yaml) — это отдельный change
- Поддержка нескольких gate-точек на одном агенте

## Decisions

### Decision 1: Gate-точки как поля в AgentConfig

**Выбор:** Добавить `gate_after` и `gate_before` в `AgentConfig`.

**Альтернативы:**
- Отдельный массив `gates` в конфиге — сложнее парсинг, нет привязки к агенту
- Аннотации в pipeline массиве (`pipeline: [{name: analyst, gate: after}]`) — нарушает обратную совместимость

**Рationale:** Поля `gate_after` и `gate_before` естественно дополняют существующий `AgentConfig` и не ломают текущий формат конфига. Обратная совместимость сохраняется — если поля отсутствуют, gate-точек нет.

### Decision 2: PipelineGate как отдельная структура

**Выбор:** Ввести тип `PipelineGate` в `pkg/pipeline/`:

```go
type GateType string
const (
    GateAfter  GateType = "after"
    GateBefore GateType = "before"
)

type PipelineGate struct {
    AgentName string
    Type      GateType
    Summary   string // резюме фазы (заполняется при остановке)
}
```

**Альтернативы:**
- Хранить gate-состояние прямо в AgentConfig — нарушает разделение конфига и runtime
- Использовать существующий `by_confirm` с флагом — не даёт показ резюме фазы

**Rationale:** Отдельная структура позволяет отделить конфигурацию gate от конфигурации агента и хранить runtime-состояние (summary).

### Decision 3: BLOCKED как отдельный статус

**Выбор:** Добавить `BLOCKED` статус в `StageResult` (помимо `passed`/`failed`).

**Альтерративы:**
- Использовать `failed` с специальным сообщением — не отличим от ошибки
- Вернуть ошибку из runtime — pipeline остановится, но без механизма retry-from

**Rationale:** `BLOCKED` явно сигнализирует "требуется вмешательство пользователя" vs "произошла ошибка". Это позволяет pipeline предложить retry-from вместо простого завершения.

### Decision 4: Gate interaction через stdin

**Выбор:** Gate-точки используют тот же механизм `promptContinue()` что и `by_confirm`.

**Альтернативы:**
- WebSocket/HTTP callback — требует web UI, который ещё не создан
- Автоматическое продолжение — не соответствует требованию явного подтверждения

**Rationale:** Переиспользование существующего `promptContinue()`.minimizes изменений. Неинтерактивный режим (pipe) работает как auto — gate пропускается.

### Decision 5: Порядок pipeline с verifier

**Выбор:** Дефолтный pipeline: `analyst → architect → coder → reviewer → tester → verifier → deployer`

**Альтернативы:**
- `analyst → architect → coder → reviewer → verifier → tester → deployer` — verifier до tester
- Без verifier — использовать существующих reviewer + tester

**Rationale:** Verifier проверяет AC и self-review после всех проверок кода и тестов, перед деплоем. Это логически соответствует dev-task-workflow: verification pass → delivery.

## Risks / Trade-offs

- **[Risk]** Gate-точки могут замедлить pipeline в автоматическом режиме → **Mitigation:** Неинтерактивный режим (pipe) пропускает gate автоматически
- **[Risk]** BLOCKED статус может быть неочевиден для существующих агентов → **Mitigation:** Агенты не обязаны использовать BLOCKED; это опциональная возможность
- **[Risk]** Изменение дефолтного pipeline порядка сломает существующие конфиги → **Mitigation:** Существующие конфиги с явным `pipeline: [...]` не затрагиваются; изменение только дефолта
