# stage-executor Specification

## Purpose

Контракт исполнителя этапа: кто создаёт результат этапа (внутренний агент или
именованный внешний адаптер) и по какому протоколу сдаёт его контроллеру.
Исполнитель отделён от гейта: гейт проверяет результат, исполнитель его создаёт.
Ни один исполнитель не переводит граф сам.

## Requirements

### Requirement: Stage executor is selected in the stage configuration

A stage with `executor: agent` MUST be executed by the internal agent runtime
unless it names an adapter in `adapter`. A named adapter MUST be declared in the
top-level `executor_adapters` map with a non-empty `argv`; an unknown adapter, an
`adapter` on a stage that is not `executor: agent` with an `agent` contract, and
unknown keys in an adapter definition MUST be rejected when the config is
validated. The agent definition MUST remain the stage contract (inputs, outputs,
verdict, mutation scope) for both executors. An adapter MUST NOT be combined with
a controller operation (`delivery`, `deploy`).

#### Scenario: Unknown adapter is rejected before a run

- **КОГДА** a stage names an adapter that is absent from `executor_adapters`
- **ТОГДА** config validation MUST fail with the stage path

### Requirement: External executor uses a bounded JSON contract

The external executor MUST run the configured argv without a shell, in the
project directory, with a timeout (default 10 minutes, at most 24 hours), capped
stdout (the JSON result) and stderr (the log), and an environment consisting only
of the `env_allow` variables, a default `PATH`, and controller-owned
`AI_TEAM_*` variables; `env_allow` MUST NOT contain the `AI_TEAM_` prefix. The
controller MUST write one JSON request to stdin and read exactly one JSON
document with `schema_version: 1` from stdout, rejecting unknown fields, extra
documents, and an oversized body. The result carries `status` (`ok` or `error`),
optional `summary`, `verdict`, `metadata` (at most 32 entries, bounded values),
and `outputs` whose `content` the controller MUST write to the declared output
path; an undeclared output name MUST be rejected. Values of allowed environment
variables and secret-like fragments MUST be redacted from the saved log, summary,
and metadata.

#### Scenario: Failure never looks like a completed stage

- **КОГДА** the process exits non-zero, exceeds its timeout or output limit,
  prints invalid JSON, reports `status: error`, or leaves a declared output
  missing
- **ТОГДА** the attempt MUST fail with an execution error and the stage MUST NOT
  be recorded as passed
- **И** a timeout of the adapter itself MUST be distinguishable from the
  cancellation or deadline of the surrounding run

#### Scenario: Cancellation stops the process tree

- **КОГДА** the run context is canceled while the program runs
- **ТОГДА** the supervised process group MUST be killed and the context error
  MUST be returned so the controller classifies the attempt as canceled

### Requirement: Controller verifies the result like an internal agent result

After any executor returns, the controller MUST collect the declared outputs,
check their type and safety, apply the mutation scope of the agent definition,
parse the verdict from the outputs, and run the exit gates through the same code
path. A verdict claimed by an executor MUST only be cross-checked against the
verdict parsed from the outputs; a mismatch MUST fail the attempt. An executor
MUST NOT decide graph transitions, approvals, or delivery. Retry and cancellation
MUST behave as for the internal agent.

#### Scenario: Mutation outside the scope fails the stage

- **КОГДА** an external program changes a file outside the stage mutation scope
- **ТОГДА** the mutation guard MUST fail the attempt even if the program reported
  `status: ok`

### Requirement: Attempt evidence names the actual executor

Every automated attempt MUST record the executor that actually ran as
`executor_info` in the attempt manifest, with `kind` (`internal_agent` or
`external`), `name` (the runtime type or adapter name), and for external
executors the summary, metadata, and the log file name. The `attempt_finished`
evidence and task events MUST carry `executor_kind` and `executor_name`. The
`executor` field MUST keep meaning the human/agent class. Failed attempts MUST
keep the executor identity.

#### Scenario: Status shows the external adapter

- **КОГДА** a stage attempt ran through adapter `demo`
- **ТОГДА** its manifest MUST contain `executor_info.kind = external` and
  `executor_info.name = demo`
