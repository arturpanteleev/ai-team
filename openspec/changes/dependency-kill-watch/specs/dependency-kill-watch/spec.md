# dependency-kill-watch delta (EXP-1)

## ADDED Requirements

### Requirement: Kill signal tracking MUST stay operational

The strategy MUST maintain an executable kill-watch for external dependencies
that could make the trust-contour value proposition redundant.

#### Scenario: Each kill scenario has a signal and an action

- **WHEN** the kill-watch document lists a scenario
- **THEN** it MUST include a concrete early-detection signal and a defined action
- **AND** the set MUST cover: forge-issued attestation, harness-issued
  attestation, «verification is not a pain» (interview pivot), and an
  unintelligible bundle

#### Scenario: The interview pivot rule is explicit

- **WHEN** users report «not needed» in three consecutive interviews or there are
  zero external re-runs in 8 weeks
- **THEN** the document MUST switch to Track P as the primary instrument