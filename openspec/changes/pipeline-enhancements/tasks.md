## 1. pkg/ui — Color и Progress Bar

- [x] 1.1 Создать `pkg/ui/color.go` с ANSI-константами и функциями `Colorize(text, color) string`, `IsTerminal() bool`
- [x] 1.2 Создать `pkg/ui/progress.go` с `ProgressBar` структурой: `New(name, feature string, total int)`, `Next(agent string)`, `Done()`, `Render(format string, args...)`
- [x] 1.3 Добавить `pkg/ui/pipeline_status.go` с `PipelineStatus` — обёрткой вокруг ProgressBar для отображения проекта/фичи/агента

## 2. pkg/notifier — Notifier System

- [x] 2.1 Создать `pkg/notifier/notifier.go` с интерфейсом `Notifier { Notify(ctx, StageResult) error }` и структурой `StageResult`
- [x] 2.2 Создать `pkg/notifier/console.go` с `ConsoleNotifier` — вывод в stdout: `[ai-team] <agent> <status> (<duration>)`
- [x] 2.3 Создать `pkg/notifier/chain.go` с `NotifierChain` для композиции нескольких notifier-ов

## 3. pkg/report — HTML Reports

- [x] 3.1 Создать `pkg/report/templates/` с go:embed HTML-шаблонами (stage.html + final.html)
- [x] 3.2 Создать `pkg/report/generator.go` с `GenerateStageReport(reportsDir, feature, agent, result, artifacts) error`
- [x] 3.3 Создать `pkg/report/generator.go` с `GenerateFinalReport(reportsDir, feature, stages []StageResult) error`
- [x] 3.4 Отчёты содержат ссылки на md-артефакты (относительные пути)

## 4. pkg/config — Role-based Configuration

- [x] 4.1 Обновить `Config` структуру: `PipelineAgents []AgentConfig` где `AgentConfig { Name, Model, Effort, CLI string }`
- [x] 4.2 Добавить парсинг нового формата (`pipeline:` как массив объектов с `name:`)
- [x] 4.3 Сохранить обратную совместимость: старый формат `pipeline: [list]` → `PipelineAgents` с глобальными model/cli/effort
- [x] 4.4 Добавить поле `Effort string` в глобальный конфиг

## 5. cmd/ai-team — CLI Flags and Colored Output

- [x] 5.1 Добавить флаг `--retry-from <agent>` в команду `run`
- [x] 5.2 Перевести вывод `ai-team run` на цветной через `pkg/ui/color.go`
- [x] 5.3 Добавить отображение прогресс-бара во время выполнения

## 6. pkg/pipeline — Orchestration Integration

- [x] 6.1 Расширить `StageResult` — добавить `Status`, `Message`, `Duration`, `StageIndex`, `TotalStages`, `Inputs`, `Outputs`
- [x] 6.2 Интегрировать `Notifier`: создать `ConsoleNotifier` по умолчанию, вызывать `Notify()` после каждого этапа
- [x] 6.3 Интегрировать отчёты: вызывать `GenerateStageReport()` после каждого этапа, `GenerateFinalReport()` в конце
- [x] 6.4 Интегрировать прогресс-бар: обновлять строку состояния перед/после каждого этапа
- [x] 6.5 Интегрировать ролевую конфигурацию: передавать model/effort/cli при запуске агента
- [x] 6.6 Реализовать `--retry-from`: пропускать этапы до указанного, валидировать артефакты пропущенных этапов
- [x] 6.7 Цветной вывод логов пайплайна (статус, время, ошибки)

## 7. Verify and Test

- [x] 7.1 `make test` — все тесты проходят
- [x] 7.2 `make build` — бинарник собирается
- [x] 7.3 Проверить `ai-team init` — создаётся `.ai-team/reports/`
- [x] 7.4 Проверить `ai-team run --retry-from <agent>` — корректный пропуск этапов
- [x] 7.5 Проверить цветной вывод в терминале и его отсутствие в pipe
- [x] 7.6 Проверить старый формат config.yaml — обратная совместимость
- [x] 7.7 Проверить HTML-отчёты — открываются в браузере, ссылки работают
