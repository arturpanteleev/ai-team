## Зачем

Создать CLI-инструмент `ai-team`, который запускает пайплайн AI-агентов (System Analyst → Architect → Coder → Reviewer → Tester → Deployer) в любом целевом проекте. Агенты общаются через артефакты в формате OpenSpec. Сам инструмент разрабатывается через OpenSpec.

## Что меняется

- Go-модуль `github.com/arturpanteleev/ai-team` со структурой `cmd/`, `pkg/`, `agents/`
- CLI с командами `init`, `run`, `list`, `version`
- Пакеты: `pkg/config`, `pkg/artifact`, `pkg/runtime`, `pkg/agent`, `pkg/pipeline`
- 6 встроенных агентов с `def.yaml` и `prompt.md`
- Runtime: agentcli (вызов opencode --message-file), заглушка llm
- В целевом проекте создаётся `.ai-team/` с артефактами в формате OpenSpec (proposal.md, specs/, design.md, tasks.md)

## Возможности

### Новые возможности
- `cli-interface`: команды `init`, `run`, `list`, `version`
- `project-init`: инициализация `.ai-team/` в целевом проекте
- `agent-orchestration`: пайплайн из 6 агентов, коммуникация через артефакты
- `opencode-integration`: runtime, вызывающий opencode --message-file
- `llm-integration`: заглушка для будущего прямого LLM API
- `agent-analyst`: System Analyst — пишет продуктовую спецификацию (proposal.md + specs/)
- `agent-architect`: Architect — пишет технический дизайн (design.md + tasks.md)
- `agent-coder`: Coder — реализует код через opencode
- `agent-reviewer`: Reviewer — проверяет код на соответствие spec
- `agent-tester`: Tester — пишет и запускает тесты
- `agent-deployer`: Deployer — коммитит и создаёт PR

### Изменённые возможности

Нет (новый проект).

## Влияние

- Создаётся новый Go-проект с нуля
- OpenSpec CLI уже установлен, структура `openspec/` создана
- Зависимости: Go 1.26+, OpenCode (CLI)
