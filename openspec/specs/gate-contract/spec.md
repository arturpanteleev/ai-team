# gate-contract Specification

## Purpose

Контракт гейта: определение в общей версионированной библиотеке, ссылка этапа с
точной версией и параметрами, группы, раздельные результаты исполнения и
вердикта, а также исполнение цепочки гейтов: последовательные группы,
параллельные гейты группы, фиксация хеша результата, durable-журнал попыток и
восстановление после перезапуска.

## Requirements

### Requirement: Gate definitions live in a versioned code library

A gate definition MUST have an ID, an exact positive integer version, a kind
(`builtin`, `llm`, `human`, `external`, or `docker`), a title, a typed parameter
schema, and optionally the result types and link kinds it applies to. The
library MUST live in repository code and MUST change only through a code change;
the Web UI, REST API, and MCP MUST expose the catalog read-only and MUST NOT
accept uploaded or edited definitions. A published ID and version MUST be
immutable: a change of behavior or parameters MUST be a new version, and older
versions MUST remain resolvable. Registering an existing ID and version MUST be
rejected. The catalog MUST be readable at `GET /api/gates/catalog` and include a
digest of each definition.

#### Scenario: Published definition cannot be replaced

- **КОГДА** a definition with an existing ID and version is registered again
- **ТОГДА** registration MUST fail and instruct to publish a new version

#### Scenario: Catalog is read-only

- **КОГДА** a client sends POST, PUT, PATCH, or DELETE to `/api/gates/catalog`
- **ТОГДА** the request MUST be rejected
- **И** GET MUST list every definition with its kind, parameters, and digest

### Requirement: Built-in library wraps the existing checks

The version-1 library MUST contain code gates `required_sections` (parameter
`sections`, `md` results), `verdict_marker` (`md`), `pr_exists`, `pr_open`,
`pr_base_branch` (`link`), `project_checks` and `pr_checks_green` (`link` with
`link_kind: pr`), the LLM gate `agent_review` (parameters `agent`, `mode`,
`max_rounds`), the human gate `human_approval`, and the external-program gate
`external_program` (see the external-gate capability). Each version-1 gate MUST keep
the semantics of its schema v5 counterpart.

#### Scenario: Gate applicability

- **КОГДА** `project_checks@1` is referenced from a stage with `link_kind: build`
- **ТОГДА** validation MUST reject the reference naming the required link kind

### Requirement: Stages reference exact versions with declared parameters

A gate reference MUST carry the definition ID and an exact version; a reference
without a version MUST be rejected and "latest" MUST NOT exist. Parameters MUST
be declared by the definition, correctly typed, within bounds, and all required
ones present. A reference MAY carry an instance `name` (unique within the stage,
default the gate ID) and a `mode` of `required` (default) or `advisory`. An
unknown ID, unknown version, undeclared or invalid parameter, inapplicable
result type, empty group, or duplicate instance name MUST be rejected before a
run starts with an error naming the stage and the `exit_gate_groups[i][j]` path.
A reference MAY be written as `id@version` or as a mapping with `gate`,
`version`, `name`, `mode`, and `params`; unknown mapping keys MUST be rejected.

#### Scenario: Unknown version

- **КОГДА** a template references `verdict_marker@9`
- **ТОГДА** validation MUST fail and list the available versions

#### Scenario: Unknown parameter

- **КОГДА** a reference passes a parameter the definition does not declare
- **ТОГДА** validation MUST fail naming the parameter and the accepted ones

#### Scenario: Only the needed gates form several groups

- **КОГДА** a stage lists `[verdict_marker@1]` then `[human_approval@1]`, and
  another stage lists one group of two gates, and a third lists none
- **ТОГДА** each stage MUST use exactly the gates it lists in the listed group
  order

### Requirement: Templates and tasks pin exact definitions

A template MUST bind to exact definitions through its `id@version` references,
and a task MUST use the template YAML it pinned at start. Publishing a newer
version of a definition MUST NOT change any published template or started task:
their references MUST keep resolving to the same definition, and the pin of a
definition MUST include its digest. A newer version MUST apply only when a
template explicitly names it and satisfies its parameter schema.

#### Scenario: New library version after publication

- **КОГДА** the library publishes `required_sections@2` after a template pinned
  `required_sections@1`
- **ТОГДА** the template MUST keep validating and its pins MUST be unchanged
- **И** a template that names `@2` MUST satisfy the version-2 parameters

### Requirement: Attempt result separates execution from verdict

A gate attempt MUST record the task, stage, stage visit, SHA-256 of the checked
result version, gate ID, version and instance name, mode, attempt ID, group
index, executor kind, start and finish times, a redacted reason, and references
to sanitized evidence. Technical `execution` (`pending`, `running`, `succeeded`,
`error`, `timeout`, `canceled`) and content `verdict` (`pass`, `fail`,
`not_applicable`) MUST be separate fields: a verdict MUST be present exactly when
execution is `succeeded`, so an error, a timeout, and a negative verdict never
collapse into one status. `error` and `timeout` MUST be classified as technical
failures that may be retried on the same result version without erasing the
earlier attempt. A required attempt MUST block unless it succeeded with `pass` or
`not_applicable`; an advisory attempt MUST NOT block.

#### Scenario: Verdict on a failed execution

- **КОГДА** an attempt has `execution: timeout` and a `verdict`
- **ТОГДА** the record MUST be invalid

#### Scenario: Blocking semantics

- **КОГДА** a required attempt ends `error`, `timeout`, `canceled`, or
  `succeeded` with `fail`
- **ТОГДА** it MUST block the transition
- **И** an advisory attempt with the same outcome MUST NOT block

### Requirement: Groups run sequentially and gates of a group run in parallel

The controller MUST execute the exit chain of a stage group by group in the
listed order, and MUST start the gates of one group together, running at most
the configured concurrency limit (`gate_concurrency`, default 4, range 1 to 64)
at the same time. A group MAY mix gate kinds (code and LLM) and a stage MAY have
several LLM gates; a `human_approval` gate MUST be the transition approval of the
stage and therefore MUST NOT precede an automatic group. The next group MUST
start only after every gate of the previous group reached a terminal outcome and
every required gate passed (or was `not_applicable`). When a required gate does
not pass, the following groups MUST NOT start, but every gate of the current
group that was started MUST be awaited and its own outcome recorded. An advisory
gate MUST be awaited with its group but MUST NOT decide whether the chain
continues. LLM gates of one group MUST run one at a time.

#### Scenario: Dependent gate after a failure

- **КОГДА** a chain has two gates in the first group and a third gate in the
  second group and one of the first two returns `fail`
- **ТОГДА** both first gates MUST finish with their own outcomes
- **И** the third gate MUST NOT start and MUST NOT have an attempt

#### Scenario: Dependent gate after success

- **КОГДА** both gates of the first group pass
- **ТОГДА** the gate of the second group MUST start exactly once, after both
  first gates finished

#### Scenario: Concurrency limit

- **КОГДА** a group has more gates than `gate_concurrency`
- **ТОГДА** no more than `gate_concurrency` of them MUST run at the same time

### Requirement: The result version is frozen for the whole wave

A wave MUST be bound to one result version: the SHA-256 of the declared outputs
of the checked stage attempt. The controller MUST verify before the wave, after
every gate, and at the end of the wave that the hash did not change. A change MUST
void the wave: no outcome of it MAY be used, no following group MAY start, and a
`gate_wave_aborted` record MUST be written. A recorded outcome MUST be read only
for the exact stage, visit, stage attempt, and hash of the wave: an outcome of an
earlier version MUST NOT count for a new one, so a new version MUST run every
group again from the first.

#### Scenario: Result edited during the wave

- **КОГДА** the checked result changes while gates run
- **ТОГДА** the wave MUST be aborted with an error naming the change
- **И** the next group MUST NOT start

#### Scenario: New version after a partial success

- **КОГДА** the first group passed for hash A and the result then changes to hash B
- **ТОГДА** every group MUST run again for hash B, and the outcomes recorded for
  hash A MUST remain in the history without deciding the transition of hash B

### Requirement: Every attempt is a durable record

Before a gate is invoked the controller MUST record `gate_attempt_started`, and
when the gate reaches a terminal state it MUST record `gate_attempt_finished`; a
gate that waits for an external answer MUST be recorded as `gate_attempt_waiting`
and keep the same attempt. The records MUST be events of the hash-chained run
log carrying the task, stage, visit, stage attempt, result hash, gate ID, version
and instance name, mode, group, executor kind, attempt ID, start and finish
times, technical `execution`, content `verdict`, and reason. The attempt ID MUST
be deterministic for the wave key, gate instance and ordinal, and a terminal
record MUST be final: a redelivered earlier record MUST NOT reopen the attempt.
When the controller cannot write the start record it MUST NOT invoke the gate.
At task start the controller MUST record `gate_pins_recorded` with the ID,
version, kind and digest of every pinned definition, and on a later start MUST
refuse to continue when a definition's digest differs from the recorded one.

#### Scenario: History of attempts

- **КОГДА** a wave finishes
- **ТОГДА** the run log MUST show, for every attempt, its start and finish times
  and its execution and verdict, and a failed gate MUST be distinguishable from
  an errored or timed-out one

#### Scenario: Pins at task start

- **КОГДА** a task starts
- **ТОГДА** exactly one `gate_pins_recorded` record MUST list the pinned
  definitions with their digests

### Requirement: Restart neither repeats nor loses gate work

After a restart or a redelivered resume the controller MUST NOT invoke a gate
whose latest attempt for the wave already succeeded, and MUST NOT retry a
recorded technical failure (`error`, `timeout`) unless a retry of exactly that
gate was requested; a retry MUST add a new attempt and MUST keep the earlier one.
An attempt recorded as started without a terminal record MUST be closed as
`canceled` with a reason stating the interruption, and a new attempt MUST run.
An attempt that waits for an answer MUST be continued, not restarted. A panic of
a gate MUST NOT be converted into an outcome.

#### Scenario: Restart after a completed gate

- **КОГДА** the controller restarts after the first group finished
- **ТОГДА** the gates of the first group MUST NOT be invoked again and the wave
  MUST continue with the next group

#### Scenario: Interrupted attempt

- **КОГДА** an attempt has a start record and no finish record at restart
- **ТОГДА** it MUST be recorded as `canceled` and a new attempt MUST run

### Requirement: Gate outcomes drive the legacy stage-check flow

The controller MUST project a finished wave onto the stage-check records so that
stages keep their behavior: a failed or errored required gate MUST block the
transition and raise a `check_decision` request with the report of the wave and
the kind of the gates that blocked (`hard` for code gates, `agent` for LLM
gates); `check_started`, `check_passed`, `check_failed`, and `check_error` MUST
carry the hash of the checked result; a recorded check for another hash MUST be
ignored. A stage whose chain has no automatic gate MUST run no check.

#### Scenario: Failed code gate blocks the transition

- **КОГДА** a required code gate returns `fail`
- **ТОГДА** the transition MUST be blocked pending a `check_decision` and the
  report MUST list the gates that did not start
