# Task Timeline

## Why

The task page lists attempts and events but does not show where the task spent
its time. FR-22 asks for a timeline of stage lanes split by state, coloured by
executor, with return, override and skip marks; FR-23 asks for a stage card with
who did the work, versions, check rounds, tokens and an approximate cost based
on the subscription, where unknown values read "нет данных", not zero.

## What Changes

- Add a `Таймлайн задачи` section to `/tasks/{runID}`: one lane per pinned
  template stage, state segments derived only from recorded task events
  (waiting for executor, work, check, awaiting confirmation), executor colour
  plus border style, and return/override/skip marks. A stage without events
  has no bar.
- Add a stage card (executor and actors, interval and time per state, result,
  versions, check rounds, attempts with log links, tokens, subscription-share
  estimate, marks, links to the stage's events in the task feed).
- Add a table alternative, keyboard navigation between lanes, and patterns so
  meaning does not rely on colour alone.
- `GET /api/metrics/flow` and `/api/metrics/flow.csv` accept an optional
  `run_id` that restricts the existing aggregate to one task; without `from`
  and `to` the selection covers the whole task. Existing period requests are
  unchanged.
- Flow metrics take the executor kind of an attempt from its
  `attempt_finished` event, because the recorder stores the workflow execution
  state (not `human`/`agent`) in `stages.execution`.

## Impact

- Frontend: new `timeline.ts` pure builder, `TaskTimeline` component, task
  detail section, event anchors.
- Backend: additive `run_id` selection on the flow metrics routes; agent and
  human attempt counts, agent share and per-stage tokens now count real
  recorder data.
