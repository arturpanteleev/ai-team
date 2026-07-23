## ADDED Requirements

### Requirement: Non-Unix workspace lock staleness recovery
On platforms without a native advisory-lock primitive, the workspace lock MUST record the acquiring process's pid and MUST reclaim an existing lock only when there is positive evidence the recorded pid no longer exists; inconclusive evidence MUST leave the lock in place.

#### Scenario: Lock holder no longer exists
- **WHEN** an existing lock's recorded pid can be positively confirmed as no longer running
- **THEN** the lock MUST be reclaimed and re-acquired by the new caller

#### Scenario: Inconclusive evidence
- **WHEN** the existing lock's pid file is missing, unreadable, unparseable, or its liveness cannot be positively disproven
- **THEN** the lock MUST NOT be reclaimed and acquisition MUST fail exactly as before this capability existed
