## ADDED Requirements

### Requirement: Notifier interface
Система ДОЛЖНА определять интерфейс `Notifier` для уведомлений о завершении этапов.

#### Scenario: Интерфейс содержит метод Notify
- **КОГДА** описан `Notifier` интерфейс
- **ТОГДА** он ДОЛЖЕН содержать метод `Notify(ctx context.Context, stage StageResult, task *Task) error`
- **И** `StageResult` ДОЛЖЕН содержать имя агента, статус, сообщение, время выполнения

### Requirement: ConsoleNotifier — реализация по умолчанию
Система ДОЛЖНА включать `ConsoleNotifier` — реализацию, выводящую уведомления в консоль.

#### Scenario: ConsoleNotifier выводит уведомление
- **КОГДА** этап завершается
- **ТОГДА** `ConsoleNotifier` ДОЛЖЕН вывести в stdout: `[ai-team] analyst completed ✓ (12.3s)`
- **И** при ошибке: `[ai-team] coder failed ✗: error message`

### Requirement: Notifier цепочка
Система ДОЛЖНА поддерживать цепочку из нескольких Notifier-ов.

#### Scenario: Множественные notifier-ы
- **КОГДА** зарегистрировано несколько Notifier-ов
- **ТОГДА** при завершении этапа ДОЛЖНЫ быть вызваны все зарегистрированные Notifier-ы
- **И** ошибка одного Notifier-а НЕ ДОЛЖНА блокировать остальные

### Requirement: Замена реализации notifier
Система ДОЛЖНА позволять замену `ConsoleNotifier` на другую реализацию без изменения кода пайплайна.

#### Scenario: Подключение нового Notifier
- **КОГДА** пользователь передаёт новую реализацию `Notifier` в пайплайн
- **ТОГДА** пайплайн ДОЛЖЕН использовать переданную реализацию вместо `ConsoleNotifier`
- **И** НЕ ДОЛЖЕН требовать изменений в `pkg/pipeline/pipeline.go`
