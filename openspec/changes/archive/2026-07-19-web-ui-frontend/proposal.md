## Why

Backend web UI (Change 4) обеспечивает API и WebSocket, но нет клиентской части для визуализации. Необходим **React frontend** для:

- Dashboard — список запусков pipeline с фильтрами
- Pipeline Detail — этапы, статус, длительность, артефакты
- Artifact Viewer — просмотр markdown артефактов
- Live updates — WebSocket для обновления статуса в реальном времени

Frontend создаётся в `web/` как отдельный проект (React + TypeScript + Vite).

## What Changes

- React + TypeScript + Vite проект в `web/`
- Dashboard page — список pipeline runs
- Pipeline Detail page — этапы, статус, артефакты
- Artifact Viewer — markdown рендеринг
- WebSocket hook для live updates
- Build output в `web/dist/` для embed в Go бинарник

## Capabilities

### New Capabilities

- `web-dashboard`: Dashboard page — список запусков pipeline
- `web-pipeline-detail`: Pipeline detail page — этапы, статус, артефакты
- `web-artifact-viewer`: Artifact viewer — просмотр markdown артефактов
- `web-websocket-client`: WebSocket client hook для live updates

### Modified Capabilities

- (нет)

## Impact

- `web/` — новый каталог с React проектом
- `web/package.json` — зависимости (react, react-router-dom, vite, typescript)
- `web/src/` — исходный код frontend
- `web/dist/` — build output для Go embed
