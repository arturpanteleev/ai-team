## ADDED Requirements

### Requirement: Task event feeds

The Web API MUST expose run-scoped and global event feeds that use the
persisted SQLite event ID as a resumable cursor. A warning filter MUST return
warning lifecycle events without changing the cursor ordering.

#### Scenario: Warning feed pagination

- **WHEN** a client requests only warnings after a persisted event cursor
- **THEN** the response MUST return matching warning events in cursor order
- **AND** MUST return a cursor that advances past every scanned event

### Requirement: Flow observability

Flow metrics MUST report lead time and cycle time through acceptance, stage
durations and execution counts, action counts, and known versus unknown token
usage for the selected period. JSON and CSV exports MUST use the same selected
period and data source.

#### Scenario: No usage envelope

- **WHEN** a run's usage envelope is absent or incomplete
- **THEN** metrics MUST mark its usage unknown and MUST NOT present zero tokens
  as measured usage
