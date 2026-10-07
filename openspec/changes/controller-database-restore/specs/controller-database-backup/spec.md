## ADDED Requirements

### Requirement: Safe controller database snapshot restore
The CLI MUST validate the SQLite integrity of a snapshot and restore it only to
a new destination with restrictive permissions. It MUST refuse symlink input
and existing output paths, and state that only SQLite data is restored.

#### Scenario: Restore a valid database snapshot
- **WHEN** `ai-team db restore --from <snapshot> --out <new path>` receives a
  valid SQLite snapshot
- **THEN** it MUST produce an independently openable SQLite database with the
  same committed controller rows
- **AND** the output mode MUST be 0600
- **AND** it MUST leave an existing destination unchanged
- **AND** the CLI MUST state that run evidence and artifacts are not restored

#### Scenario: Reject an invalid snapshot
- **WHEN** restore receives a symlink or a database failing SQLite integrity
  checking
- **THEN** it MUST fail without publishing the destination
