# Stage executor switching — delta

## ADDED Requirements

### Requirement: Executor choice is scoped to one ready stage visit

The control plane MUST allow a user with the stage function role to select
`human` or `agent` for the current ready stage when it is waiting on a pending
approval. The selection MUST be bound to that approval identity and MUST NOT
be reused by a later loop or return visit to the same stage.

#### Scenario: Executor is changed for the current visit

- **WHEN** the current lifecycle checkpoint is waiting and its pending
  approval targets the requested stage
- **THEN** the selected executor is stored with the pending approval ID,
  actor, and timestamp
- **AND** the pipeline uses that executor when the approval resumes the stage

#### Scenario: A later visit has a different approval

- **WHEN** the stage is reached again under a new approval ID
- **THEN** an override bound to the old approval MUST NOT affect the new visit

### Requirement: Human stages can invoke the configured stage agent

An agent-capable typed human input approval MUST offer `run_agent` and
`refine_agent` actions. `run_agent` MUST invoke the pinned stage agent.
`refine_agent` MUST require the current result and provide it to the agent as
an input artifact included in the immutable attempt input snapshot.

#### Scenario: User selects «Сделай»

- **WHEN** a resolved human input approval selects `run_agent`
- **THEN** the configured agent executes for that stage
- **AND** evidence records `executor_changed`, `agent_started`, and
  `agent_finished`

#### Scenario: User selects «Доработай агентом»

- **WHEN** a resolved human input approval selects `refine_agent` with current
  result text
- **THEN** the exact text is supplied as the `current-result` input artifact
- **AND** the attempt manifest snapshots its bytes

### Requirement: Running checkpoints retain the resolved stage approval

The lifecycle state MUST retain the resolved approval identity when a run
moves from a waiting checkpoint into a stage, until dispatch has consumed that
visit. Resume MUST verify and restore the associated action or executor
override before dispatch.

#### Scenario: Process stops after checkpoint and before dispatch

- **WHEN** a resolved `run_agent` or `refine_agent` approval is checkpointed as
  running and the process stops before the stage starts
- **THEN** resume restores the same resolved approval and dispatches the pinned
  agent with the correct current-result input
- **AND** a visit-bound executor override remains attached only to that approval

### Requirement: Human edits of agent results are identified

The pipeline MUST record a human edit of a prior agent result for the same
stage with the source agent attempt ID in its evidence manifest and emit
`human_result_edited_agent`.

#### Scenario: Human changes an agent result

- **WHEN** a human submits a result for a stage with a prior agent attempt
- **THEN** the human attempt identifies that agent attempt as its edited source
- **AND** the configured output is replaced atomically only after validating
  the regular-file path
- **AND** the prior result is read from the exact output path in the human
  stage's configured output contract, even when the agent emitted multiple files
