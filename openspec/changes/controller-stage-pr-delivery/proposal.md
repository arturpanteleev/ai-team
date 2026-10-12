# Controller-owned PR delivery before review

## Why

The default implementation stage keeps hard `pr_exists`, `pr_open`, and
`pr_base_branch` checks. A terminal-only PR delivery cannot satisfy those
checks before `code_review` and QA run, so the controller needs a stage-scoped,
replayable delivery result while preserving immutable attempt evidence and
separate whole-run attestation.

## What Changes

- Add a Unix controller API operation that validates the pinned attempt
  manifest, canonical approved plan, runtime identity, and approval before
  executing PR delivery.
- Persist an immutable controller receipt keyed by run, attempt manifest,
  approved plan, and runtime identity; replay repairs the PR result projection
  from that receipt and reuses the delivery executor's remote recovery.
- Publish the controller-created PR URL as a stage result and an immutable
  input for later review and QA stages before the existing hard PR checks run.
- Keep terminal run attestation and its record separate from the reviewed PR
  commit; never rewrite the published attempt manifest.

## Capabilities

- `delivery-executor`
- `run-evidence`

## Impact

Changes the pipeline ordering for configured PR-result stages whose hard check
requires `pr_exists`; other delivery stages retain terminal delivery behavior.
Adds a controller API request/receipt contract and updates architecture docs.
