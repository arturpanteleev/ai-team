# B-86: auto task pickup with parallel and token budget limits

## Why

The `auto` run mode (B-37) still needs a human to create every task. Teams
want to keep a draft backlog of ideas and let the agent take the ones a human
has explicitly released, without letting unattended work run away: the
instance needs a parallel-task limit, and model spend needs a per-task and a
per-day token budget that stop work instead of silently continuing.

## Scope

- A draft idea backlog stored separately from tasks (`backlog_items` in the
  web database). New items are always `draft` and never start automatically.
- An explicit, human-set `ready` status is the only pickup marker. The picker
  in `ai-team web` takes one ready item per poll, in the order items were
  marked ready, and starts it in `auto` mode through the same admission path
  as `POST /api/runs`.
- `auto_pickup.max_parallel_tasks` (default 1) counts non-terminal lifecycle
  checkpoints, including tasks waiting for a human. When the limit or daily
  budget is reached the item stays queued and `auto_pickup_deferred` is
  recorded once per item, reason and UTC day.
- `budget.max_task_tokens` and `budget.max_daily_tokens` are checked before
  every model-invoking agent attempt. Usage is recomputed from attempt
  manifests on disk, never from process memory. Exceeding a budget appends
  `budget_exceeded` and pauses the run as resumable; only an explicit human
  resume continues it. A resume does not grant a new task budget by itself:
  a new allowance requires an explicit human grant with a reason
  (`budget_allowance_granted`); the daily budget is never granted.
- Enabling auto pickup requires both token budgets (fail-closed).

## Out of scope

- A browser UI for the backlog (the API is available).
- Auto pickup with the scheduler or disposable-worker engines; they do not
  support `auto` mode and the item is marked `failed`.

## Acceptance

- Unknown or draft markers are never picked; only exact `ready` is.
- Exactly at a limit the picker defers and the task stops; one below it
  proceeds.
- The day boundary is 00:00 UTC; a restart does not reset the daily counter.
- Checker agents (including grill rounds) are subject to the same budgets.
- A budget stop is visible as an event and never continues on its own.
