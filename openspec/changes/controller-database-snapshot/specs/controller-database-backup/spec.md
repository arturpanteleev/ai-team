## ADDED Requirements

### Requirement: Online controller database snapshot
The CLI MUST create an online, transactionally consistent SQLite snapshot at a
new destination, preserve committed WAL state, set restrictive file
permissions, and refuse to replace an existing destination. The command MUST
state that this snapshot does not include run evidence, artifacts, or other
filesystem state.

#### Scenario: Snapshot of an active database
- **WHEN** `ai-team db backup --db <source> --out <new path>` runs while the
  controller database is open
- **THEN** the resulting SQLite file MUST open independently and contain the
  committed rows from the source, including rows not yet checkpointed from WAL
- **AND** the file mode MUST be 0600
- **AND** existing destination files MUST remain unchanged
- **AND** the CLI MUST state that evidence and artifacts are outside the
  snapshot
