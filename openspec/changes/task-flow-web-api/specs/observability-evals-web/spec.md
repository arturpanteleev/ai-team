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

Flow metrics MUST report lead time and cycle time through acceptance, with
median and nearest-rank p80; stage durations and attempt counts grouped by
execution, decision, and outcome; agent-attempt share by stage; successful
agent results without linked human edits; override, skip, and return counts by
issuing stage; and known versus unknown token usage for the selected period.
JSON and CSV exports MUST use the same selected period and expose the same
metric set.

Flow reports MUST preserve usage attestation and provenance. Run totals MUST
use complete usage envelopes and stage totals MUST use attested attempt
manifests. The monthly subscription amount and optional currency in
`.ai-team/config.yaml` are the only source of the subscription estimate;
omitted currency defaults to USD. A configured monthly subscription amount MAY
be allocated across runs and stages in proportion to their attested tokens
divided by all complete project run tokens in the same UTC month. Reports MUST
NOT expose runtime `cost_usd` as subscription or tariff cost. Missing or
incomplete selected usage, stage manifests, or monthly denominators makes the
estimate unknown rather than zero. Separately, reports MUST show the provider
tariff cost as attested agent-attempt tokens multiplied by the configured
`usage.tariffs` price of the attempt's model effective at the attempt; each
tariff entry MUST carry its currency, effective date and source, and ai-team
MUST NOT ship or infer prices. An attempt without a recorded model, without a
tariff for its model, or with cache tokens lacking a cache price makes the
tariff cost unknown rather than zero. A 1000-run metrics calculation MUST complete within
300 milliseconds.

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
- **AND** metrics MAY report each stage's token-weighted subscription share
- **AND** JSON and CSV MUST identify the value as an estimate and preserve its
  envelope attestation source

#### Scenario: State-grouped stage metrics

- **WHEN** a client requests metrics for a period containing stage attempts
- **THEN** stage attempt counts and durations MUST be grouped by execution,
  decision, and outcome, with invalidated attempts excluded
- **AND** override, skip, and return counts MUST use the issuing stage
