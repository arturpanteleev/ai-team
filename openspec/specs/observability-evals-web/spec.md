# observability-evals-web Specification

## Purpose
TBD - created by archiving change control-plane-hardening. Update Purpose after archive.
## Requirements
### Requirement: Append-only lifecycle events
The system MUST persist sequence-checked, SHA-256 hash-chained run and attempt
events sufficient to reconstruct the current state.

#### Scenario: Process crash
- **WHEN** the CLI terminates without a final event
- **THEN** reconciliation MUST mark the stale run interrupted instead of leaving it running forever

#### Scenario: Event record is modified
- **WHEN** a persisted event is changed, removed, inserted or reordered
- **THEN** event log verification MUST fail before the controller appends another event

#### Scenario: Lifecycle is replayed
- **WHEN** a verified event log is replayed
- **THEN** run status, attempts and invalidations MUST be reconstructed deterministically
- **AND** impossible transitions or a mismatched attempt manifest digest MUST fail closed

### Requirement: Immutable web history
Web API and UI MUST display artifacts and evidence belonging to the selected run.

#### Scenario: Newer run of same feature
- **WHEN** a feature is run again
- **THEN** an older run page MUST continue displaying the older run artifacts

### Requirement: Safe artifact serving
The web server MUST bind to localhost by default and prevent lexical, absolute and symlink traversal outside the configured artifact root.

#### Scenario: Symlink outside root
- **WHEN** an artifact path resolves through a symlink to a file outside the root
- **THEN** the server MUST deny access

### Requirement: Layered evals
The eval system MUST distinguish deterministic contract evals, behavioral fixtures, workflow fault-injection and statistical LLM quality evals.

#### Scenario: LLM score used as hard gate
- **WHEN** an LLM quality score has no calibrated error bounds
- **THEN** it MUST remain advisory and MUST NOT be the sole hard gate

