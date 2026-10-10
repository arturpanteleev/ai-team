# Stage executor switching and agent actions (B-32)

## Proposal

**ID**: B-32  |  **Trace**: `TZ.md` — «Исполнитель этапа: смена и «Сделай»»

### Что меняется

Allow a ready stage to switch between its pinned human and agent executor.
Human input approvals for agent-capable stages expose «Сделай» and
«Доработай агентом»; the refinement action binds the current result text as an
immutable agent input. Human edits of a previous agent result retain the
attempt identity in evidence.

### Почему

The pinned template remains the default, while a responsible user can choose
who executes a specific ready visit. Approval identity prevents a choice from
leaking into a later loop or return to the same stage.

### Scope

1. Persist executor overrides in lifecycle state, bound to a ready approval
   visit, and expose a secured web API for changing the current ready stage.
2. Dispatch the selected executor in the pipeline; add `run_agent` and
   `refine_agent` actions to human input approvals when the pinned stage has
   an agent. Snapshot the refinement text as `current-result`.
3. Record `executor_changed`, `agent_started`, `agent_finished`, and
   `human_result_edited_agent` in run evidence, with the edited agent attempt
   identity in the human attempt manifest.
4. Keep the CLI command aligned with the task-stage action interface introduced
   by B-71: `ai-team task stage <stage-id> run-agent`. The current branch
   exposes the lifecycle/pipeline behavior and web API; CLI command wiring is
   an integration point for that interface.

### Acceptance criteria

- A ready stage can change executor through the secured API; a stale approval
  cannot apply the change to a later visit.
- A human input approval on a stage with an agent offers both agent actions.
- `run_agent` executes the configured stage agent; `refine_agent` passes the
  submitted current result as an input snapshot.
- Human edits of an agent result identify the source attempt in evidence.
- Events survive evidence replay and worker API event validation.
