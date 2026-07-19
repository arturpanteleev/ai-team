## 1. Setup проекта

- [x] 1.1 Инициализировать Vite проект в `web/` с React + TypeScript
- [x] 1.2 Добавить зависимости: react-router-dom, react-markdown
- [x] 1.3 Настроить vite.config.ts (dev proxy к :8080, build output)
- [x] 1.4 Создать базовую структуру `web/src/` (components, pages, hooks, styles)

## 2. Routing и layout

- [x] 2.1 Настроить React Router с маршрутами: `/`, `/pipelines/:id`, `/artifacts/*`
- [x] 2.2 Создать Layout компонент (sidebar/nav + main content)
- [x] 2.3 Настроить CSS Modules для стилей

## 3. Dashboard page

- [x] 3.1 Создать `Dashboard.tsx` — список pipeline runs
- [x] 3.2 Создать `PipelineCard.tsx` — карточка pipeline run (feature, status, duration)
- [x] 3.3 Реализовать фильтрацию по статусу
- [x] 3.4 Добавить обработку пустого состояния

## 4. Pipeline Detail page

- [x] 4.1 Создать `PipelineDetail.tsx` — детали pipeline run
- [x] 4.2 Создать `StageRow.tsx` — строка этапа (agent, status, duration)
- [x] 4.3 Создать `StatusBadge.tsx` — цветовой badge статуса
- [x] 4.4 Реализовать раскрытие этапа со списком артефактов
- [x] 4.5 Добавить live updates через WebSocket

## 5. Artifact Viewer

- [x] 5.1 Создать `ArtifactViewer.tsx` — просмотр markdown артефактов
- [x] 5.2 Реализовать markdown рендеринг через react-markdown
- [x] 5.3 Добавить переключение Raw/Rendered view
- [x] 5.4 Добавить навигацию назад

## 6. WebSocket hook

- [x] 6.1 Создать `useWebSocket.ts` — хук для WebSocket соединения
- [x] 6.2 Реализовать reconnect с exponential backoff
- [x] 6.3 Реализовать обработку events (stage_started, stage_completed, pipeline_completed)
- [x] 6.4 Интегрировать хук с Dashboard и PipelineDetail

## 7. Стили и UX

- [x] 7.1 Создать базовые стили (dark theme, typography, spacing)
- [x] 7.2 Добавить loading states
- [x] 7.3 Добавить error states
- [x] 7.4 Оптимизировать для desktop resolution (1280+)

## 8. Build и интеграция

- [x] 8.1 Настроить build скрипт: `npm run build` → `web/dist/`
- [x] 8.2 Проверить что build работает корректно
- [x] 8.3 Добавить инструкции по запуску dev server в README
