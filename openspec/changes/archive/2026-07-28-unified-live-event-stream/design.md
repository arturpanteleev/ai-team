# Проектирование

## Контракт события

Wire event содержит `version`, глобальный `cursor`, `run_id`, run-local
`sequence`, `type`, optional `attempt_id`, `timestamp` и JSON-object `data`.
Версия начинается с `1`. Cursor равен SQLite `events.id`; он используется
только для доставки и не заменяет run-local sequence.

## Production bridge

CLI и web могут быть разными процессами, поэтому прямой вызов in-process hub
из recorder не решает задачу. Web-сервер запускает bounded SQLite tailer:
читает события `id > cursor` возрастающими страницами и передаёт их hub.
SQLite остаётся durable source для replay, а hub — live transport.

## Replay без окна потери

Регистрация клиента и replay выполняются внутри одного event-loop hub.
Пока replay provider читает durable события, live broadcasts ожидают в
очереди. После упорядоченного replay клиент добавляется в live-набор. Так между
replay и live mode нет окна потери.

## Frontend

Hook хранит максимальный обработанный cursor в `sessionStorage`, подключается к
`/ws?cursor=<cursor>`, игнорирует события с меньшим или равным cursor и
повторяет подключение с exponential backoff. UI обновляет projections на любом
релевантном lifecycle event. Polling выполняется редко как recovery fallback.

## Ограничения

В change не входят live log chunks и distributed broker. Их можно добавить
поверх того же versioned contract отдельными features.
