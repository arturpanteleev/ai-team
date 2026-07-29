# Версионированный граф workflow

## Why

Маршрут pipeline сейчас зашит в порядок массива и специальные ветки
`reviewer → coder`. Из-за этого UI не может объяснить следующий переход,
новые циклы требуют правок engine, а approval policy хранится на этапе,
хотя человек подтверждает конкретное ребро. Нужен декларативный граф, не
ослабляющий обязательные человеческие approvals.

## What Changes

- Schema v4 добавляет entry, outcome edges, terminal targets и `max_visits`.
- Approval policy с roles, quorum, actions и exact targets переносится на
  ребро перехода.
- Engine выбирает следующий узел по recorded outcome и сохраняет выбранное
  ребро в lifecycle/evidence.
- Любой цикл ограничивается `max_visits`; неоднозначные, недостижимые и
  неограниченные графы отклоняются до run.
- Config schemas 1–3 продолжают работать через детерминированную компиляцию
  линейного legacy pipeline.
- Immutable workflow snapshot и web detail показывают фактические nodes,
  edges, policies и текущий маршрут.

## Impact

- Новый capability `workflow-graph`.
- Изменяются `role-config`, `workflow-transitions`, `agent-orchestration`,
  `pipeline-gates`, `run-evidence`, `web-api` и `web-pipeline-detail`.
- Затрагиваются config loader, workflow compiler, Pipeline и frontend.
