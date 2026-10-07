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
allowed result operation. It MUST reject a result with an unknown schema, wrong
job/run/action, expired or replayed capability, invalid nonce, oversized output,
path traversal, or forged human decision. Worker output MUST NOT itself count as
proof of a human decision or of an independently executed check.

#### Scenario: Invalid or replayed worker result

- **WHEN** a worker submits a malformed, unauthorized, expired, replayed, or
  out-of-scope result
- **THEN** the controller MUST reject it without changing approvals, lifecycle,
  evidence, or delivery state

### Requirement: Network separation from administrative control API

The worker runtime MUST NOT have a network route to administrative control-plane
endpoints. The controller MUST persist validated job results and human decisions
without an API credential available to the worker.

#### Scenario: Worker calls an administrative endpoint

- **WHEN** a worker attempts an authenticated or unauthenticated request to an
  administrative control-plane endpoint
- **THEN** the runtime network policy MUST deny the connection
- **AND** browser HTTPS ingress MUST remain available to authorized team members
