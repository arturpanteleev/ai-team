# fail-closed-resume-evidence delta (OPS-3)

## ADDED Requirements

### Requirement: Fail-closed evidence verification on resume

Before continuing a non-terminal run, the system MUST verify the applicable
evidence chain and snapshots (run manifest identity/schema, config and workflow
snapshot digests, the event hash chain, and attempt manifest digests) in a
fail-closed way, and MUST reject the resume with a structured reason on any
mismatch.

#### Scenario: Tampered config snapshot blocks resume

- **WHEN** a run is interrupted, its `config.json` snapshot is modified, and
  resume is attempted
- **THEN** resume MUST be rejected with reason `config_snapshot`
- **AND** the resume MUST NOT proceed to continue the run

#### Scenario: Tampered event chain blocks resume

- **WHEN** any event in `events.jsonl` is modified
- **THEN** resume MUST be rejected with reason `event_chain`

#### Scenario: Attempt manifest mismatch blocks resume

- **WHEN** an attempt manifest digest no longer matches the replayed chain
- **THEN** resume MUST be rejected with reason `attempt_manifest`

### Requirement: Structured rejection reason

The rejection MUST expose a stable machine-readable reason code and a sentinel
error, so callers can programmatically classify the failure.

#### Scenario: Reason is machine-readable

- **WHEN** resume is rejected due to evidence mismatch
- **THEN** the returned error MUST wrap the sentinel `ErrResumeEvidence`
- **AND** carry a `reason` code from the stable set
  (`manifest_identity`, `config_snapshot`, `workflow_snapshot`, `event_chain`,
  `attempt_manifest`, `already_terminal`)

### Requirement: Explicit recording of rejection reason

When the event log is appendable, the run MUST record a `resume_blocked` event
carrying the rejection reason and detail, so the failure cause is persisted in
the tamper-evident evidence log.

#### Scenario: Rejection reason is recorded

- **WHEN** resume is rejected with an appendable event log
- **THEN** the run's evidence log MUST contain a `resume_blocked` event
- **AND** its `data.reason` MUST equal the stable reason code
- **AND** the event chain MUST remain valid after the appended event

#### Scenario: Terminal run

- **WHEN** resume is attempted on a terminal run
- **THEN** it MUST be rejected with reason `already_terminal`
