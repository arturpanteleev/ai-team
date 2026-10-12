## ADDED Requirements

### Requirement: Declarative stage check

A template stage MUST be able to declare a `check`:
- `kind: hard` with a non-empty `rules` list (and stage `required_sections`);
- `kind: agent` with a registry-known checker agent.

Config validation MUST reject unknown kinds, hard checks carrying
agent/mode/max_rounds fields, empty rule sets, missing or unknown checker agents
and negative `max_rounds`. A `check` agent MUST be listed as a pipeline agent.

#### Scenario: Unknown check kind

- **КОГДА** stage declares `check.kind: manual`
- **ТОГДА** config validation MUST reject the template

#### Scenario: Missing checker agent

- **КОГДА** stage declares `check.kind: agent` without a registry agent
- **ТОГДА** config validation MUST reject the template

### Requirement: Check runs before the transition

The pipeline MUST run the stage check after an attempt passes and before the
edge to the next stage is confirmed, except for config-skipped stages and
`check.mode: grill`. The check MUST NOT depend on whether the attempt was
replayed: replay MUST decide from durable evidence, where a durable
`check_passed` skips re-evaluation, a durable `check_failed` or `check_error`
blocks through the same `check_decision` request, and an attempt with no
completed durable check (including a replayed human attempt interrupted after
`attempt_finished` but before the check) MUST run the check again. Re-running
MUST be idempotent and MUST fail closed on ambiguous evidence.

The pipeline MUST append `check_started` before evaluation and
`check_passed`/`check_failed`/`check_error` after, with the report path, its
SHA-256 and the status.

#### Scenario: Passed check

- **КОГДА** all hard rules pass (or the checker agent reports a passing verdict)
- **ТОГДА** the pipeline MUST append `check_passed`
- **И** MUST allow the transition to proceed

#### Scenario: Grill mode is deferred

- **КОГДА** stage declares `check.mode: grill`
- **ТОГДА** B-40 mechanism MUST NOT run the check (handled by B-42)

### Requirement: Advisory grill round lifecycle

For `check.kind: agent` with `check.mode: grill`, the checker MUST return
exactly one `**Question:**` marker or a passing `**Verdict:** PASSED`. A
malformed response or checker execution error MUST fail closed through a
human `check_decision`. A question MUST create a same-stage approval with
answer, new-version, and done actions. An answer MUST be passed to the next
checker round as untrusted context. A new-version action MUST pin an immutable
human result revision whose content SHA-256 differs from the pinned baseline;
a different revision ID with identical bytes or a comment-only revision MUST
not resolve the question. The revision MUST satisfy the SHA-256-verified pinned
stage's typed-result and required-section contract before append, and the next
checker round MUST read that exact revision. Done and `max_rounds` MUST complete
the advisory grill without bypassing the stage's normal confirmation.
`grill_completed` MUST record the round count, open-question count, and
completion reason. A resolved grill approval checkpoint MUST recover from
verified decision evidence after a process crash.

#### Scenario: Human edits the stage result

- **КОГДА** the responsible person submits a new immutable result revision for
  the current human stage whose content SHA-256 differs from the pinned
  baseline and chooses `new_version`
- **ТОГДА** the checker MUST receive the exact pinned stage-scoped revision
- **И** a later edit MUST NOT silently replace the pinned revision

#### Scenario: Comment-only edit does not create a new grill version

- **КОГДА** a new immutable revision ID has the same content SHA-256 as the
  pinned result
- **ТОГДА** `new_version` MUST be rejected and leave the grill approval pending

#### Scenario: Resume after a resolved grill decision

- **КОГДА** the process crashes after persisting the resolved grill approval
  as the active stage approval
- **ТОГДА** resume MUST verify the approval decision and source attempt from
  evidence, then continue the same checker round without losing the decision

### Requirement: Failed check blocks the stage

A `failed` or `error` check result MUST block the transition, persist the report,
append `check_failed` (or `check_error` for an execution failure), raise a
pending `check_decision` approval (`kind=check_decision`,
`trigger=stage_check`, actions `new_version`/`override`/`return` targeting the
same stage), append `approval_requested` and pause the run waiting for the
decision. The approval payload MUST carry the report path, its SHA-256, the
check kind/status and the failed rules.

#### Scenario: Verdict marker missing

- **КОГДА** a hard `verdict_marker` check finds no accepted verdict in the result
- **ТОГДА** the stage MUST be marked blocked
- **И** a pending `check_decision` approval MUST be created
- **И** the run MUST pause with `pending_approval_id` set

#### Scenario: Checker agent errors

- **КОГДА** the checker agent fails to produce a readable report
- **ТОГДА** the pipeline MUST append `check_error`
- **И** MUST NOT silently pass the stage

### Requirement: Check report is visible in the task

The pipeline MUST write the check report as a controller artifact at
`{artifacts}/{feature}/.stage-check/{stage}.md`, readable via
`report.ReadStageCheckReport`, and a copy in run evidence. A stage re-run MUST
renew the controller copy (AUD-04) and MUST version the evidence copy so
immutable evidence is preserved.

#### Scenario: Report survives re-run

- **КОГДА** a `new_version` decision re-runs the stage
- **ТОГДА** the new check report MUST be readable at the same task path
- **И** the previous evidence copy MUST remain available

### Requirement: Check decision binding

A `check_decision` approval MUST bind the failed stage check: payload `stage_id`
and the check route MUST equal the stage, the status MUST be `failed` or
`error`, the check kind MUST be `hard` or `agent`, and a `new_version` action
with a same-stage route MUST be present. Unsupported actions, invalid report
paths/hashes and inconsistent triggers MUST be rejected by the store.

#### Scenario: Override action is honored later

- **КОГДА** a human resolves `check_decision` with `override` targeting the stage
- **ТОГДА** the run MUST resume from that stage without re-running the check
  (routing semantics finalized in B-41)
