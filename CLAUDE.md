# ai-team — проект

## Структура

- `cmd/ai-team/main.go` — точка входа CLI
- `pkg/` — внутренние пакеты
  - `config/` — конфигурация
  - `artifact/` — пути артефактов
  - `runtime/` — Runtime interface + AgentCLI
  - `agent/` — Agent struct + Registry
  - `pipeline/` — оркестрация
- `agents/{name}/` — встроенные агенты (def.yaml + prompt.md)
- `e2etest/` — тестовые проекты и E2E-тесты
- `openspec/` — OpenSpec change history

## Разработка через OpenSpec

1. `/opsx:propose "feature-name"` — создать change
2. review proposal, specs, design, tasks
3. `/opsx:apply` — реализовать
4. `/opsx:archive` — архивировать

## Команды

```bash
make build    # сборка
make test     # тесты всех пакетов
make clean    # очистка
```
