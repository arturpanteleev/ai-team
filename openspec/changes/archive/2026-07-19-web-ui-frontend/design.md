## Context

Backend (Change 4) предоставляет:
- REST API: `/api/pipelines`, `/api/pipelines/:id`, `/api/pipelines/:id/artifacts`, `/api/artifacts/:path`
- WebSocket: `/ws` с events (stage_started, stage_completed, pipeline_completed)
- Static file serving из `web/dist/`

Frontend должен быть создан в `web/` как отдельный React проект.

## Goals / Non-Goals

**Goals:**
- React + TypeScript + Vite проект в `web/`
- Dashboard page — список pipeline runs с фильтрами по статусу
- Pipeline Detail page — этапы, статус, длительность, артефакты
- Artifact Viewer — markdown рендеринг артефактов
- WebSocket hook для live updates
- Responsive design (desktop-first)
- Dark theme (соответствует terminal aesthetic)

**Non-Goals:**
- Mobile-first responsive (dev tool, desktop only)
- SSR/SSG
- State management library (use React hooks + context)
- Тестирование frontend (отдельный scope)

## Decisions

### Decision 1: State management — React hooks + context

**Выбор:** Хуки + Context API, без внешних библиотек.

**Альтернативы:**
- Redux/Zustand/Jotai — избыточно для dashboard приложения
- React Query — хорош для кэширования, но добавляет зависимость

**Rationale:** Приложение простое (3 страницы), данные приходят через API/WebSocket. Хуков достаточно.

### Decision 2: Router — React Router v6

**Выбор:** `react-router-dom` v6.

**Альтернативы:**
- TanStack Router — новый, менее зрелый
- Wouter — минималистичный, но менее популярен

**Rationale:** React Router — de facto standard, стабильный, хорошо документирован.

### Decision 3: Markdown rendering — react-markdown

**Выбор:** `react-markdown` для рендеринга markdown артефактов.

**Альтернативы:**
- marked — менее безопасный
- Простой dangerouslySetInnerHTML — XSS risk

**Rationale:** react-markdown безопасный, поддерживает plugins, хорошо поддерживается.

### Decision 4: Стилизация — CSS Modules

**Выбор:** CSS Modules (встроено в Vite/CRA).

**Альтернативы:**
- Tailwind CSS — добавляет зависимость, конфигурацию
- Styled Components — runtime overhead
- SCSS — дополнительная зависимость

**Rationale:** CSS Modules встроены в Vite, нет дополнительных зависимостей, изоляция стилей.

## Risks / Trade-offs

- **[Risk]** WebSocket может отключаться → **Mitigation:** Автоматический reconnect с exponential backoff
- **[Risk]** Нет offline support → **Mitigation:** Dev tool, не критично
- **[Risk]** Build output увеличивает Go бинарник → **Mitigation:** Optimized build, gzip
