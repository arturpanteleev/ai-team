# Durable attempt usage and accurate run summaries

## Why

Usage currently lives only in process memory, so a pause and resume can lose
previous token counts. Resumed HTML reports also rebuild attempt rows without
their manifest-backed checks and artifacts, and report timestamps can mix
local zones. The run wall-time setting is applied per execution invocation,
while its old name and documentation suggest a task-wide duration.

## Scope

Persist attested input/output tokens in each immutable attempt manifest and
rebuild the task total from those manifests after resume. Keep totals unknown
when any invoked model runtime lacks complete usage. Use the original task
creation time for elapsed duration, use the human decision timestamp for
approval events, and expose `budget.max_execution_time` while retaining
`budget.max_wall_time` as a legacy alias.

Restore report counts and attempt pages from manifests, render report times in
UTC, and narrow `ai-team usage` to input/output/total tokens plus an optional
approximate subscription allocation. Do not show API price estimates; show
`нет данных` or `оценка недоступна` when required inputs are missing.

## Acceptance criteria

- Token totals equal the sum of per-attempt manifests after two pauses, without
  counting any attempt twice.
- Approval events use `Decision.DecidedAt`; total elapsed time starts at the
  original task creation while each resume receives a fresh execution budget.
- The legacy `max_wall_time` key still loads as an alias; both names together
  are rejected.
- Reports restore Checks/Inputs/Outputs from manifests and use one timezone.
- The usage CLI shows only token totals and an explicitly approximate
  subscription share, or the corresponding Russian unavailable text.
