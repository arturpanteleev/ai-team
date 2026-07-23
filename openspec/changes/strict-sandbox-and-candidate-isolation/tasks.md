> **Status: proposal only.** No task below has been started. This change is
> intentionally scoped to proposal + design + specs tonight, not
> implementation — it is the largest and most safety-critical architectural
> change proposed against this codebase, and rushing it unreviewed would
> undermine the exact guarantees it's meant to add. Needs explicit sign-off
> on the design (particularly D2's sandbox backend choice per-OS, and D4's
> breaking CLI change) before task 1.1 starts.

## 1. Sandbox backend

- [ ] 1.1 Define `SandboxBackend` interface (filesystem/network/env/resource policy)
- [ ] 1.2 Implement Linux backend (container or bubblewrap-equivalent)
- [ ] 1.3 Decide and implement (or explicitly defer) a macOS strategy
- [ ] 1.4 Wire `pkg/checks` to execute through the backend
- [ ] 1.5 Wire `pkg/runtime.AgentCLI` to execute through the backend
- [ ] 1.6 Strict profile fails closed at startup if no backend probes successfully
- [ ] 1.7 Record backend identity/policy/limits in evidence per invocation

## 2. Candidate isolation

- [ ] 2.1 Implement candidate worktree lifecycle (create from base commit, classify untracked files)
- [ ] 2.2 Re-point source-mutating stages and checks at the candidate root
- [ ] 2.3 Implement `candidate.json` (tree/commit hash, patch digest, changed paths/modes/blobs, check evidence, decisions)
- [ ] 2.4 Invalidate recorded decisions when candidate hash changes
- [ ] 2.5 Implement explicit promotion step, run only after delivery succeeds
- [ ] 2.6 Remove the now-redundant live-workspace snapshot hashing (performance win, see design.md Risks)

## 3. Delivery and approval

- [ ] 3.1 Delivery plan construction reads from candidate identity, not live-workspace digest
- [ ] 3.2 Reject delivery if plan candidate hash != verified candidate hash
- [ ] 3.3 Replace `--approve-gates` with `--approve-gate <gate-id>:<subject-sha>`
- [ ] 3.4 Ship one release accepting both flags with a deprecation warning before removing the old one

## 4. Evidence and identity

- [ ] 4.1 Add candidate identity fields to attempt/artifact manifests (schema version bump)
- [ ] 4.2 Add terminal ledger root hash / external anchor
- [ ] 4.3 Capture and pin resolved `opencode` binary identity per run; fail closed on mid-run change

## 5. Verification

- [ ] 5.1 Fixture proving a hostile check cannot read a host secret or reach the network from inside the candidate/sandbox
- [ ] 5.2 Fixture proving live target is untouched until promotion, including on delivery failure
- [ ] 5.3 Fixture proving candidate mutation after a decision invalidates that decision
- [ ] 5.4 Fixture proving a plan referencing a stale candidate is rejected
- [ ] 5.5 Update README/CHANGELOG for the `--approve-gates` -> `--approve-gate` migration
