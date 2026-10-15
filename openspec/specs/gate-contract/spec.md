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
`link_kind: pr`), `required_phrases` (`md` results; see the requirement below),
the LLM gate `agent_review` (parameters `agent`, `mode`,
`max_rounds`), the human gate `human_approval`, and the external-program gate
`external_program` (see the external-gate capability). Each version-1 gate MUST keep
the semantics of its schema v5 counterpart. The library MUST also publish
`agent_review@2` (adds `verdict_schema` and `max_attempts`) and
`human_approval@2` (adds `on_reject`) next to the unchanged version 1 of both,
so pinned templates keep their definitions.

#### Scenario: Gate applicability

- **КОГДА** `project_checks@1` is referenced from a stage with `link_kind: build`
- **ТОГДА** validation MUST reject the reference naming the required link kind

#### Scenario: Every code gate runs through the scheduler

- **КОГДА** a stage lists any of `required_sections`, `verdict_marker`,
  `required_phrases`, `pr_exists`, `pr_open`, `pr_base_branch`,
  `project_checks`, `pr_checks_green`
- **ТОГДА** the controller MUST execute it as a gate attempt of the group
  scheduler with separate `execution` and `verdict` in the run journal
- **И** a lookup or tool failure MUST be `error`, a negative finding MUST be
  `fail`, and neither MUST be recorded as `pass`

#### Scenario: Specification without acceptance criteria is blocked before LLM and human

- **КОГДА** `product_spec` lists `[required_sections]`, `[agent_review]`,
  `[human_approval]` and the result lacks the section `Критерии приёмки`
- **ТОГДА** the code gate MUST fail, the LLM gate and the human decision MUST
  NOT start
- **И** after the section is added the new result version MUST run all groups
  again from the first

### Requirement: required_phrases checks explicit phrases without scripts

`required_phrases@1` MUST check that configured words or fragments are present
in an `md` result. The parameters `phrases`, `case` (`sensitive|insensitive`),
`scope` (`result|section`) and `match` (`all|any|at_least`) MUST be required;
`section` MUST be required exactly for `scope: section` and `min_count` exactly
for `match: at_least` (1 to the number of phrases); `unit` (`substring|word`)
defaults to `substring`. A section scope MUST cover the heading's body up to
the next heading of the same or a higher level. A heading MUST be an ATX
heading (1 to 6 `#` followed by a space or the end of the line, indented by at
most 3 spaces) or a setext heading (a paragraph underlined by `=` or `-`); lines
inside fenced code blocks, indented code, and HTML comments MUST NOT be
headings. A closing fence MUST use the same character, be no shorter than the
opening fence, and carry no info string. `case: insensitive` MUST use simple
Unicode case folding (sigma, final sigma and capital sigma are equal). The gate MUST NOT execute user-supplied code. Regular
expressions MUST be available only with `regex: true`, MUST be Go RE2, bounded
in count and length, MUST NOT allow a zero-length match (such as `\b`, `^`, `$`, `a|\b`; checked on probe strings during validation and ignored at execution), and MUST be compiled
during configuration validation. An invalid parameter or combination MUST be
rejected before a run starts. An empty result or a missing section MUST be
`fail`; an unreadable, oversized or absent Markdown output MUST be `error`; the
gate MUST NOT return `not_applicable`.

#### Scenario: Missing phrase fails with a reason

- **КОГДА** `match: all` and one phrase is absent
- **ТОГДА** the attempt MUST be `succeeded` with verdict `fail` and a reason
  naming exactly the absent phrases

#### Scenario: Invalid parameters are a configuration error

- **КОГДА** `case` is omitted, `min_count` exceeds the number of phrases, or a
  regular expression does not compile as RE2
- **ТОГДА** configuration validation MUST fail naming the stage, the gate path
  and the parameter, and MUST NOT repeat the phrase value

#### Scenario: Unreadable result is an error

- **КОГДА** the Markdown output of the result cannot be read
- **ТОГДА** the attempt MUST be `error`, never `pass`

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
several LLM gates (a stage MAY have at most one `agent_review` in mode `grill`);
a `human_approval` gate MUST be the transition approval of the stage, MUST sit
in the last group and therefore MUST NOT precede an automatic group. A
`human_approval` gate that shares the last group with automatic gates MUST wait
for them: the person is asked only after every automatic gate ended without a
blocking outcome. The next group MUST
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

### Requirement: A wave is bound to one result version and an edit voids it

A wave MUST be bound to one result version: the SHA-256 of the declared outputs
of the checked stage attempt. The controller does not physically forbid writes to
the result files (other processes own them); the binding is detective and
fail-closed. The controller MUST verify before the wave, after
every gate, and at the end of the wave that the hash did not change. A change MUST
be detected and MUST void the wave: no outcome of it MAY be used, no following group MAY start, and a
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

### Requirement: LLM gate has a fixed verdict contract independent of grill

An `agent_review` gate MUST call the reviewer agent with fixed inputs (the
agent's declared inputs plus the outputs of the checked stage) and MUST read the
answer by the configured `verdict_schema`: `verdict_marker` (default, a
`Verdict: PASSED|FAILED` line outside code blocks and quotes) or `json` (one
object `{"verdict":"pass|fail","reason":"...","findings":[{"severity","message"}]}`
either as the whole report or as the only fenced `json` block; unknown fields,
an unknown verdict or severity, a `fail` without reason or findings, and a
`pass` with a `blocker` finding MUST violate the schema). An answer that does
not match the schema and a refusal or failure of the runtime MUST end the
attempt with `execution: error` and no verdict; they MUST NOT become `pass` or
`fail`. The gate MUST call the reviewer up to `max_attempts` times (default 1,
at most 5) until a valid verdict arrives, MUST keep every attempt of the call in
the reason of the gate attempt, MUST NOT retry a valid negative verdict, and
MUST stop at once when the context is canceled. `verdict_schema` and
`max_attempts` MUST NOT be accepted together with `mode: grill`; the grill mode
keeps its own question rounds, answers and new versions, and the contract of the
gate MUST NOT depend on it.

#### Scenario: Answer outside the schema

- **КОГДА** the reviewer answers with prose where the schema requires a verdict
- **ТОГДА** the gate attempt MUST end `error` without a verdict
- **И** the stage MUST be blocked with a `check_error`, not passed

#### Scenario: Retry recovers a technical failure only

- **КОГДА** the first call of a gate with `max_attempts: 2` fails and the
  second returns a valid `pass`
- **ТОГДА** the gate MUST pass and its reason MUST not hide the first failure
- **И** a valid `fail` on the first call MUST end the gate without a second call

### Requirement: Human gate records every wait and every decision

The `human_approval` gate MUST be recorded in the same attempt history as the
other gates: when the controller asks the person it MUST record a
`gate_attempt_waiting` attempt of the gate (executor `human`) bound to the hash
of the result version, and when the person decides it MUST finish that attempt
with the action, the role, the author and a bounded comment in the reason:
`approve` as `pass`, and `reject` and every `return_to_*` as `fail`. The record
MUST be written whether the decision is made in the running process or while the
process is stopped (Web, REST, MCP), and an identical redelivery MUST NOT add a
second decision. The gate definition `human_approval@2` MUST accept `on_reject`:
`stop` (default, the run ends) or `stay` (the task stays at the stage, the
refusal needs a reason, and the new version of the result MUST run the whole
chain again from the first group without consuming the visit allowance of the
stage). Allowed actions are those of the transition approval, the role is the
decision function of the stage and the quorum is `any`; the decision is bound to
the exact subject hash of the approval.

#### Scenario: Refusal keeps the task at the stage

- **КОГДА** a person rejects a result whose gate has `on_reject: stay`
- **ТОГДА** the human gate attempt MUST be finished with `fail` and the reason
  MUST name the action, the author and the comment
- **И** the stage MUST run again and the code, LLM and human gates MUST run again
  for the new version

#### Scenario: The person is asked last

- **КОГДА** an automatic gate of the chain fails or errors
- **ТОГДА** no transition approval and no human gate attempt MUST exist for that
  result version

### Requirement: One service path for human decisions

A decision on a stage transition MUST be made through one service
(`pkg/humandecision`) used by Web, REST and MCP. The service MUST, in order:
find a pending transition approval; accept only an action the approval offers;
require the subject hash of the request to equal the hash of the approval (a
request without hash or with a stale one MUST be refused); require that the
actor holds a required role that no earlier vote covered; refuse
self-approval when the pinned template forbids it; store the decision with the
artifact revisions pinned by the first vote; and write an audit record
`human_gate_decided` with the approval, stage, action, target, author, role,
hash, channel and the client's `Idempotency-Key` (the key is only written to
the audit and MUST NOT deduplicate anything). A redelivered identical request
is recognized by (author, action, subject hash, role) alone: it MUST NOT store
a second decision and MUST be audited as `human_gate_decision_replayed`, and it
MUST be answered only in a role the author still holds; a request in another
role whose seat somebody else closed MUST be refused, not reported as a replay. When the audit cannot be written after the
decision was stored, the caller MUST be told so and a repeat of the request
MUST be safe. A client that omits the hash on the Web confirmation endpoint
MAY be served with the hash of the pending approval, but this path is
DEPRECATED: the response MUST carry the `Deprecation: true` and `Warning`
headers and the audit MUST say that the person did not confirm the hash. The
network MCP server and every new client MUST send `subject_hash`; the bundled
frontend never confirms without it. A person who holds several required roles
votes once per role, each call in the next role no earlier vote covered (a
local owner closes a multi-role quorum by calling once per role); once every
role of that person is covered an identical request is a replay and a different
action MUST be refused with `ErrAlreadyVoted`. Of several racing identical
requests exactly one MUST write `human_gate_decided`, decided by the result of
the approval store. After a resume the visit counter MUST NOT count attempts
that ended with an `on_reject: stay` refusal, so any number of refusals decided
through Web or REST keeps the visit allowance.

#### Scenario: Web and REST give the same decision and audit

- **КОГДА** the same person decides the same approval through the task confirm
  endpoint and through the generic decision endpoint
- **ТОГДА** the stored decision and the audit record MUST be identical except
  for the channel

#### Scenario: Stale view

- **КОГДА** the result changed after the person opened the page and the request
  carries the old hash
- **ТОГДА** the decision MUST be refused with `subject_hash_mismatch` and MUST
  neither be stored nor audited

### Requirement: Advisory gates warn and never block

A gate reference with `mode: advisory` MUST run in its group like any other gate
and MUST be awaited with the whole group, but its outcome MUST NOT decide
whether the chain continues or the stage may transition. An advisory `fail`, and
an advisory technical failure (`error`, `timeout`, `canceled`), MUST stay a
visible warning: the attempt keeps its own execution and verdict (an error is
never reported as a success), the attempt events carry `mode: advisory` and
`advisory_warning`, the stage check event carries the list of `warnings`, and the
rules of the check report show the outcome as `advisory_failed` or
`advisory_error`. A group MAY mix required and advisory gates; the transition
MUST depend only on the required ones. Validation MUST reject `mode: advisory`
on `human_approval` (the decision of a person is the transition itself) and on
`agent_review` with mode `grill` (its question rounds must end before the chain
goes on).

#### Scenario: Advisory fail in a mixed group

- **КОГДА** a group has a required gate that passes and an advisory gate that
  returns `fail`
- **ТОГДА** the next group MUST start after both finished and the stage MUST be
  able to transition without a `check_decision`
- **И** the passed check MUST list the advisory gate with its `fail` verdict as a
  warning, and the advisory attempt MUST still be recorded as `fail`

#### Scenario: Advisory technical error

- **КОГДА** an advisory gate ends in `timeout`
- **ТОГДА** the transition MUST NOT be blocked
- **И** the warning MUST say `timeout` without a verdict

#### Scenario: Advisory human or grill gate

- **КОГДА** a template marks `human_approval` or a grill `agent_review` advisory
- **ТОГДА** validation MUST fail naming the gate path

### Requirement: Technically failed gates can be retried on the same version

A `check_decision` MUST offer the action `retry_gate` when a technical failure
of a required gate (latest attempt `error` or `timeout`) raised it, and MUST list the
retryable gates and the hash of the checked result in its payload; a
`check_decision` without such a gate MUST NOT offer it. Resolving it MUST name
the gates to retry; a gate whose latest attempt is not a technical failure MUST
be refused (a negative verdict is content, not a technical failure). The
controller MUST record `gate_retry_requested` bound to the hash, the approval,
the gates and the actor, and MUST run the chain again on the same result version
with exactly those gates retried: every retried gate MUST get a new attempt, the
earlier attempt MUST stay in the history, and gates that already succeeded,
including the other gates of the same group, MUST NOT run again. A request
recorded before a crash MUST be applied again on the next resume, and the stage
MUST NOT run again. A retry that fails technically again MUST raise its own
`check_decision`, even when the report is byte-identical. The retry MUST be
refused when the checked result changed (a changed result is a new version and
runs every group again). Web and REST expose it as
`POST /api/runs/{run}/stages/{stage}/gates/{gate}/retry` (optional body
`{"gates":[...],"reason":"..."}`) and
`POST /api/runs/{run}/gate-attempts/{attempt}/retry`; the permission is the one of
taking the stage.

#### Scenario: Retry keeps the successful gates

- **КОГДА** the first group passed and the LLM gate of the second group ended
  `error`, and the retry of that gate is requested
- **ТОГДА** only that gate MUST get a new attempt on the same hash, the passed
  gates MUST NOT run again and the stage agent MUST NOT run again
- **И** both attempts of the retried gate MUST be in the history

#### Scenario: A verdict is not retried

- **КОГДА** a required gate returned `fail`
- **ТОГДА** `retry_gate` MUST NOT be offered and a request naming that gate MUST be refused

### Requirement: Only a system administrator overrides a failed required gate

Moving a task on after a required gate failed MUST require the system administrator right
(`gate:override`, role `admin`; also for a technical failure nobody retried), checked on the server for every channel; the
`product_owner` function MUST NOT imply it, and the generic decision endpoint
MUST NOT be a way around it. A non-administrator MUST get `403` before any other
check, an administrator without a reason MUST get a validation error, and an
administrator with a reason MUST pass the task on. The override MUST be recorded
as an immutable `check_overridden` event with the author, the authority
`system_admin`, the time, the reason, the hash of the checked result, the stage
visit, the approval and the exact blocking attempts (gate, attempt ID, execution,
verdict, reason); it MUST NOT change any attempt: a gate that failed stays `fail`
in the attempts, the events, the API and the UI. The override MUST apply only to
the result version it names: a result with another hash MUST need a new override,
and an override made before versions were recorded MUST cover nothing.

#### Scenario: Product owner cannot override

- **КОГДА** a member who holds product_owner and every stage function, but not
  the administrator right, requests an override
- **ТОГДА** the response MUST be `403` and the controller MUST NOT be called

#### Scenario: Administrator needs a reason

- **КОГДА** an administrator requests an override without a reason
- **ТОГДА** the response MUST be a validation error and nothing MUST be recorded

#### Scenario: The outcome stays red

- **КОГДА** an administrator overrides a failed gate with a reason
- **ТОГДА** the task MUST transition
- **И** the failed attempt MUST still be `succeeded` with verdict `fail` in the
  attempt history and in the API, marked as overridden

#### Scenario: Another version is not covered

- **КОГДА** the result changes after an override and the new version fails
- **ТОГДА** the transition MUST be blocked until a new override of that version

### Requirement: The API tells whether the task may move on and why

`GET /api/runs/{run}/gates` (and the MCP tool `get_task_gates`) MUST return, for
every stage with a gate chain, the configured groups and every recorded result
version with its gates (instance, mode, group, effective attempt, number of
attempts, `blocking`, `warning`, `retryable`, `overridden`), the full attempt
history of the version, the override recorded for exactly that version and the
aggregated transition decision: `allowed`, a `state` (`passed`,
`passed_with_warnings`, `overridden`, `blocked`, `error`, `waiting`,
`incomplete`) and the reasons. The response MUST also list every override, the
advisory warnings of the current versions and whether the caller may override.
The decision MUST be a pure function of the recorded attempts and overrides of
that version: an older version MUST NOT resolve a newer one, and an override
MUST NOT hide the outcome of the gate it let pass. The UI MUST show an override
of a failure as a prominent red warning and an advisory outcome as an amber one
in the task card, the route and the history.

#### Scenario: Aggregated decision

- **КОГДА** a required gate failed and an advisory gate failed
- **ТОГДА** the decision MUST be `blocked` with the failed required gate as the
  reason and the advisory gate listed as a warning
