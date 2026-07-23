# deterministic-checks Specification

## Purpose
TBD - created by archiving change control-plane-hardening. Update Purpose after archive.
## Requirements
### Requirement: Command evidence
The controller MUST execute configured deterministic checks and persist their command, tool version, timing, exit code, stdout and stderr.

#### Scenario: Failed test command
- **WHEN** a required test command exits non-zero
- **THEN** the stage MUST fail regardless of an LLM-authored PASS claim

### Requirement: Check classes
The workflow MUST support formatter, lint, build, unit, integration, end-to-end, coverage, race and security check classes.

#### Scenario: Optional unavailable check
- **WHEN** an optional check tool is unavailable
- **THEN** the outcome MUST be explicitly skipped with a reason
- **AND** it MUST NOT be reported as passed

### Requirement: Reproducible check context
Each check MUST record workspace baseline and relevant configuration.

#### Scenario: Dirty workspace
- **WHEN** a mutation stage starts in an allowed dirty workspace
- **THEN** the controller MUST distinguish pre-existing changes from changes created by the attempt

### Requirement: Process tree termination on cancellation
When a stage or check process is canceled or times out, the controller MUST attempt to terminate that process's descendants in addition to the process itself, on every supported platform where a mechanism to do so is available.

#### Scenario: Non-Unix descendant process
- **WHEN** a canceled or timed-out process on a non-Unix platform has spawned child processes of its own
- **THEN** the controller MUST attempt a whole-tree termination
- **AND** MUST fall back to terminating only the direct process if tree termination is unavailable, without failing the cancellation itself
