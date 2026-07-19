## Why

Текущий механизм `by_confirm` работает на уровне отдельных агентов и предназначен для контроля перехода между этапами. Однако для полноценного workflow по решению задач (dev-task-workflow) необходимы **workflow-level gate-точки** — явные точки остановки pipeline с запросом подтверждения пользователя на ключевых этапах: после фиксации продуктовых требований, после технического плана и перед commit/push/PR.

Текущий `by_confirm` не может обеспечить:
- Строгую привязку gate-точек к логическим фазам workflow (а не к конкретным агентам)
- Показ резюме фазы перед подтверждением
- Механизм BLOCKED статуса для остановки pipeline при противоречиях
- Предложение retry-from при обнаружении блокеров

## What Changes

- Добавлен `PipelineGate` тип в конфигурацию пайплайна с поддержкой привязки к агентам (`gate_after`, `gate_before`)
- Pipeline останавливается на gate-точках, показывает резюме выполненной фазы и ждёт подтверждения `Y/n`
- Добавлен `BLOCKED` статус в `StageResult` — агент может сигнализировать о блокере (противоречия, неполные требования)
- При `BLOCKED` pipeline останавливается, показывает блокер и предлагает `retry-from <agent>`
- Gate-точки конфигурируются через `config.yaml`: `gate_after: analyst`, `gate_before: deployer`

## Capabilities

### New Capabilities

- `pipeline-gates`: Workflow-level gate-точки — остановка pipeline на ключевых фазах с запросом подтверждения и показом резюме
- `pipeline-blocked-status`: Механизм BLOCKED статуса для остановки pipeline при обнаружении блокеров с предложением retry-from

### Modified Capabilities

- `workflow-transitions`: Расширение существующего transition机制 — добавление gate-типов помимо `auto` и `by_confirm`
- `role-config`: Добавление полей `gate_after` и `gate_before` в `AgentConfig`

## Impact

- `pkg/config/config.go` — добавление полей `GateAfter`, `GateBefore` в `AgentConfig`
- `pkg/config/load.go` — парсинг новых полей, обновление default pipeline
- `pkg/pipeline/pipeline.go` — логика gate-точек, BLOCKED статус, pause/resume
- `pkg/pipeline/workflow.go` — вспомогательные функции для gate и block
- `pkg/notifier/notifier.go` — расширение `StageResult` для поддержки BLOCKED и gate events
- `.ai-team/config.yaml` — формат конфигурации с gate-точками
