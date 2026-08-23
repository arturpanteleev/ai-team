# control-plane-safety Specification

## Purpose
Безопасность control plane: workspace lock, fail-closed контракты, symlink rejection, bounded outputs, allow-list окружения.
## Requirements
### Requirement: Mandatory verdict contract
The controller MUST require exactly one valid verdict from every stage whose definition declares a verdict contract.

#### Scenario: Missing verdict
- **WHEN** a verdict-bearing stage exits successfully but emits no verdict
- **THEN** the stage MUST fail with a contract error
- **AND** downstream stages MUST NOT execute

#### Scenario: Ambiguous verdict
- **WHEN** an output contains multiple canonical verdict markers
- **THEN** the stage MUST fail closed instead of selecting one marker

### Requirement: Consistent outcomes
The system MUST derive CLI exit code, run status, stage status, HTML and web projections from the same domain outcome.

#### Scenario: Negative verdict
- **WHEN** a stage returns REJECTED, CHANGES_REQUESTED or FAIL
- **THEN** no projection MUST display that stage or run as passed

#### Scenario: Canceled run
- **WHEN** the run is canceled before all stages complete
- **THEN** the final report MUST display canceled and MUST NOT display passed

### Requirement: Explicit side-effect approval
The system MUST deny delivery side effects unless a validated canonical delivery
plan and explicit approval of that plan's exact SHA-256 are present.

#### Scenario: Non-interactive delivery
- **WHEN** delivery is reached without explicit approval in a non-interactive process
- **THEN** the controller MUST stop before commit, push or PR creation

#### Scenario: Approval does not match plan
- **WHEN** the supplied approval SHA-256 differs from the canonical plan SHA-256
- **THEN** the controller MUST stop before commit, push or PR creation

### Requirement: Mutation boundaries
Each stage MUST declare and obey its allowed mutation scope and capabilities.

#### Scenario: Read-only reviewer changes source
- **WHEN** a read-only stage changes a source file
- **THEN** the controller MUST reject the attempt and preserve evidence of the violation

### Requirement: Разделение control и candidate root

Controller MUST хранить durable state в control target, но MUST выполнять
source mutation и checks только в exact candidate root.

#### Scenario: Agent пытается изменить live source

- **КОГДА** mutation отсутствует в candidate delta, но появляется в live source
- **ТОГДА** run MUST завершиться fail-closed

### Requirement: Control plane не исполняет cloud run inline

При настроенном cloud worker launcher control plane MUST передавать execution
отдельному worker process/job и MUST NOT вызывать AI runtime в HTTP process.

#### Scenario: Worker command настроен

- **КОГДА** web control plane получает start или resume
- **ТОГДА** он MUST создать worker job через process-backed engine
- **И** HTTP process MUST только наблюдать persisted lifecycle/events

