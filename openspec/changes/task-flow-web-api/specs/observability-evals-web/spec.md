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

Flow reports MUST preserve usage attestation and provenance. Run totals MUST
use complete usage envelopes and stage totals MUST use attested attempt
manifests. A configured monthly subscription amount MAY be allocated across
runs by token share, but reports MUST NOT infer a price per model token. Missing
or incomplete usage in the applicable monthly denominator makes the estimate
unavailable.

#### Scenario: No usage envelope

- **WHEN** a run's usage envelope is absent or incomplete
- **THEN** metrics MUST mark its usage unknown and MUST NOT present zero tokens
  as measured usage
- **AND** aggregate token values MUST be null if any selected run is unknown

#### Scenario: Subscription allocation uses attested tokens

- **WHEN** a monthly subscription amount and complete envelopes exist for all
  runs in the relevant month
- **THEN** metrics MAY report the selected runs' token-weighted subscription
  share as an approximation
- **AND** JSON and CSV MUST identify the value as an estimate and preserve its
  envelope attestation source
