## Why

Текущие промпты агентов (analyst, architect, deployer) не соответствуют требованиям dev-task-workflow. Промпты слишком кратки и не содержат:

- Строгих правил поведения (принцип минимального изменения, запрет на додумывание)
- Структуры артефактов по стандарту скилла (business goal, scope/out-of-scope, AC с категориями)
- Ограничений на формат коммитов и PR (≤10 слов, ≤700 символов)
- Требования на русский язык для артефактов
- Механизма блокировки при противоречиях

Обновление промптов необходимо для.alignment с workflow, который будет управляться через gate-точки (Change 1).

## What Changes

- Обновлённый промпт `analyst`: бизнес-проблема, scope/out-of-scope, AC с категориями (happy path, errors, edge cases,不变 behavior), "не додумывать", "BLOCKED при противоречиях"
- Обновлённый промпт `architect`: затронутые компоненты, контракты, риски, порядок реализации, "вернуться к аналитику при ошибке в требованиях"
- Обновлённый промпт `deployer`: commit ≤10 слов, номер задачи, без атрибуции агента, PR ≤700 символов

## Capabilities

### New Capabilities

- `analyst-workflow-rules`: Правила поведения analyst — принцип минимального изменения, запрет додумывания, BLOCKED при противоречиях
- `architect-workflow-rules`: Правила поведения architect — обнаружение ошибок в требованиях, фиксация рисков
- `deployer-constraints`: Ограничения deployer — формат коммитов, формат PR

### Modified Capabilities

- `agent-analyst`: Обновление структуры proposal.md и spec.md
- `agent-architect`: Обновление структуры design.md и tasks.md
- `agent-deployer`: Добавление ограничений на коммиты и PR

## Impact

- `agents/analyst/prompt.md` — перезапись промпта
- `agents/analyst/def.yaml` — возможно, обновление inputs/outputs
- `agents/architect/prompt.md` — перезапись промпта
- `agents/deployer/prompt.md` — перезапись промпта
