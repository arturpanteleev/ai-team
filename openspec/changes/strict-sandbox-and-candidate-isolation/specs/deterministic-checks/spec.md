## MODIFIED Requirements

### Requirement: Reproducible check context
Each check MUST record workspace baseline and relevant configuration, and
MUST execute against the isolated candidate root under the sandbox backend
rather than against the live target directory.

#### Scenario: Dirty workspace
- **WHEN** a mutation stage starts in an allowed dirty workspace
- **THEN** the controller MUST distinguish pre-existing changes from changes created by the attempt

#### Scenario: Check needs network access
- **WHEN** a configured check requires network access (e.g. a dependency download)
- **THEN** the check definition MUST declare the exact host:port it needs
- **AND** the sandbox backend MUST deny any network access not declared
