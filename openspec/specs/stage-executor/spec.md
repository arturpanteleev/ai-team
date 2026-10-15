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
variables of at least 4 bytes (except `PATH`) and secret-like fragments found by
the shared redactor MUST be redacted from the saved log, summary, and metadata;
a secret outside `env_allow` is covered only by the shared redactor. When every
non-controller agent stage names an adapter, runtime preflight MUST NOT require
the agent CLI or its credentials and MUST instead verify that each used adapter's
program exists and is executable, a relative path staying inside the repository.
Config validation MUST reject `adapter` on a controller operation (`delivery`,
`deploy`).

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

### Requirement: Container executor runs the same contract in a sandbox

An adapter with a `docker` block MUST run its `argv` inside the configured
image through the shared container primitive, speaking the unchanged JSON
contract. The image MUST be pinned by digest unless `allow_unpinned_image` is
set; the network MUST be `none` unless `bridge` is configured; CPU, memory and
process limits, a read-only root file system, dropped capabilities and
no-new-privileges MUST always apply. The container MUST receive only the declared
stage inputs mounted read-only, a private per-attempt result directory mounted
read-write, optionally the repository read-only, read-only repository mounts
named in the configuration, and the allowlisted environment variables; the
request MUST name container paths only. A result given as a file in the result
directory MUST be copied to the declared output only when the program reported
`status: ok`; an output given both as content and as a file, a container exit
failure, an unpinned or missing image, a timeout and an output limit hit MUST
fail the attempt as a technical error, never as a verdict. The container MUST be
removed on cancellation and timeout, containers of a dead controller MUST be
removable by the `ai-team.stage-attempt` label, and the attempt `executor_info`
metadata MUST record the image reference with its digest, network, limits,
mounts and every exception to the strict defaults, without environment values.
Selecting another container agent MUST require only configuration or an
adapter program, not a change of the graph engine.

#### Scenario: Container review follows the existing routes

- **КОГДА** a `code_review` stage runs through a container adapter and the agent
  reports `CHANGES_REQUESTED` and later `APPROVED`
- **ТОГДА** the controller MUST offer the configured return to `implementation`
  for the first result and the approval route for the second, and both attempts
  MUST record the container image digest in `executor_info`

#### Scenario: Container failure is not a verdict

- **КОГДА** the container exits non-zero, prints invalid JSON, reports
  `status: error`, or claims a verdict that disagrees with its report
- **ТОГДА** the attempt MUST fail technically and no review output MUST be
  recorded as a completed stage
