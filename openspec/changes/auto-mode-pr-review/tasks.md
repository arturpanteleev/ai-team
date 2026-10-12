# B-37 + B-44 tasks

- [x] Pin auto-mode executor/confirmation policy without mutating the active
      template; persist mode for resume and preserve human-owned checkpoints.
- [x] Record one report-bound agent retry and stop for human action after a
      repeated check failure.
- [x] Implement controller PR resolution, stable identity checks, `project_checks`
      through `ai-team gate`, and `pr_checks_green` through `gh pr checks`.
- [x] Supply the immutable PR diff and identity to the reviewer with its
      declared specification and technical-decision inputs.
- [x] Add local Git repository, stub-`gh`, replay and pipeline tests for the
      auto retry, reviewer inputs, and human/agent PR behavior.
- [x] Update CLI/config reference documentation.
- [x] Rebase on the latest `origin/master`, run pinned `GOTOOLCHAIN=go1.26.9
      make verify`, and record the final branch SHA.
