# run-evidence Specification

## Purpose
TBD - created by archiving change control-plane-hardening. Update Purpose after archive.
## Requirements
### Requirement: Run and attempt identity
Every pipeline run and stage attempt MUST have stable unique identifiers.

#### Scenario: Repeated feature
- **WHEN** the same feature is executed more than once
- **THEN** each run MUST store independent logs, reports, artifacts and events

### Requirement: Artifact provenance
Every published artifact MUST record producer, run, attempt, size and SHA-256 hash.

#### Scenario: Stale output
- **WHEN** a stage exits without publishing a fresh output for its current attempt
- **THEN** an output from an earlier attempt MUST NOT satisfy the contract

### Requirement: Retry invalidation
Retry and loopback MUST invalidate downstream evidence from superseded attempts.

#### Scenario: Loopback to coder
- **WHEN** verifier sends the workflow back to coder
- **THEN** previous reviewer, tester and verifier outputs MUST NOT be reused as current evidence

### Requirement: Atomic publication
Artifacts and manifests MUST be published atomically.

#### Scenario: Interrupted write
- **WHEN** a process is terminated during output creation
- **THEN** the partial file MUST NOT be accepted as a completed artifact

