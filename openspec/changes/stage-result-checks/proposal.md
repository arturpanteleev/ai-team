# Stage result checks (B-40)

## Proposal

**ID**: B-40  |  **Trace**: `TZ.md` — «Проверки результата этапа» (§6, §7, FR-11, FR-19/FR-20)

### Что меняется

A stage may declare an automatic check of its own result: hard rules computed
locally (`verdict_marker`, `required_sections`, `pr_*`) or a checker agent that
reads the stage result and emits a verdict report. The check runs right after
the attempt succeeds and before the transition is confirmed. A failed or failed
run check blocks the stage and raises a `check_decision` approval (`new_version`
/ `override` / `return`) bound to the failed stage; `override`/`return` routing
behavior is filled in by B-41; `new_version` re-runs the stage on resume. The
task keeps a readable copy of the check report at
`{artifacts}/{feature}/.stage-check/{stage}.md`.

### Почему

The current pipeline trusts the stage's self-assessed verdict. B-40 adds a
deterministic, configurable gate between the result and the confirmation of the
transition, so required sections, verdict markers and PR preconditions are
enforced by the controller rather than by agent goodwill — and a blocked stage
is surfaced to a responsible human as a persisted approval with the failing
report attached.

### Scope

1. Add declarative `check` on template stages (`hard|agent`), hard rules and
   a checker agent; validate the contracts in config validation.
2. Add `pkg/stagecheck` (kind/status constants, rule results, markdown report,
   verdict parsing) and the pipeline integration: events, report persistence,
   blocked transition, `check_decision` approval.
3. Add `check_started`, `check_passed`, `check_failed`, `check_error` events;
   keep the report visible in the task and in run evidence.

### Out of scope (later tasks)

- B-41: override/return routing semantics of `check_decision`.
- B-42: `check.mode: grill` for spec/product stages.
- B-44: project-level `project_checks` / `pr_checks_green`.
- B-71: UI/CLI wiring to answer check decisions.