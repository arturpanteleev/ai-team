# Task detail and stage panel (B-62) — tasks

- [x] Return task text, mode, external links, and pinned template from task
      detail API; include stage link kind and skippable metadata.
- [x] Add stable `/tasks/{runID}` page and route board/new-task navigation to
      it.
- [x] Render lifecycle-gated actions, result revisions/comparison, checker
      evidence, logs, grill Q&A/history, and task events.
- [x] Add component regressions for action visibility, result submission,
      checker report, and grill history.
- [x] Add browser E2E traversal for manual and automatic modes.
- [x] Document the task detail resource and route.
- [x] Run focused frontend/API tests and pinned `GOTOOLCHAIN=go1.26.9 make verify`.
