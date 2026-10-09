# Design: stage executor switching

`lifecycle.State.ActiveApprovalID` carries the resolved handoff across the
waiting-to-running checkpoint. Resume verifies that approval against the
hash-chained decision/transition evidence before dispatch, so a process stop
after the checkpoint cannot drop a human action or executor override.

`lifecycle.State.ExecutorOverrides[stageID]` carries the selected executor,
the prior default/selection, actor, timestamp, active approval ID, and the
originating visit ID. The API only updates the `NextStage` of a waiting
checkpoint after it confirms the same pending approval targets that stage.
When a human executor opens its typed input approval, the active approval ID
is rebound to that form while the originating visit ID remains stable. The
pipeline clears the override after that stage attempt completes.

On resume, the pipeline uses the override only when its approval ID matches
the current resolved approval. It appends `executor_changed` with a stable
change ID and checks the existing event log first, so a crash between event
append and lifecycle update does not duplicate the event. A later approval
visit has a different ID and therefore falls back to the pinned executor.

For a human form backed by an agent, `run_agent` starts a fresh invocation.
`refine_agent` requires the current result in the decision comment, writes it
to a temporary file under the run evidence directory, and lets the normal
input snapshot mechanism bind its exact bytes to the agent attempt. The
temporary is removed after the invocation has snapshotted it.

When a human submission replaces a previous agent output at its configured
path, the pipeline selects the prior agent artifact by that exact configured
path, even if the attempt emitted other files. The writer permits atomic replacement only when the typed approval
binds a prior agent attempt. The human attempt event and manifest record that
attempt ID. Normal human submissions remain immutable/idempotent.

The API route is `POST /api/runs/{runID}/executor` with
`{"stage_id":"…","executor":"human|agent"}`. It uses the user's stage
function role and returns the selected visit ID. The CLI stage-action command
is intentionally left for the B-71 integration seam; the intended action is
`ai-team task stage <stage-id> run-agent`.
