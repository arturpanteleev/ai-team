## ADDED Requirements

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

