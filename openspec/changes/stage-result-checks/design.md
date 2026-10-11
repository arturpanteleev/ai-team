# Design: stage result checks

`config.TemplateStage.Check` declares the gate. `hard` requires `rules`
(`verdict_marker`, `required_sections` via the stage's `required_sections`,
`pr_exists`, `pr_open`, `pr_base_branch`); `agent` requires a checker agent from
the registry. Validation rejects unknown kinds, hard checks with agent/grill
fields, missing checker agents and `max_rounds < 0`. `check.mode: grill`
(spec/product checks, B-42) is skipped by B-40 mechanics entirely.

`pkg/stagecheck` knows nothing about the pipeline: `RunHard(ctx, HardInput)`
evaluates rules against the concatenated markdown content and the `pr_*` closure
produced by the pipeline; `RunAgent(stage, source, report)` merges hard rules and
the checker agent's markdown report; `ParseVerdict` accepts `Verdict:
PASSED|APPROVED|PASS` → passed and `FAILED|CHANGES_REQUESTED|REJECTED|FAIL` →
failed/error, ignoring markers inside `verdict` data regions. Reports render as
markdown; statuses aggregate so any failed/errored rule fails the check.

After an attempt passes and before the edge confirms, `runStageChecks` runs:
`check_started` → evaluate → persist the report → `check_passed` (allow the
transition) or `check_failed`/`check_error`. A fail or error raises
`check_decision`: an `approval.KindCheckDecision` pending approval
(`approval.StageCheckTrigger`, same-stage `new_version`/`override`/`return`
actions, payload with `report_path`/`report_sha256` and failed rules), the
`approval_requested` event, `saveWaiting(stage, id)`, and an
`ApprovalRequiredError` that pauses the run (existing waiting/resume machinery).
`new_version` re-runs the stage on resume by targeting the same stage.

The report is a controller file at `{artifacts}/{feature}/.stage-check/{stage}.md`
(readable via `report.ReadStageCheckReport`) plus a copy in the run evidence
`{runDir}/checks/{stage}.md`; on a stage re-run the controller copy is cleared
with the other ephemeral stage artifacts (AUD-04), and the evidence copy is
attempt-suffixed so immutable evidence is preserved.

Checks run only on fresh attempts: the hook is skipped for replayed human
attempts and config-skipped stages. `pr_*` rules reuse `delivery.CheckPullRequest`
against the detected base branch; detection failure is recorded as a rule error
and also blocks the transition (fail closed).