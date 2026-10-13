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
`https` URLs without credentials; a redirect to another host, another port or
another scheme MUST be rejected before the payload is sent there (an omitted
port equals 443). For `smtp://` the channel MUST use STARTTLS with any
non-loopback host and MUST NOT send the message or credentials when the server
does not offer it. Every delivery MUST be bounded by a
timeout. Channel errors MUST NOT contain the destination URL, response body or
credentials.

#### Scenario: A webhook redirects to a foreign host

- **WHEN** the configured webhook answers with a redirect to a different host
- **THEN** the foreign host MUST NOT receive the request
- **AND** the delivery MUST fail with an error that does not include either URL

#### Scenario: SMTP without TLS

- **WHEN** the SMTP URL names a non-loopback server that does not offer
  STARTTLS, with or without credentials
- **THEN** the channel MUST NOT send the credentials or the message

#### Scenario: A webhook redirects to another port or to http

- **WHEN** the configured webhook redirects to the same host on another port,
  or to `http`
- **THEN** the redirect MUST NOT be followed

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

### Requirement: Notification delivery from every event writer

Notifications MUST be delivered by every process that writes task events into
the shared event store — the web server, a local `ai-team run` and the
scheduler worker — through the same dispatcher, outside the task execution
path, each with its own cursor. The web server and the scheduler
worker MUST keep a durable cursor that starts at the event tail on first
enable; if that cursor cannot be read or initialized, the process MUST NOT
start notification delivery rather than replay from the beginning. A local run
MUST start at the tail when it begins and MUST deliver only events of the runs
it executes; its cursor is in memory. Events a run did not deliver before it
exited are delivered only by a process on the same database whose durable
cursor is still before them. The scheduler worker MUST restore default signal
handling before its final flush, so a second interrupt ends the process.

#### Scenario: Local run sends a subscribed event

- **WHEN** `ai-team run` records an event that a channel subscribes to
- **THEN** the channel MUST receive the notification without `ai-team web` running

#### Scenario: Local run without notifications

- **WHEN** the template has no `notifications` section
- **THEN** `ai-team run` MUST NOT start a notification worker

### Requirement: Durable, deduplicated notification delivery

Each channel and event pair MUST be claimed atomically in the shared delivery
log before sending and recorded durably after it. No process MUST notify a
channel again for an event already claimed or recorded, including after a
reconnect, restart, cursor rewind or when the web server and a run share one
database. A claim whose holder stopped before recording MUST expire after a
bounded lease. An acquired claim MUST carry an owner token; recording an
outcome and releasing a claim MUST succeed only while that token still holds
the pair. If the delivery record cannot be persisted, the event MUST remain
pending. Final delivery records MAY be pruned after a retention period; live
claims MUST NOT be pruned.

#### Scenario: Replay after reconnect

- **WHEN** the notifier re-reads events that it already delivered
- **THEN** no channel MUST receive a second notification for them

#### Scenario: Web server and local run share one database

- **WHEN** `ai-team run` and `ai-team web` both read an event subscribed by a channel
- **THEN** the channel MUST receive exactly one notification for it

#### Scenario: Two processes race for one delivery

- **WHEN** several workers on two connections to one database claim the same
  channel and event at once
- **THEN** exactly one of them MUST acquire the claim

### Requirement: Notification failures do not affect tasks

A delivery failure MUST be persisted as a warning `integration_error` with
`integration=notifier` and the channel name, at most once per channel and
event, MUST NOT change the task outcome and MUST NOT be retried after it is
persisted. Notifier `integration_error`
events MUST NOT be dispatched. If the failure warning cannot be persisted, the
event MUST remain pending.

#### Scenario: Channel is unavailable

- **WHEN** a subscribed webhook answers with an error status
- **THEN** SQLite MUST contain a warning `integration_error` naming the channel
- **AND** the task MUST keep its status
- **AND** the failure MUST NOT itself trigger a notification
