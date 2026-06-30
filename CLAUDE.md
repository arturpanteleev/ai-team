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

## Команды

```bash
make build    # сборка
make test     # тесты всех пакетов
make clean    # очистка
```
