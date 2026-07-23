# workflow-engine Specification

## Purpose
TBD - created by archiving change control-plane-hardening. Update Purpose after archive.
## Requirements
### Requirement: Explicit state machine
Workflow transitions MUST be evaluated by a deterministic state machine independent of console, report and persistence adapters.

#### Scenario: Transition test
- **WHEN** the state machine receives the same state, policy and event sequence
- **THEN** it MUST produce the same outcome and next transition

### Requirement: Declarative contracts
Stage behavior MUST be declared in configuration rather than inferred from agent names.

#### Scenario: Mutation stage with another name
- **WHEN** a stage named `implementer` declares source mutation and git-change evidence
- **THEN** it MUST receive the same guard as a stage named `coder`

### Requirement: Strict configuration
Configuration and agent definitions MUST reject unknown fields, duplicate stages, invalid loopback targets and unsupported ordering constraints.

#### Scenario: Misspelled field
- **WHEN** config contains `gate_afer` instead of `gate_after`
- **THEN** validation MUST fail before any task or artifact file is changed

### Requirement: Explicit non-interactive policy
Every checkpoint MUST define its non-interactive behavior.

#### Scenario: Required checkpoint without TTY
- **WHEN** a required checkpoint is reached without a TTY or pre-authorized approval
- **THEN** the run MUST stop with policy-denied

