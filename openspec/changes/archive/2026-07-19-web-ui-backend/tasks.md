## 1. Зависимости и setup

- [x] 1.1 Добавить зависимости в go.mod: `github.com/go-chi/chi/v5`, `modernc.org/sqlite`, `github.com/gorilla/websocket`
- [x] 1.2 Создать структуру пакета `pkg/web/`

## 2. SQLite store

- [x] 2.1 Создать `pkg/web/store/sqlite.go` — подключение к SQLite, auto-migration
- [x] 2.2 Создать `pkg/web/store/models.go` — модели: PipelineRun, Stage
- [x] 2.3 Реализовать методы: CreatePipelineRun, UpdatePipelineRun, CreateStage, UpdateStage, GetPipelineRuns, GetPipelineRunByID, GetStagesByPipelineRunID

## 3. HTTP server

- [x] 3.1 Создать `pkg/web/server.go` — HTTP-сервер с chi router
- [x] 3.2 Настроить CORS middleware для dev
- [x] 3.3 Настроить static file serving для frontend (web/dist)

## 4. API handlers

- [x] 4.1 Создать `pkg/web/handlers.go` — handlers для API endpoints
- [x] 4.2 Реализовать `GET /api/pipelines` — список pipeline runs
- [x] 4.3 Реализовать `GET /api/pipelines/:id` — детали pipeline run со stages
- [x] 4.4 Реализовать `GET /api/pipelines/:id/artifacts` — список артефактов
- [x] 4.5 Реализовать `GET /api/artifacts/:path` — содержимое артефакта

## 5. WebSocket

- [x] 5.1 Создать `pkg/web/websocket.go` — WebSocket hub с broadcast
- [x] 5.2 Реализовать подключение/отключение клиентов
- [x] 5.3 Реализовать broadcast events (stage_started, stage_completed, pipeline_completed)

## 6. Pipeline integration

- [x] 6.1 Создать `pkg/web/notifier.go` — WebNotifier (запись в SQLite + push WebSocket)
- [x] 6.2 Добавить event emitter в `pkg/pipeline/pipeline.go` — stage_started callback
- [x] 6.3 Интегрировать WebNotifier в pipeline через NotifierChain

## 7. CLI команда

- [x] 7.1 Добавить команду `web` в `cmd/ai-team/main.go` с флагом `--port`
- [x] 7.2 Реализовать запуск HTTP-сервера при `ai-team web`
- [x] 7.3 Добавить graceful shutdown при Ctrl+C

## 8. Тесты

- [x] 8.1 Написать unit-тесты для store (CRUD операции)
- [x] 8.2 Написать unit-тесты для API handlers
- [x] 8.3 Написать unit-тесты для WebSocket hub
- [x] 8.4 Запустить `make build` и `make test`
