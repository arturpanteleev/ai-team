## Context

Текущий pipeline работает через CLI. Все данные хранятся на диске в `.ai-team/artifacts/`. Нет HTTP-сервера, API, WebSocket, или базы данных.

Существующие компоненты для переиспользования:
- `Notifier` + `NotifierChain` — точка расширения для WebSocket
- `StageResult` — центральное событие с данными об этапе
- `runtime.Artifact` — метаданные артефактов
- `report/` — структуры данных для отчётов (можно конвертировать в JSON)
- `pipeline.Pipeline.Run()` — ядро оркестрации

go.mod содержит только `gopkg.in/yaml.v3`.

## Goals / Non-Goals

**Goals:**
- HTTP-сервер с REST API для запроса данных о pipeline
- SQLite для хранения истории pipeline runs (modernc.org/sqlite — чистый Go, без CGO)
- WebSocket для live updates (stage_started, stage_completed, pipeline_completed)
- WebNotifier — запись в SQLite + push через WebSocket
- CLI команда: `ai-team web --port 8080`
- Статический файл serving для frontend (build из web/)

**Non-Goals:**
- Frontend (отдельный Change 5)
- Аутентификация (localhost only)
- Запуск задач через web (только мониторинг)
- Multi-user support

## Decisions

### Decision 1: Router — chi

**Выбор:** `github.com/go-chi/chi/v5` для routing.

**Альтернативы:**
- `net/http` стандартный — нет middleware, mux routing
- `gorilla/mux` — не обновляется
- `gin-gonic/gin` — тяжелее, другая философия

**Rationale:** Chi lightweight, stdlib-compatible, активно поддерживается. Идеален для REST API.

### Decision 2: SQLite — modernc.org/sqlite

**Выбор:** `modernc.org/sqlite` — чистый Go, без CGO.

**Альтернативы:**
- `mattn/go-sqlite3` — требует CGO, проблемы с кросс-компиляцией
- `crawshaw.io/sqlite` — CGO-based
- Файловая система — нет query capabilities

**Rationale:** modernc.org/sqlite не требует CGO, работает на всех платформах, достаточно быстрый для我们的 use case.

### Decision 3: WebSocket — gorilla/websocket

**Выбор:** `github.com/gorilla/websocket` для WebSocket.

**Альтернативы:**
- `nhooyr.io/websocket` — менее популярен
- SSE (Server-Sent Events) — не поддерживает bidirectional

**Rationale:** gorilla/websocket — de facto standard для Go WebSocket.

### Decision 4: Архитектура хранения

**Выбор:** SQLite для метаданных + filesystem для артефактов.

**Альтернативы:**
- Только SQLite — артефакты большие, накладно
- Только filesystem — нет query capabilities

**Rationale:** SQLite хранит runs, stages, events. Артефакты остаются на диске и serve через static file serving.

### Decision 5: Event emitter в pipeline

**Выбор:** Добавить callback/event в pipeline для уведомления web backend.

**Альтернативы:**
- Polling filesystem — медленно, ненадёжно
- Modify Notifier для stage_started — нарушает текущий контракт

**Rationale:** Event emitter в pipeline позволяет web notifier получать все события (start, complete, block) без модификации Notifier интерфейса.

## Risks / Trade-offs

- **[Risk]** modernc.org/sqlite может быть медленнее CGO версии → **Mitigation:** Для我们的 use case (dev tool, ít concurrent users) достаточно
- **[Risk]** gorilla/websocket deprecated → **Mitigation:** nhooyr.io/websocket как fallback
- **[Risk]** Дополнительные зависимости увеличивают бинарник → **Mitigation:** Accept trade-off для функциональности
