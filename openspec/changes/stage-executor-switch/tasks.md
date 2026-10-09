# Stage executor switching (B-32) — tasks

- [x] Persist visit-bound executor overrides in lifecycle state.
- [x] Add secured web API to set the executor for the current ready stage.
- [x] Apply matching overrides in pipeline stage dispatch and clear them after
      the stage visit.
- [x] Add `run_agent` and `refine_agent` typed approval actions and snapshot
      the refinement text as an agent input.
- [x] Mark human edits of prior agent results in attempt evidence.
- [x] Persist and verify resolved approval identity across running checkpoints;
      select prior agent results by the human contract output path.
- [x] Add executor/agent events to evidence and worker API allowlist; retry
      uncertain completion writes and reject incomplete agent lifecycle replay.
- [x] Add pipeline, lifecycle, and web tests for visit identity and agent
      execution/refinement.
- [ ] Wire the CLI action `ai-team task stage <stage-id> run-agent` through
      the B-71 task-stage action interface after that branch lands; this branch
      provides the engine/API behavior only.
- [x] Run full `GOTOOLCHAIN=go1.26.9 make verify` on the revised implementation tree.
