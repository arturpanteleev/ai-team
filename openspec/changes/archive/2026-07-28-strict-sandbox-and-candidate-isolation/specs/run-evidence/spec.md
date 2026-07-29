## MODIFIED Requirements

### Requirement: Artifact provenance
Every published artifact MUST record producer, run, attempt, size, SHA-256
hash and the candidate identity it was produced against.

#### Scenario: Stale output
- **WHEN** a stage exits without publishing a fresh output for its current attempt
- **THEN** an output from an earlier attempt MUST NOT satisfy the contract

## ADDED Requirements

### Requirement: Terminal ledger anchor
The event ledger MUST expose a terminal root hash (or equivalent external
anchor) at the end of a run, computed such that a rewrite of the entire
`events.jsonl` file by its own owner is detectable, not just internal
hash-chain self-consistency.

#### Scenario: Full chain rewrite by the file owner
- **WHEN** every event in `events.jsonl` is rewritten with a new, internally
  self-consistent hash chain
- **THEN** comparing against the previously recorded terminal root hash MUST detect the tampering
