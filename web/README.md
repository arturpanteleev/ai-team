# ai-team web frontend

React + TypeScript + Vite SPA для дашборда ai-team. Продакшн-сборка
встраивается в Go-бинарник через `go:embed` (`web/dist`); `ai-team web
--dist <path>` позволяет подменить её локальной сборкой.

## Разработка

```bash
npm ci
npm run dev        # dev-сервер; API ожидается на том же origin (proxy)
npm run lint
npm test           # vitest + testing-library
npm run build      # прод-сборка в dist/
```

`make verify` дополнительно проверяет, что `web/dist` в репозитории
соответствует свежей сборке — после изменений фронта выполните
`npm run build` и закоммитьте dist.

## Архитектура

- `src/api.ts` — типизированный клиент REST API (`/api/pipelines`, `/api/runs`,
  approvals, artifacts).
- `src/hooks/useWebSocket.ts` — живой event stream `/ws` с cursor/replay;
  stream identity сбрасывает cursor при пересоздании SQLite store, polling
  остаётся recovery fallback.
- `src/pages/Dashboard.tsx` — список runs; `PipelineDetail.tsx` — детали run,
  pending approvals (subject hash, actions), checks/mutations/delivery.
- Аутентификация: в cloud-режиме Bearer token обменивается на HttpOnly
  session cookie; все write-запросы требуют `X-CSRF-Token` (см. `src/api.ts`).

События, которые эмитит бэкенд: `run_started`, `run_resumed`, `run_paused`,
`run_canceled`, `attempt_started`, `attempt_finished`, `attempts_invalidated`,
`approval_requested`, `approval_decided`, `transition_selected`,
`run_finished` (контракт — `src/types/index.ts`, источник —
`pkg/web/recorder.go`).
