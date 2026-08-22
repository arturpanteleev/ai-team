# workflow-engine Specification

## Purpose
Детерминированное исполнение workflow: контроллер владеет переходами, исходами и лимитами визитов; LLM предлагает только вердикты.
## Requirements
### Requirement: Explicit state machine
Pipeline MUST вычислять execution/decision/outcome явно, независимо от console
и report adapters, и MUST сохранять подтверждённое состояние на durable stage
boundaries.

#### Scenario: Transition test
- **WHEN** the state machine receives the same state, policy and event sequence
- **THEN** it MUST produce the same outcome and next transition

#### Scenario: Process restart
- **КОГДА** процесс завершается между stages
- **ТОГДА** новый процесс MUST восстановить следующий допустимый stage из
  persisted state
- **И** MUST NOT повторять уже подтверждённый attempt без явного rollback

### Requirement: Declarative contracts
Stage behavior MUST be declared in configuration rather than inferred from agent names.

#### Scenario: Mutation stage with another name
- **WHEN** a stage named `implementer` declares source mutation and git-change evidence
- **THEN** it MUST receive the same guard as a stage named `coder`

### Requirement: Strict configuration
Configuration and agent definitions MUST reject unknown fields, duplicate stages, invalid loopback targets and unsupported ordering constraints.

#### Scenario: Misspelled field
- **WHEN** config contains an unknown top-level field (например, опечатка в имени ключа)
- **THEN** validation MUST fail before any task or artifact file is changed

### Requirement: Explicit non-interactive policy
Every checkpoint MUST define its non-interactive behavior.

#### Scenario: Required checkpoint without TTY
- **WHEN** a required checkpoint is reached without a TTY or pre-authorized approval
- **THEN** the run MUST stop with policy-denied
