## ADDED Requirements

### Requirement: Isolated candidate root per run
Every run MUST create an isolated candidate filesystem root from a fixed base
commit before any source-mutating agent or deterministic check executes. No
source-mutating stage and no check MUST read from or write to the live
target directory.

#### Scenario: Coder stage writes code
- **WHEN** the coder stage produces file changes
- **THEN** those changes MUST land only in the candidate root
- **AND** the live target directory MUST remain byte-for-byte unchanged until promotion

#### Scenario: Untracked files at run start
- **WHEN** the live target has untracked files present when a run starts
- **THEN** the controller MUST classify them as user-owned and MUST NOT copy them into the candidate implicitly

### Requirement: Candidate identity binds the whole run
A single `candidate.json` document MUST record the base tree hash, the
candidate tree/commit hash, the patch digest, every changed path with its
mode and blob SHA-256, every executed check's adapter/tool identity/output
digest, and every stage decision (review, test, verification) — all
referencing the same candidate hash.

#### Scenario: Candidate changes after a decision
- **WHEN** the candidate tree hash changes after a review or verification decision was recorded against a prior hash
- **THEN** that decision MUST be invalidated
- **AND** the invalidated decision MUST NOT satisfy any downstream precondition

#### Scenario: Delivery plan references a different candidate
- **WHEN** a delivery plan's candidate hash does not match the hash bound to the recorded review/test/verification decisions
- **THEN** delivery MUST be refused

### Requirement: Explicit promotion
Updating the live target to reflect a delivered candidate MUST be a distinct,
explicit operation performed only after delivery succeeds, and MUST NOT occur
as an implicit side effect of agent execution.

#### Scenario: Delivery fails
- **WHEN** delivery does not succeed
- **THEN** the live target directory MUST remain exactly as it was before the run started
