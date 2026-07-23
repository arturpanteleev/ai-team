## MODIFIED Requirements

### Requirement: Validated delivery plan
Delivery MUST consume a structured plan containing the candidate identity,
branch, base commit, exact file set, file SHA-256/modes, verification
evidence, preconditions, commit message and PR metadata. The plan's file set
and digests MUST be derived from the isolated candidate root, not from a
live-workspace digest comparison.

#### Scenario: Unrelated dirty file
- **WHEN** a dirty file is not listed in the validated delivery plan
- **THEN** it MUST NOT be staged or committed

#### Scenario: Git transforms staged bytes
- **WHEN** attributes, filters or line-ending normalization make staged blobs differ from approved bytes
- **THEN** the executor MUST reject delivery before commit

#### Scenario: Plan built from a different candidate than was verified
- **WHEN** the plan's candidate identity does not match the candidate that review, tests and verification actually ran against
- **THEN** the executor MUST reject delivery before commit
