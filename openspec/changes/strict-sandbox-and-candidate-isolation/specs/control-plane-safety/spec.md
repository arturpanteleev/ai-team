## MODIFIED Requirements

### Requirement: Explicit side-effect approval
The system MUST deny delivery side effects unless a validated canonical
delivery plan referencing the current candidate identity and explicit
approval of that plan's exact SHA-256 are present.

#### Scenario: Non-interactive delivery
- **WHEN** delivery is reached without explicit approval in a non-interactive process
- **THEN** the controller MUST stop before commit, push or PR creation

#### Scenario: Approval does not match plan
- **WHEN** the supplied approval SHA-256 differs from the canonical plan SHA-256
- **THEN** the controller MUST stop before commit, push or PR creation

#### Scenario: Plan references a stale candidate
- **WHEN** the delivery plan's candidate identity does not match the candidate identity bound to the run's recorded review/test/verification decisions
- **THEN** the controller MUST stop before commit, push or PR creation regardless of a valid plan-hash approval

### Requirement: Mutation boundaries
Each stage MUST declare and obey its allowed mutation scope and capabilities,
enforced by executing the stage inside its isolated candidate root under a
sandbox backend rather than by comparing filesystem snapshots of a live,
directly-writable workspace.

#### Scenario: Read-only reviewer changes source
- **WHEN** a read-only stage's sandboxed process attempts to write a source file
- **THEN** the sandbox backend MUST deny the write
- **AND** the controller MUST reject the attempt and preserve evidence of the violation

## ADDED Requirements

### Requirement: Exact per-checkpoint approval
Non-interactive approval of a checkpoint MUST be scoped to that checkpoint's
exact subject SHA, not granted blanket for every future checkpoint in the run.

#### Scenario: Blanket approval flag
- **WHEN** a user supplies an approval flag without a checkpoint-specific subject SHA
- **THEN** the controller MUST reject it as insufficient for any checkpoint
- **AND** MUST require `--approve-gate <gate-id>:<subject-sha>` matching the actual reached checkpoint's subject

#### Scenario: Approval subject mismatch
- **WHEN** a supplied per-gate approval's subject SHA does not match the checkpoint actually reached
- **THEN** the controller MUST stop and MUST NOT treat the checkpoint as approved
