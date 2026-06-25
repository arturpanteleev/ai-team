## 1. Go-модуль и скелет проекта

- [x] 1.1 Инициализировать Go-модуль `github.com/arturpanteleev/ai-team`
- [x] 1.2 Создать дерево директорий: `cmd/ai-team/`, `pkg/{config,artifact,runtime,agent,pipeline}`, `agents/`, `testdata/`
- [x] 1.3 Добавить `.gitignore` (go, openspec, .ai-team)
- [x] 1.4 Создать `Makefile` с целями: `build`, `test`, `clean`

## 2. Пакет Config

- [x] 2.1 Определить структуру `Config` (Feature, Task, CLI, Pipeline)
- [x] 2.2 Реализовать `Load(path string) (*Config, error)` — читает `.ai-team/config.yaml`
- [x] 2.3 Реализовать генерацию конфига по умолчанию
- [x] 2.4 Написать unit-тесты для загрузки конфига

## 3. Пакет Artifact

- [x] 3.1 Определить структуры путей артефактов: TaskPath, ProductSpecPath, TechDesignPath, ReviewPath, TestReportPath
- [x] 3.2 Реализовать разрешение путей по имени фичи
- [x] 3.3 Реализовать структуру `Task` (имя фичи, описание задачи, пути артефактов)
- [x] 3.4 Написать unit-тесты для путей артефактов

## 4. Интерфейс Runtime и AgentCLI

- [x] 4.1 Определить интерфейс `Runtime`: `Execute(ctx, agent, task) error`
- [x] 4.2 Реализовать `AgentCLIRuntime`:
  - [x] 4.2.1 Собрать промпт из system prompt + входных артефактов
  - [x] 4.2.2 Записать промпт во временный файл
  - [x] 4.2.3 Запустить `opencode --resume --message-file` в целевой директории
  - [x] 4.2.4 Проверить exit code и вернуть ошибку при неудаче
- [x] 4.3 Реализовать проверку наличия CLI в системе
- [x] 4.4 Реализовать заглушку `LLMRuntime`, возвращающую ErrNotImplemented
- [x] 4.5 Написать unit-тесты для runtime (мок opencode)

## 5. Пакет Agent

- [x] 5.1 Определить структуру `Agent` (Name, RuntimeType, CLI, Prompt, Inputs, Outputs)
- [x] 5.2 Реализовать `LoadAgent(name string) (*Agent, error)` — читает `agents/{name}/def.yaml`
- [x] 5.3 Реализовать `Registry` со всеми встроенными агентами
- [x] 5.4 Написать unit-тесты для загрузки агентов

## 6. Пакет Pipeline

- [x] 6.1 Определить структуру `Pipeline` с упорядоченным списком агентов
- [x] 6.2 Реализовать `Run(ctx, task) error` — выполнять каждого агента последовательно
- [x] 6.3 Реализовать обработку ошибок: остановка при первом сбое, сообщение о том, какой агент упал
- [x] 6.4 Реализовать настройку пайплайна из config.yaml (поддержка кастомного порядка)
- [x] 6.5 Написать unit-тесты с мок-агентами

## 7. CLI (cmd/ai-team)

- [x] 7.1 Реализовать `main.go` с CLI-фреймворком (cobra или std flag)
- [x] 7.2 Реализовать команду `init`:
  - [x] 7.2.1 Создать структуру директорий `.ai-team/`
  - [x] 7.2.2 Создать config.yaml по умолчанию
- [x] 7.3 Реализовать команду `run`:
  - [x] 7.3.1 Парсить флаги --feature и --task
  - [x] 7.3.2 Записать task.md в артефакты
  - [x] 7.3.3 Запустить пайплайн
- [x] 7.4 Реализовать команду `list` — вывести таблицу агентов
- [x] 7.5 Реализовать команду `version`

## 8. Определения агентов

- [x] 8.1 Создать `agents/analyst/def.yaml` и `prompt.md`
- [x] 8.2 Создать `agents/architect/def.yaml` и `prompt.md`
- [x] 8.3 Создать `agents/coder/def.yaml` и `prompt.md`
- [x] 8.4 Создать `agents/reviewer/def.yaml` и `prompt.md`
- [x] 8.5 Создать `agents/tester/def.yaml` и `prompt.md`
- [x] 8.6 Создать `agents/deployer/def.yaml` и `prompt.md`

## 9. Тестовые данные и интеграция

- [x] 9.1 Создать `testdata/sample-project/` с минимальным Go-проектом
- [x] 9.2 Создать `testdata/sample-project/.ai-team/config.yaml`
- [x] 9.3 Написать интеграционный тест: init → запуск пайплайна на тестовом проекте

## 10. Сборка и документация

- [x] 10.1 Проверить, что `go build ./cmd/ai-team` успешно собирается
- [x] 10.2 Написать `README.md` с примерами использования
- [x] 10.3 Написать `CLAUDE.md` для AI-агентов, работающих над этим проектом
