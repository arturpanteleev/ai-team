# B-37 + B-44: auto runs with PR-bound review checks

## Why

The graph runner already supports an `auto` run mode, but that mode needs an
explicit, durable policy for which stages remain human-owned and how a failed
stage check is retried. PR reviewers also need the actual GitHub diff and PR
identity, while PR stages need project gate and GitHub CI checks that work for
both human and agent results.

## Scope

- Pin an auto-mode copy of the template for each run. Stages with an agent run
  automatically; stages without an agent remain typed human inputs.
- Keep question answering, agreed specification and delivery-plan approval
  human-owned. A failed hard/agent stage check gives an agent stage one retry
  with an immutable report-bound feedback input, then pauses for human action.
- Run `project_checks` with `ai-team gate` against the exact PR head and base in
  an isolated temporary repository that does not share the controller's
  gitdir. Use gate policy from the pinned PR base so a
  candidate cannot weaken its own checks. Run `pr_checks_green` with
  `gh pr checks` and reject a moving PR identity.
- Give a reviewer the stable `gh pr diff` and pinned PR identity alongside its
  declared specification and technical-decision inputs whenever a successful
  preceding PR result is available.
- Preserve deferred delivery and controller-only Git mutation. This change
  does not move PR creation earlier in the graph.

## Acceptance

- An auto run retries a failed agent stage check exactly once, includes the
  failed report in the retry input, and stops for a human decision if the retry
  also fails.
- An auto run leaves agentless stages and the delivery-plan decision human.
- Both human and agent PR results use the same project and GitHub checks.
- The reviewer receives a stable PR diff, PR/base/head identity, specifications
  and technical decision when a PR URL is present.
- PR checks do not move the controller checkout or create commits or pushes.
