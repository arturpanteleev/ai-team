## Контекст

Создаётся новый Go-проект — CLI-инструмент `ai-team`. Проект использует OpenSpec для собственной разработки. Инструмент запускается в любом целевом проекте и выполняет пайплайн AI-агентов.

**Текущее состояние:** Пустой репозиторий, установлен OpenSpec CLI (`.opencode/`, `openspec/`).

**Ограничения:**
- Go 1.26+ (доступен)
- OpenCode CLI (установлен у автора, проверка наличия встроена в инструмент)
- Без прямых API-ключей к LLM (только Agent CLI runtime)
- Агенты вызывают `opencode --resume --message-file <prompt>`

## Цели / Не-цели

**Цели:**
- Go-модуль с CLI и пакетами (config, artifact, runtime, agent, pipeline)
- 6 встроенных агентов с def.yaml и prompt.md
- Runtime: agentcli (opencode), заглушка llm
- Команды: `init` (создаёт .ai-team/), `run` (запускает пайплайн), `list` (список агентов)
- Артефакты в формате OpenSpec: proposal.md, specs/, design.md, tasks.md

**Не-цели:**
- Прямое LLM API (v1 — только agentcli)
- Внешние registry/кэш
- GUI/веб-интерфейс
- Параллельное выполнение агентов

## Решения

### Решение 1: Один Go-модуль, внутренние пакеты
- Все пакеты в одном `go.mod` (`github.com/arturpanteleev/ai-team`)
- `cmd/ai-team/main.go` — точка входа CLI
- `pkg/` — внутренние пакеты
- **Почему:** простота разработки и сборки

### Решение 2: Runtime через интерфейс
```go
type Runtime interface {
    Execute(ctx context.Context, agent *Agent, task *Task) error
}
```
- `AgentCLIRuntime` — пишет prompt.md, запускает `opencode --message-file`
- `LLMRuntime` — заглушка, возвращает `ErrNotImplemented`
- **Почему:** можно добавить другие Runtime без изменения агентов

### Решение 3: Агент описан через def.yaml
- Каждый агент в `agents/{name}/def.yaml`
- Поля: name, runtime, cli, prompt_file, inputs, outputs
- `pkg/agent/registry.go` загружает их
- **Почему:** агентов легко добавлять и конфигурировать

### Решение 4: Артефакты в формате OpenSpec
- `.ai-team/{feature}/`:
  - `proposal.md` — от Analyst
  - `specs/` — от Analyst
  - `design.md` — от Architect
  - `tasks.md` — от Architect
- **Почему:** единый формат, OpenSpec-совместимость, без дублирования

### Решение 5: Agent CLI runtime вызывает opencode с --message-file
- Готовит промпт: system prompt + контекст из input-артефактов
- Пишет во временный файл
- Запускает: `opencode --resume --message-file /tmp/ai-team-xxx.md`
- **Почему:** --message-file надёжнее stdin для длинных контекстов

## Риски / Компромиссы

- **[Зависимость от OpenCode]** → Проверка наличия при `init` и `run`, понятная ошибка
- **[Нет API-ключей]** → v1 только agentcli, llm — заглушка
- **[OpenCode может не справиться]** → Fallback: повторный запуск с более детальным промптом
- **[Длинные артефакты]** → Промпт ссылается на файлы, а не копирует их содержимое
