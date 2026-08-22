# run-evidence Specification

## Purpose
Immutable run/attempt manifests и hash-chained event log как источник истины; SQLite/web — восстанавливаемые projections.
## Requirements
### Requirement: Run and attempt identity
Run evidence MUST сохранять identity attempts, transitions и всех человеческих
approval requests/decisions в одной verified chain.

#### Scenario: Approval evidence
- **КОГДА** approval создаётся или получает решение
- **ТОГДА** event MUST содержать approval_id, subject hash, actor/action при
  решении и связанные from/to stages

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

### Requirement: Non-Unix workspace lock staleness recovery
On platforms without a native advisory-lock primitive, the workspace lock MUST record the acquiring process's pid and MUST reclaim an existing lock only when there is positive evidence the recorded pid no longer exists; inconclusive evidence MUST leave the lock in place.

#### Scenario: Lock holder no longer exists
- **WHEN** an existing lock's recorded pid can be positively confirmed as no longer running
- **THEN** the lock MUST be reclaimed and re-acquired by the new caller

#### Scenario: Inconclusive evidence
- **WHEN** the existing lock's pid file is missing, unreadable, unparseable, or its liveness cannot be positively disproven
- **THEN** the lock MUST NOT be reclaimed and acquisition MUST fail exactly as before this capability existed

### Requirement: Evidence compiled graph

Immutable workflow snapshot MUST содержать фактически исполненный compiled
graph, включая entry, edges, approval policies и max_visits.

#### Scenario: Resume graph run

- **КОГДА** graph run возобновляется после restart
- **ТОГДА** engine MUST проверить snapshot digest
- **И** восстановить visit counters и next node из immutable evidence/lifecycle

### Requirement: Candidate metadata в run evidence

Run evidence MUST сохранять candidate metadata и актуальную `candidate.json`
без зависимости от существования live projection.

#### Scenario: Evidence проверяется после run

- **КОГДА** worktree позднее удалён
- **ТОГДА** base identity, patch hash, changed files, checks и attempts MUST
  оставаться доступны в immutable run evidence
