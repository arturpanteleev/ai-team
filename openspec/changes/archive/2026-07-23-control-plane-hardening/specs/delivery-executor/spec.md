## ADDED Requirements

### Requirement: Validated delivery plan
Delivery MUST consume a structured plan containing branch, base commit, exact
file set, file SHA-256/modes, verification evidence, preconditions, commit
message and PR metadata.

#### Scenario: Unrelated dirty file
- **WHEN** a dirty file is not listed in the validated delivery plan
- **THEN** it MUST NOT be staged or committed

#### Scenario: Git transforms staged bytes
- **WHEN** attributes, filters or line-ending normalization make staged blobs differ from approved bytes
- **THEN** the executor MUST reject delivery before commit

### Requirement: Protected branch safety
The delivery executor MUST determine and reject the repository default or protected branch before push.

#### Scenario: Detached or default branch
- **WHEN** delivery starts from a detached HEAD or default branch without an approved feature branch transition
- **THEN** push MUST be denied

### Requirement: Idempotent delivery
Delivery MUST persist step results and safely resume after partial failure.

#### Scenario: Push succeeds and PR creation fails
- **WHEN** a retry occurs after the remote branch already exists
- **THEN** the executor MUST reuse the verified commit and retry only PR creation

#### Scenario: Crash after commit before state persistence
- **WHEN** branch HEAD advanced after the exact approved commit but commit identity was not persisted
- **THEN** the executor MUST re-verify commit message, parent, paths, modes and blob hashes before recovery
- **AND** it MUST NOT create a duplicate commit
