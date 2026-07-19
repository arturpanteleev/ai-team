## 1. Конфигурация и типы

- [x] 1.1 Добавить поля `GateAfter`, `GateBefore` bool в структуру `AgentConfig` в `pkg/config/config.go`
- [x] 1.2 Обновить кастомный YAML unmarshaling в `config.go` для парсинга новых полей
- [x] 1.3 Добавить метод `HasGateAfter()` и `HasGateBefore()` в `AgentConfig`
- [x] 1.4 Обновить `config.Default()` в `load.go` для поддержки gate полей по умолчанию

## 2. Типы pipeline и gate

- [x] 2.1 Создать тип `GateType` (after/before) и структуру `PipelineGate` в `pkg/pipeline/`
- [x] 2.2 Добавить поля `GateAfter`, `GateBefore` в `Agent` struct (runtime)
- [x] 2.3 Добавить функцию `findGates(agents []Agent) []PipelineGate` для поиска gate-точек в pipeline

## 3. BLOCKED статус

- [x] 3.1 Добавить константу `StatusBlocked = "blocked"` в `pkg/notifier/notifier.go`
- [x] 3.2 Расширить `StageResult` полем `Blocker string` для описания блокера
- [x] 3.3 Обновить `ColoredStatus()` в `ui/color.go` для отображения BLOCKED

## 4. Логика gate в pipeline

- [x] 4.1 Реализовать метод `checkGateAfter(agent Agent, result StageResult) bool` в `pipeline.go`
- [x] 4.2 Реализовать метод `checkGateBefore(agent Agent) bool` в `pipeline.go`
- [x] 4.3 Реализовать метод `showGateSummary(agent Agent, result StageResult)` — показ резюме фазы
- [x] 4.4 Реализовать метод `promptGate(gate PipelineGate) bool` — запрос подтверждения Y/n
- [x] 4.5 Интегрировать gate-проверки в основной цикл `Pipeline.Run()`: после выполнения агента проверить gate_after, перед следующим — gate_before

## 5. BLOCKED логика в pipeline

- [x] 5.1 Добавить обработку `StatusBlocked` в цикле `Pipeline.Run()` — остановка pipeline
- [x] 5.2 Реализовать показ блокера: `Блокер: {описание}`
- [x] 5.3 Реализовать предложение retry-from: `ai-team run --retry-from {agent_name}`
- [x] 5.4 Установить код выхода 0 для BLOCKED (отличие от failed)

## 6. Неинтерактивный режим

- [x] 6.1 Проверять `isTerminalStdin()` перед gate-остановкой
- [x] 6.2 В неинтерактивном режиме gate пропускать (как auto)
- [x] 6.3 В неинтерактивном режиме BLOCKED работать как failed (код выхода 1)

## 7. Обновление дефолтного pipeline

- [x] 7.1 Обновить `Registry.DefaultPipeline()` для нового порядка: analyst → architect → coder → reviewer → tester → verifier → deployer
- [x] 7.2 Обновить `config.Default()` для нового порядка
- [x] 7.3 Добавить gate_after: true для analyst и architect в дефолтный pipeline
- [x] 7.4 Добавить gate_before: true для deployer в дефолтный pipeline

## 8. Тесты

- [x] 8.1 Написать unit-тесты для парсинга gate полей в config
- [x] 8.2 Написать unit-тесты для `findGates()`
- [x] 8.3 Написать unit-тесты для gate interaction (promptGate с моком stdin)
- [x] 8.4 Написать unit-тесты для BLOCKED обработки в pipeline
- [x] 8.5 Написать E2E тест для pipeline с gate-точками
