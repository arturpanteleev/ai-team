## Контекст

Проект уже имеет unit-тесты (14 тестов, все проходят) и базовую структуру. Нужно добавить CI, E2E-тесты и eval-систему.

**Текущее состояние:**
- Go-модуль, 5 пакетов, CLI, 6 агентов
- Unit-тесты: config, artifact, runtime, agent, pipeline
- OpenSpec для SDD-разработки

**Ограничения:**
- В CI нет opencode (и быть не может — это AI-агент)
- Нужен mock для E2E
- Eval должен уметь оценивать текстовые артефакты

## Цели / Не-цели

**Цели:**
- GitHub Actions: build + test + vet на каждый push/PR
- Mock opencode для E2E
- E2E-тест: полный пайплайн + верификация артефактов
- Eval-пакет: чтение output агента, прогон через LLM-судью (опционально)
- Команда `ai-team eval`

**Не-цели:**
- Eval без opencode (всегда вызывает opencode как судью)
- Автоматический запуск eval в CI (только вручную, т.к. нужен opencode)

## Решения

### Решение 1: Mock opencode — bash-скрипт
- `testdata/mock-opencode.sh` распознаёт агента по содержимому `--message-file`
- Создаёт файлы в `--message-file` директории (или по путям из промпта)
- Для Deployer: проверяет review и test-report, если REJECTED/FAIL — падает
- **Почему:** bash скрипт проще Go-мока, не требует компиляции

### Решение 2: E2E-тест на Go
- Ставит `testdata/` на первое место в PATH
- Создаёт временный проект, запускает `ai-team init` и `ai-team run`
- Верифицирует что созданы все артефакты
- Тестирует сценарий отказа: mock-opencode возвращает ошибку

### Решение 3: Eval — пакет pkg/eval
```go
type Eval struct {
    AgentName string
    ArtifactPath string
    Criteria []string
}

type Result struct {
    Score int
    Comment string
}
```
- `eval.Run()` вызывает opencode как судью
- Критерии: полнота, тестируемость, отсутствие двусмысленностей
- **Почему:** вызов opencode как судьи — тот же AgentCLI runtime

### Решение 4: CLI `ai-team eval`
```
ai-team eval --feature "hello-api" --task "..."
```
- Запускает пайплайн (опционально, если артефакты уже есть)
- Для каждого артефакта запускает eval
- Печатает таблицу результатов

## Риски / Компромиссы

- **[Mock может отличаться от реального opencode]** → Mock минимален, тестирует только pipeline flow, а не качество
- **[Eval требует opencode]** → Eval не запускается в CI, только локально
- **[Eval дорогой]** → Каждый eval — вызов opencode = токены. Использовать осмысленно
