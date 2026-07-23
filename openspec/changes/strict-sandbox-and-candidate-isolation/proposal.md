## Why

Two independent audits now agree on the same conclusion from different angles.
The original `AUDIT.md` (2026-07-20, written alongside control-plane-hardening)
named this gap directly as P0 blockers C-01/C-02/C-03 and tracked it as tasks
9.1-9.8 in that change — which shipped its other 34 tasks but never reached
this section. A second, fully independent audit (`INDEPENDENT_AUDIT_2026-07-23.md`)
re-derived the same gap from scratch via live functional testing, mutation
testing and code review, and added concrete new evidence: the OpenCode
permission/sandbox mechanism (`OpenCodeIsolationEnvironment`) has zero test
coverage proving a real `opencode` binary honors it, and workspace-hashing
cost was measured (not estimated) at ~14-20 full-tree passes per run on a
9,247-file repo.

Today, `ai-team` mutates the live target workspace directly. The controller
detects disallowed mutations *after* the fact by comparing filesystem
snapshots; it cannot prevent an agent or a compromised check from reading a
secret, reaching the network, or corrupting workspace state before that
comparison runs. There is no immutable "candidate" that review, tests,
verification and delivery all provably refer to — each stage re-derives trust
from a live, mutable directory. This is why README and AUDIT.md both
correctly, honestly scope the product as `trusted-local` only.

This proposal captures the full shape of the fix so it can be reviewed and
scheduled deliberately, rather than rushed. **This proposal intentionally
contains no implementation** — see `tasks.md` for why, and see the companion
change branches for the parts of tonight's backlog that *were* implemented
(they close narrower, independently-testable gaps and do not require this
architecture).

## What Changes

- Introduce a `SandboxBackend` interface with explicit capabilities (deny
  network by default with explicit grants, filesystem allow-list, environment
  allow-list, CPU/memory/pid/disk/time limits) and at least one real Linux
  backend (container or bubblewrap-equivalent). Strict profile fails closed if
  no backend is available; trusted-local profile may run without one but must
  say so in evidence.
- Every run creates an isolated candidate root (worktree or copy-on-write
  checkout) from a fixed base commit before any agent or check executes. All
  source-mutating stages and all deterministic checks run against the
  candidate, never the live target. A separate, explicit promotion step is the
  only thing allowed to update the live target, and only after delivery
  succeeds.
- A `candidate.json` binds one candidate tree/commit hash to: the diff that
  produced it, every executed check (adapter, tool identity, output digest),
  every stage decision (review/test/verification), and the delivery plan. Any
  change to the candidate invalidates every downstream approval already
  recorded against it.
- `--approve-gates` is replaced by exact per-checkpoint approval
  (`--approve-gate <gate-id>:<subject-sha>`), so blanket approval of a run can
  no longer stand in for approval of one specific gate's actual subject.
- `opencode` binary identity (path, hash, resolved version) is captured once
  per run and pinned for the duration of that run; attempts fail closed if the
  resolved binary changes mid-run.
- The event ledger gains a terminal root hash / external anchor so tampering
  by the file owner is detectable, not just internally self-consistent.

## Capabilities

### New Capabilities

- `sandbox-backend`: pluggable OS-level execution sandbox with explicit
  network/filesystem/environment/resource policy; strict profile fails closed
  without one.
- `candidate-isolation`: isolated immutable candidate root per run; live
  target is never mutated by agents or checks; promotion is a separate,
  explicit, post-delivery step.

### Modified Capabilities

- `control-plane-safety`: verdicts, checks and delivery approval bind to one
  `candidate.json` identity instead of a live-workspace digest comparison;
  `--approve-gates` blanket approval is replaced by exact per-gate,
  per-subject approval.
- `delivery-executor`: delivery plan references the candidate tree/commit
  hash directly rather than re-deriving an equivalent live-workspace digest.
- `run-evidence`: attempt manifests gain candidate identity fields; the event
  ledger gains a terminal root hash / external anchor.
- `deterministic-checks`: checks execute against the candidate root through
  the sandbox backend, not the live target with host-user privileges.

## Impact

- New packages: `pkg/sandbox` (backend interface + implementations),
  `pkg/candidate` (worktree/copy-on-write lifecycle, `candidate.json`).
- `pkg/pipeline`: stage execution re-pointed at the candidate root; gate
  approval changes from blanket to exact-subject.
- `pkg/checks`: check execution routed through `SandboxBackend` instead of
  direct `exec.Command` on the live workspace.
- `pkg/delivery`: plan construction reads from candidate identity; live target
  promotion becomes an explicit final step instead of being implicit in "the
  workspace the agent already wrote to."
- `pkg/evidence`: manifest schema version bump (candidate fields); ledger gains
  a terminal anchor.
- `pkg/runtime`: capture and pin resolved `opencode` binary identity per run.
- CLI: `--approve-gates` flag replaced by `--approve-gate <id>:<sha>` (breaking
  change to the run command's non-interactive contract — needs a migration
  note in README and CHANGELOG).
- This is the largest single architectural change proposed against this
  codebase to date and should not be scheduled alongside other in-flight
  structural work.
