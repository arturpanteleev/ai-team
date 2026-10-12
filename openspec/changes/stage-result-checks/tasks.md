# Stage result checks (B-40) — tasks

- [x] Declare `check` on template stages (`hard|agent`), hard `rules`, stage
      `required_sections`, and validation for both kinds.
- [x] Add `pkg/stagecheck`: statuses, rules, markdown report, verdict parsing
      (incl. `verdict` data-region handling), hard rules and agent-report merge.
- [x] Run the check after an attempt passes and before the transition confirms;
      skip replayed human attempts, config-skipped stages and `check.mode: grill`.
- [x] Persist the report in the task (`{artifacts}/{feature}/.stage-check/{stage}.md`,
      readable via `report.ReadStageCheckReport`) and in run evidence; handle
      stage re-runs under AUD-04 immutability.
- [x] Emit `check_started`, `check_passed`, `check_failed`, `check_error` events.
- [x] Raise `check_decision` approval (`kind=check_decision`,
      `trigger=stage_check`, actions `new_version/override/return`) on fail/error,
      save waiting and pause the run; `new_version` re-runs the stage on resume.
- [x] Add `kind=check_decision` support to the approval store (payload, actions,
      targets, `inferKind`) and cover it with tests.
- [x] Wire `pr_*` rules through `delivery.CheckPullRequest` with detected base branch.
- [x] Add stagecheck unit tests and pipeline integration tests (hard pass/fail,
      required sections, agent pass/fail/error, resume with `new_version`).
- [x] Project the SHA-verified grill `new_version` into the next stage input and
      recover it after a crash between transition and target-stage execution.
- [x] Run `GOTOOLCHAIN=go1.26.9 make verify` (build, vet, lint, coverage gate).
