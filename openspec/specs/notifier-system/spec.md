## Purpose

Спецификация определяет нормативное поведение capability `notifier-system`.

## Requirements
### Requirement: Notifier interface
Система MUST определять интерфейс `Notifier` для уведомлений о завершении этапов.

#### Scenario: Интерфейс содержит метод Notify
- **КОГДА** описан `Notifier` интерфейс
- **ТОГДА** он MUST содержать метод `Notify(ctx context.Context, stage StageResult) error`
- **И** `StageResult` MUST содержать run/attempt IDs, имя агента, domain state, status, error и время выполнения

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

### Requirement: Уведомления в пайплайне
Пайплайн MUST вызывать Notifier после каждого завершённого этапа и поддерживать stage_started events.

#### Scenario: Notify вызывается после каждого этапа
- **КОГДА** агент завершает выполнение
- **ТОГДА** пайплайн MUST вызвать `Notifier.Notify()` для всех зарегистрированных notifier-ов
- **И** передать полный доменный StageResult без повторной интерпретации outcome

#### Scenario: Projection adapter
- **КОГДА** pipeline recorder подключён
- **ТОГДА** started/finished lifecycle MUST проецироваться отдельно от completion notifier chain
- **И** ошибка projection MUST быть залогирована, но MUST NOT менять immutable workflow outcome
