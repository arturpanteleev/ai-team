## Why

Текущий pipeline работает только через CLI. Нет возможности:

- Мониторить статус пайплайна в реальном времени через GUI
- Просматривать историю выполненных задач
- Анализировать артефакты через веб-интерфейс
- Контролировать процесс решения задач визуально

Для полноценного dev-tool необходим **web dashboard** — веб-интерфейс для мониторинга pipeline, аналогичный CICD системам (TeamCity, Jenkins). Backend обеспечивает API, хранение данных в SQLite и real-time обновления через WebSocket.

## What Changes

- Go HTTP-сервер с REST API и WebSocket
- SQLite для хранения истории pipeline runs
- API endpoints: `/api/pipelines`, `/api/pipelines/:id`, `/api/artifacts/:path`
- WebSocket hub для live updates
- Pipeline event emitter (stage_started, stage_completed, pipeline_completed)
- WebNotifier — реализация Notifier для записи в SQLite + push через WebSocket
- CLI команда: `ai-team web --port 8080`

## Capabilities

### New Capabilities

- `web-http-server`: HTTP-сервер с routing, middleware, static file serving
- `web-sqlite-store`: SQLite storage для pipeline runs, stages, artifacts
- `web-api`: REST API endpoints для запроса данных о pipeline
- `web-websocket`: WebSocket hub для real-time updates
- `web-pipeline-integration`: Интеграция pipeline с web backend (event emitter, WebNotifier)
- `web-cli-command`: CLI команда `ai-team web`

### Modified Capabilities

- `notifier-system`: Расширение Notifier интерфейса для поддержки stage_started events

## Impact

- `pkg/web/` — новый пакет (server, handlers, websocket, store)
- `go.mod` — добавление зависимостей (chi router, modernc.org/sqlite, gorilla/websocket)
- `cmd/ai-team/main.go` — добавление команды `web`
- `pkg/notifier/notifier.go` — возможно расширение интерфейса
- `pkg/pipeline/pipeline.go` — добавление event emitter
