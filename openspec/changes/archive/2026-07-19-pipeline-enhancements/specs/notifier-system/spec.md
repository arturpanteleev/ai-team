## ADDED Requirements

### Requirement: Notifier interface
Система MUST определять интерфейс `Notifier` для уведомлений о завершении этапов.

#### Scenario: Интерфейс содержит метод Notify
- **КОГДА** описан `Notifier` интерфейс
- **ТОГДА** он MUST содержать метод `Notify(ctx context.Context, stage StageResult, task *Task) error`
- **И** `StageResult` MUST содержать имя агента, статус, сообщение, время выполнения

### Requirement: ConsoleNotifier — реализация по умолчанию
Система MUST включать `ConsoleNotifier` — реализацию, выводящую уведомления в консоль.

#### Scenario: ConsoleNotifier выводит уведомление
- **КОГДА** этап завершается
- **ТОГДА** `ConsoleNotifier` MUST вывести в stdout: `[ai-team] analyst completed ✓ (12.3s)`
- **И** при ошибке: `[ai-team] coder failed ✗: error message`

### Requirement: Notifier цепочка
Система MUST поддерживать цепочку из нескольких Notifier-ов.

#### Scenario: Множественные notifier-ы
- **КОГДА** зарегистрировано несколько Notifier-ов
- **ТОГДА** при завершении этапа MUST быть вызваны все зарегистрированные Notifier-ы
- **И** ошибка одного Notifier-а MUST NOT блокировать остальные

### Requirement: Замена реализации notifier
Система MUST позволять замену `ConsoleNotifier` на другую реализацию без изменения кода пайплайна.

#### Scenario: Подключение нового Notifier
- **КОГДА** пользователь передаёт новую реализацию `Notifier` в пайплайн
- **ТОГДА** пайплайн MUST использовать переданную реализацию вместо `ConsoleNotifier`
- **И** MUST NOT требовать изменений в `pkg/pipeline/pipeline.go`
