# Design: controller stage PR delivery

## Goals

- Make the default implementation stage's existing hard PR checks and later
  review/QA stages executable without weakening those checks.
- Keep delivery authority in the controller and bind every effect to pinned
  evidence and exact approval.
- Recover safely if remote PR creation succeeds before local receipt or
  projection persistence.
- Preserve attempt manifests and keep the later terminal attestation separate.

## Non-goals

- Move PR creation earlier for stages without a hard `pr_exists` result rule.
- Put worker-provided URLs or worker-local receipt files in the authority path.
- Add the terminal attestation trailer to an already reviewed commit.

## Decisions

The worker requests `delivery.stage.execute` with only run ID, attempt ID,
attempt-manifest SHA-256, approved plan hash, and runtime identity. The
controller re-derives the pinned stage/config, strict event replay, prepared
plan, approval decision, and workspace digest from controller-owned stores.
It executes delivery with only controller-derived run/runtime trailers.

After the executor returns a verified PR, the controller durably writes a
self-digesting receipt keyed by the identity tuple and projects the URL to the
project artifact tree and candidate artifact tree. A retry first reads that
receipt and repairs both projections. If the remote PR exists but the receipt
does not, the delivery executor's saved state plus `gh pr view` reconciles the
same PR without another commit or PR.

The pipeline invokes this operation only when a configured agent PR result
stage declares the hard `pr_exists` rule. It then runs the unchanged hard PR
checks, and forwards the controller-owned `pull-request` artifact as an
immutable input to later graph stages. Resume at review or QA replays the
receipt before constructing that stage's inputs.

A non-interactive run stops in `awaiting_plan` inside the agent stage, after
the coder, controller checks and plan preparation. On resume the stage starts a
controller-only continuation attempt: the agent runtime is not invoked again,
the guard requires an unchanged candidate workspace, controller checks run
again, and the plan is rebuilt from the prepared state and authorized for the
exact hash. Re-running the model would produce a new plan that the human never
saw, so it is never part of resuming an approval wait.

At terminal finalize the run-level attestation is written as usual. The
terminal record references the stage receipt, attempt manifest digest,
attestation digest, and runtime identity. The commit retains only the run and
runtime trailers used at stage delivery; the later attestation digest is not
inserted into or used to rewrite that commit.

## Risks and mitigations

- A receipt can be durable while a projection is missing or partially written.
  Atomic projection replacement and receipt-first retry repair the read model.
- Remote PR creation can succeed before local state is saved. The delivery
  executor retries by finding and verifying the existing PR for the approved
  branch and commit.
- A worker could submit a forged request or URL. The API accepts identities
  only over the scoped Unix controller transport and derives all authority and
  the URL itself.

## Migration

No attempt manifest migration is needed. Terminal records gain optional stage
receipt references; legacy records without those references retain their
existing validation path.
