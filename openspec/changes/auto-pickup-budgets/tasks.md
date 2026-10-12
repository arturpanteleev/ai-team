# B-86 tasks

- [x] Add `budget.max_task_tokens`, `budget.max_daily_tokens` and the
      `auto_pickup` section with fail-closed validation.
- [x] Recompute token usage from attempt manifests for the UTC day and enforce
      task and daily budgets before model-invoking attempts.
- [x] Pause over-budget runs as resumable with a `budget_exceeded` event.
- [x] Store the draft idea backlog separately with human-only `ready`
      transitions and an atomic picker claim.
- [x] Run the picker in `ai-team web` with the parallel-task limit, daily
      budget and deferred/started/failed events; reconcile claims on restart.
- [x] Table tests for limit boundaries, UTC day boundary, unknown markers,
      budget stops and restart persistence with an injected clock.
- [x] Update configuration and Web API reference documentation.
