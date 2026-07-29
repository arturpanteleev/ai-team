# Единый живой поток событий

## Зачем

Pipeline уже сохраняет lifecycle events в SQLite, а web-сервер поднимает
WebSocket hub, но production-код не связывает эти части. В результате UI
получает актуальное состояние только polling-запросами, а WebSocket существует
лишь формально.

## Что меняется

- Вводится versioned wire contract для run events.
- SQLite event id становится глобальным монотонным cursor, а `sequence`
  сохраняет порядок внутри run.
- Web-сервер читает новые SQLite events и публикует их в WebSocket hub.
- Подключение с `?cursor=N` получает durable replay до перехода в live mode.
- Frontend сохраняет последний cursor, дедуплицирует события и передаёт его
  после reconnect.
- Редкий polling остаётся safety fallback, но перестаёт быть основным live
  transport.

## Затронутые области

- `pkg/web/store`, `pkg/web`, `web/src`.
- Capabilities: новый `run-event-stream`; изменённые `web-websocket`,
  `web-websocket-client`, `web-sqlite-store`, `web-pipeline-detail`,
  `run-evidence`.
