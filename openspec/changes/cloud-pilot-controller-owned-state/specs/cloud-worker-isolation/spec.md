## ADDED Requirements

### Requirement: Controller-owned human decisions and authoritative evidence

The controller MUST be the only writer of human approval decisions and
authoritative run evidence. A worker MUST NOT receive writable access to those
stores and MUST NOT be able to use its job capability to create or resolve a
human decision.

The current worker approval adapter rejects decision writes through the normal
pipeline interface, but is only an application-level defense. The worker still
receives the shared database path and target filesystem access, so this does
not satisfy the requirement or prevent direct database writes.

#### Scenario: Worker attempts to alter an approval or evidence

- **WHEN** a compromised worker attempts to read a control-plane secret, alter
  or delete an existing approval, lifecycle record, evidence event, manifest,
  artifact, or control database
- **THEN** the filesystem/API boundary MUST deny the operation
- **AND** the original controller-owned state MUST still pass its integrity
  verification

### Requirement: Job-scoped worker result capability

The controller MUST issue a short-lived capability scoped to one active job and
allowed result operation. Each `ProcessEngine` child invocation MUST receive a
fresh bounded `execution_id`, and the result MUST echo that exact identity. The
controller MUST reject a result with an unknown schema, wrong job/run/action or
execution identity, expired or replayed capability, invalid nonce, oversized
output, path traversal, or forged human decision. Worker output MUST NOT itself
count as proof of a human decision or of an independently executed check.

The `execution_id` binds a result to one launched invocation and detects stale
or cross-invocation output. It does not prove worker honesty, prevent the worker
from editing accessible files, or create process isolation. Durable queue
records are logical jobs: schema 1 records remain loadable and are upgraded to
the current invocation schema with a newly generated identity only when
`ProcessEngine` launches them.

#### Scenario: Invalid or replayed worker result

- **WHEN** a worker submits a malformed, unauthorized, expired, replayed, or
  out-of-scope result
- **THEN** the controller MUST reject it without changing approvals, lifecycle,
  evidence, or delivery state

#### Scenario: Result belongs to another or previous process invocation

- **WHEN** a worker returns a result whose `execution_id` differs from the
  freshly generated identity for this invocation, including a valid result
  replayed from an earlier invocation of the same run and operation
- **THEN** the controller MUST reject it as an infrastructure failure
- **AND** it MUST NOT classify the stale result's business outcome as the
  outcome of the current invocation

### Requirement: Network separation from administrative control API

The worker runtime MUST NOT have a network route to administrative control-plane
endpoints. The controller MUST persist validated job results and human decisions
without an API credential available to the worker.

#### Scenario: Worker calls an administrative endpoint

- **WHEN** a worker attempts an authenticated or unauthenticated request to an
  administrative control-plane endpoint
- **THEN** the runtime network policy MUST deny the connection
- **AND** browser HTTPS ingress MUST remain available to authorized team members
