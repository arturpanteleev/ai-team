# Stage executor switching (B-32) — tasks

- [x] Persist visit-bound executor overrides in lifecycle state.
- [x] Add secured web API to set the executor for the current ready stage.
- [x] Apply matching overrides in pipeline stage dispatch and clear them after
      the stage visit.
- [x] Add `run_agent` and `refine_agent` typed approval actions and snapshot
      the refinement text as an agent input.
- [x] Mark human edits of prior agent results in attempt evidence.
- [x] Add executor/agent events to evidence and worker API allowlist.
- [x] Add pipeline, lifecycle, and web tests for visit identity and agent
      execution/refinement.
- [ ] Wire the CLI action `ai-team task stage <stage-id> run-agent` through
      the B-71 task-stage action interface after that branch is available.
- [x] Run full `GOTOOLCHAIN=go1.26.9 make verify` on the implementation tree.
