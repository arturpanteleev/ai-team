## Context

`control-plane-hardening` (archived 2026-07-23) delivered fail-closed verdicts,
immutable run/attempt evidence, a hash-chained event log, and a delivery
executor that will not commit without an explicit exact-plan-hash approval.
All of that is real and independently verified (see
`INDEPENDENT_AUDIT_2026-07-23.md`, section 15.8). What it did not change is
*where* agents and checks run: directly in the live target workspace, as the
host OS user, with the full parent environment. The controller's guarantees
are therefore all *detective* (compare a snapshot before/after, reject on
mismatch) rather than *preventive* (make the disallowed action impossible).
Two independent audits converged on this as the single largest remaining gap
between "trusted-local prototype" and anything that could honestly claim to
handle untrusted code or sit next to real secrets.

## Goals / Non-Goals

**Goals:**
- Make agent and check execution provably confined: no ambient access to the
  live target, host secrets, or network unless explicitly granted.
- Make "what was reviewed / tested / verified" and "what gets delivered" the
  same, provably identical artifact — not two things a snapshot digest claims
  are equal.
- Keep the system single-binary, local-first (per control-plane-hardening's
  own non-goals) — no required external orchestrator or cloud dependency.
- Preserve today's `trusted-local` profile as a still-supported, faster path
  for users who accept the current, documented risk; `strict` is additive.

**Non-Goals:**
- Full multi-tenant/cloud sandboxing. This is a local developer tool; the
  threat model is "one compromised or careless agent/check on one machine,"
  not "hostile multi-user isolation."
- Windows sandbox backend in this phase. `sandbox-backend` ships Linux first
  (container or bubblewrap-equivalent) and a documented no-op/deny-by-default
  fallback elsewhere; strict profile requires the real backend and fails
  closed if it's unavailable, including on macOS/Windows until a backend
  exists for them.
- Solving H-03 (machine-readable acceptance-criteria traceability) or H-07
  (non-Go typed test adapters) — related, tracked separately, not required
  for candidate isolation itself to be sound.

## Decisions

### D1. Candidate lifecycle owns a worktree, not a copy

Use `git worktree` against the run's base commit rather than a naive
directory copy: it is cheap, reuses the object store, and gives the
controller a real commit/tree hash for `candidate.json` for free. Untracked
files present in the live target at run start are classified explicitly
(user-owned, not copied into the candidate) rather than silently included —
this preserves control-plane-hardening's existing "clean baseline" precondition
instead of loosening it.

### D2. Sandbox is a capability interface, not a single implementation

`SandboxBackend` exposes `Run(ctx, spec) (Result, error)` where `spec`
declares filesystem mounts (candidate root read-write, everything else
read-only or absent), network policy (deny by default, explicit host:port
grants), environment (explicit allow-list, not today's deny-list — see the
narrower, already-implemented env allow-list fix in
`pkg/runtime/agentcli.go` for the immediate version of this same idea), and
resource limits. `checks.Runner` and `runtime.AgentCLI` both go through this
interface instead of calling `exec.Command` directly against the live
filesystem. Strict profile fails closed at startup if no backend probes
successfully; trusted-local profile logs a prominent warning and proceeds
without one, exactly as today.

### D3. `candidate.json` is the one identity everything binds to

Base tree hash, candidate tree/commit hash, patch digest, changed
paths/modes/blob SHAs, every executed check (adapter, tool fingerprint,
output digest), and every stage decision (review/test/verification) reference
this one hash. The delivery plan must reference the same hash. Any mutation
to the candidate after a decision was recorded invalidates that decision —
this is a strengthening of the existing loopback-invalidation mechanism
(`pkg/workflow`), not a new mechanism from scratch.

### D4. Exact per-gate approval replaces blanket `--approve-gates`

Today, `--approve-gates` approves every future checkpoint in one run,
regardless of what each checkpoint's actual subject turns out to be — the
subject SHA is recorded in evidence, but the CLI flag itself isn't scoped to
it (independent audit Finding, H-02 in AUDIT.md). Replace it with
`--approve-gate <gate-id>:<subject-sha>`, one per checkpoint, checked against
the actual subject at resume time. This is a CLI-contract breaking change and
needs a README/CHANGELOG migration note.

### D5. Promotion is a separate, explicit, final step

The live target is never written to until delivery succeeds against the
candidate. "Promotion" (fast-forwarding the live target to the candidate's
result, e.g. so the user's checked-out branch reflects what was delivered)
is its own function with its own precondition checks — it does not happen
implicitly as a side effect of agents having already written into the live
directory, because they no longer do.

## Risks / Trade-offs

- **Performance**: worktree creation + sandboxed execution adds latency per
  stage. Mitigated by D1 (worktree, not copy) and by the fact that the
  existing workspace-hashing cost (independently measured at ~1.6s cold per
  full-tree pass, done 14-20+ times per run today) is *removed* by this
  change, not added to — snapshot-diffing goes away once the candidate is
  the only thing being mutated.
- **Complexity**: this is the single largest change proposed against this
  codebase. It touches pipeline, checks, delivery and evidence simultaneously.
  Recommend implementing behind a config flag (`candidate_isolation: true`)
  with both code paths coexisting for at least one release, rather than a
  flag-day cutover.
- **macOS sandbox backend**: no mature bubblewrap-equivalent exists for macOS
  the way it does for Linux containers. Strict profile on macOS may need to
  accept a weaker backend (e.g. `sandbox-exec`, deprecated but still
  functional) or explicitly declare macOS strict-profile as unsupported for
  now — this needs an explicit decision before implementation starts, not
  during it.
- **Breaking CLI change** (D4): existing scripts/CI using `--approve-gates`
  break. Needs a deprecation window (accept both, warn on the old flag, one
  release) rather than a hard cutover.
