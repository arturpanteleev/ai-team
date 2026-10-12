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

### Requirement: External notification channels

The system MUST provide webhook, Slack incoming-webhook and SMTP e-mail
notification channels behind a common channel interface. Channel destinations
and credentials MUST be read from environment variables named in the template
and MUST NOT be stored in it. Webhook and Slack destinations MUST be absolute
`https` URLs without credentials; a redirect to another host or scheme MUST be
rejected before the payload is sent there. Every delivery MUST be bounded by a
timeout. Channel errors MUST NOT contain the destination URL, response body or
credentials.

#### Scenario: A webhook redirects to a foreign host

- **WHEN** the configured webhook answers with a redirect to a different host
- **THEN** the foreign host MUST NOT receive the request
- **AND** the delivery MUST fail with an error that does not include either URL

#### Scenario: SMTP credentials without TLS

- **WHEN** the SMTP URL carries credentials for a non-loopback server that does
  not offer TLS
- **THEN** the channel MUST NOT send the credentials or the message

### Requirement: Subscriptions by event type and stage function

Each channel MUST declare the task event types it receives and MAY restrict
them to template stage functions. Configuration MUST reject an unknown event
type, an unknown channel type and a function absent from the project
template. A channel without functions MUST receive matching events of any
stage; a channel with functions MUST receive only events whose stage has one
of them.

#### Scenario: Event of an unsubscribed function

- **WHEN** a channel subscribes to `stage_stalled` for function `qa`
- **AND** `stage_stalled` is stored for a `developer` stage
- **THEN** the channel MUST NOT be notified

#### Scenario: Unknown subscription type

- **WHEN** a channel lists an event type that ai-team does not emit
- **THEN** configuration validation MUST fail

### Requirement: Durable, deduplicated notification delivery

The web server MUST deliver notifications from the durable event store outside
the task execution path, with its own cursor that starts at the event tail on
first enable. It MUST record each handled channel and event pair durably and
MUST NOT notify a channel again for an event already recorded, including after
a reconnect, restart or cursor rewind. A delivery failure MUST be persisted as
a warning `integration_error` with `integration=notifier` and the channel
name, MUST NOT change the task outcome and MUST NOT be retried after it is
persisted. Notifier `integration_error` events MUST NOT be dispatched. If the
delivery record or the failure warning cannot be persisted, the event MUST
remain pending.

#### Scenario: Replay after reconnect

- **WHEN** the notifier re-reads events that it already delivered
- **THEN** no channel MUST receive a second notification for them

#### Scenario: Channel is unavailable

- **WHEN** a subscribed webhook answers with an error status
- **THEN** SQLite MUST contain a warning `integration_error` naming the channel
- **AND** the task MUST keep its status
- **AND** the failure MUST NOT itself trigger a notification
