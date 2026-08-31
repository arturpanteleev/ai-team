# control-plane-safety delta (P1-4 containment-profile)

## Modified Requirements

### Requirement: Containment receipt in run evidence

Run evidence manifest (`RunManifest`) MUST include `ContainmentReceipt` field
containing per-axis containment status. Receipt MUST be generated on terminal
run completion and included in the immutable run manifest.

#### Scenario: Receipt present in evidence

- **WHEN** a terminal run completes
- **THEN** `RunManifest.ContainmentReceipt` MUST be non-nil
- **AND** receipt MUST contain keys: fs, net, proc, env

### Requirement: Containment receipt in gate evidence

Gate attestation bundle MAY include containment receipt for the gate run.
If present, receipt MUST be included in gate.json record and verifiable
during `ai-team verify`.

#### Scenario: Gate verify shows receipt

- **WHEN** `ai-team verify` checks a gate bundle with containment receipt
- **THEN** verify output MUST include per-axis receipt status
