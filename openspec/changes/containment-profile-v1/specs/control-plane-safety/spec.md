# control-plane-safety delta (P1-4 containment-profile)

## Modified Requirements

### Requirement: Containment receipt in run evidence

The run evidence directory MUST include a `containment_receipt` per-axis
containment status, persisted as `{RunDir}/containment.json` at terminal run
completion. `run.json` is immutable since `Start`, so the receipt is a sibling
evidence file (parallel to `usage.json`) rather than a field inside
`RunManifest`. Legacy runs without a receipt remain valid; `ai-team verify`
and the gate treat their axes as `UNAVAILABLE`.

#### Scenario: Receipt present in evidence

- **WHEN** a terminal run completes
- **THEN** `containment.json` MUST be present in the run evidence directory
- **AND** receipt MUST contain keys: fs, net, proc, env
- **AND** each key MUST be one of: `ENFORCED`, `PARTIAL`, `UNAVAILABLE`

### Requirement: Containment receipt in gate evidence

When a gate run has a containment receipt, the gate MUST include it in the
gate.json record, and `ai-team verify` MUST verify it against the bundle.

#### Scenario: Gate verify shows receipt

- **WHEN** `ai-team verify` checks a gate bundle with containment receipt
- **THEN** verify output MUST include per-axis receipt status
