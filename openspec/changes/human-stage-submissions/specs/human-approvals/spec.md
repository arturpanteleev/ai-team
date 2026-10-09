## ADDED Requirements

### Requirement: Immutable human stage submission versions

The controller MUST store each typed human stage result as an immutable,
stage-scoped revision. The revision MUST retain the exact submitted bytes,
description, actor, stage, approval ID, result kind, configured link kind when
applicable, monotonically increasing version, and SHA-256 of the result bytes.
The resolved input approval decision MUST bind the same version and SHA-256.
An exact retry for the same approval MUST return the existing revision; a
changed concurrent or repeated submission for that approval MUST be rejected
without replacing any stored version.

#### Scenario: Exact submission and retry

- **WHEN** an authorized actor submits the configured result for a pending
  human input approval
- **THEN** the controller MUST persist a new immutable stage revision and
  resolve the approval with matching exact bytes, version, and SHA-256
- **AND** repeating the same submission MUST return the same revision

#### Scenario: Concurrent changed submissions

- **WHEN** two submissions for one approval race with different bytes
- **THEN** at most one MUST resolve the approval
- **AND** the losing submission MUST report a conflict without overwriting the
  winning revision or decision

#### Scenario: Next visit to a human stage

- **WHEN** the workflow later returns to the same human stage with a new input
  approval
- **THEN** the next result MUST append a new version after the prior immutable
  stage revision

### Requirement: Missing human submission description is auditable

The controller MUST append a `description_missing` event when a human stage
resumes from a non-reject input decision whose description is empty or
whitespace-only. The event MUST be bound to the stage, active attempt, input
approval ID, and description field. The ordinary worker event API MUST NOT
accept this controller-only event. Replay MUST reject the event unless it
belongs to an unfinished attempt whose executor is `human` and whose input
approval identity matches.

#### Scenario: Empty description on a human result

- **WHEN** a resolved human stage input has no description
- **THEN** the resumed stage MUST record one controller-owned warning event
  before its attempt finishes

#### Scenario: Worker or agent forges the warning

- **WHEN** a worker submits `description_missing` through the ordinary event
  append API, or replay finds it attached to an agent attempt
- **THEN** the event MUST be rejected
