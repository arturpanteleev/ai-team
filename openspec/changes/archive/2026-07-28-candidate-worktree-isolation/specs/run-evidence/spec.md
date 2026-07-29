## ADDED Requirements

### Requirement: Candidate metadata в run evidence

Run evidence MUST сохранять candidate metadata и актуальную `candidate.json`
без зависимости от существования live projection.

#### Scenario: Evidence проверяется после run

- **КОГДА** worktree позднее удалён
- **ТОГДА** base identity, patch hash, changed files, checks и attempts MUST
  оставаться доступны в immutable run evidence
