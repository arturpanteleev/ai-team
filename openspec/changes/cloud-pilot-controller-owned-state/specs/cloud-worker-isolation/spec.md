## ADDED Requirements

### Requirement: Replay-resistant per-invocation controller API

Every authenticated worker controller API request MUST carry a cryptographically
random 256-bit nonce and an issue timestamp. The server MUST reject a missing or
malformed nonce, a missing timestamp, a request older than 30 seconds, a request
more than 5 seconds in the future, and any nonce already accepted during that
invocation. The server MUST enforce the guard before dispatch so rejected calls
cannot change recorder or approval state. Replay state MUST be bounded to at
most 4096 active nonces per invocation; once full, requests MUST fail closed
until expired entries are reclaimed.

#### Scenario: Worker replays a controller API request

- **WHEN** a worker submits the same authenticated request and nonce twice
- **THEN** the controller MUST accept at most the first request
- **AND** the replay MUST NOT duplicate recorder events or approval writes

#### Scenario: Worker submits a stale or invalidly timed request

- **WHEN** a worker omits the nonce or timestamp, submits an expired request,
  or claims an issue time beyond the permitted future skew
- **THEN** the controller MUST reject it before dispatch
- **AND** controller-owned recorder and approval state MUST remain unchanged

#### Scenario: Worker exhausts per-invocation replay state

- **WHEN** an invocation has 4096 unexpired accepted nonces
- **THEN** the controller MUST reject additional requests until expired entries
  can be reclaimed
- **AND** its replay-state memory MUST remain bounded

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
output, path traversal, or forged human decision. A result MAY contain
worker-reported check claims, but the controller MUST label them as claims and
MUST NOT count them as passed checks unless the controller or a separately
trusted verifier independently ran the check against the exact accepted bytes.
Worker output MUST NOT itself count as proof of a human decision, artifact
integrity, or an independently executed check.

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

#### Scenario: Worker reports a passing check without trusted verification

- **WHEN** a worker returns a check claim such as `tests_passed` or a digest for
  a candidate artifact
- **THEN** the controller MUST record the claim as worker-reported only
- **AND** it MUST NOT mark the check as verified unless a trusted verifier ran
  it against the exact bytes and the controller recorded verifier identity,
  version, and result

#### Scenario: Controller stages and commits candidate artifacts

- **WHEN** a result includes candidate artifact bytes
- **THEN** the controller MUST accept them only through bounded transfer into
  controller-owned staging under allowlisted logical names/types
- **AND** it MUST calculate the digest from received bytes and commit to
  authoritative evidence only after validation
- **AND** malformed, partial, oversized, or failed validation MUST leave
  authoritative state unchanged
- **AND** the commit MUST be idempotent for the same accepted result

#### Scenario: Worker attempts to supply a human decision

- **WHEN** a result, artifact, check claim, or job capability contains an
  approve/reject/resolve decision
- **THEN** the controller MUST reject or ignore that field and MUST NOT change
  approval state
- **AND** only an authenticated, authorized human action through the controller
  may create the decision and its audit record

### Requirement: Network separation from administrative control API

The worker runtime MUST NOT have a network route to administrative control-plane
endpoints. The controller MUST persist validated job results and human decisions
without an API credential available to the worker.

This requirement is not satisfied by the current subprocess launcher, filtered
environment, application-level approval adapter, a container declaration, or
`execution_id` correlation alone. Before enabling strict/cloud-pilot operation,
the supported runtime MUST demonstrate OS-enforced separation: worker has no
controller DB/evidence/approval mounts or writable path, runs under a distinct
least-privilege identity, has a read-only base filesystem plus bounded disposable
scratch, has no host/container runtime socket or control-plane secret, and has
network policy that denies admin endpoints while allowing only required
provider egress. The controller must own persistent lifecycle/evidence/queue
writes and must validate the runtime policy in an executed deployment smoke test.

#### Scenario: Worker calls an administrative endpoint

- **WHEN** a worker attempts an authenticated or unauthenticated request to an
  administrative control-plane endpoint
- **THEN** the runtime network policy MUST deny the connection
- **AND** browser HTTPS ingress MUST remain available to authorized team members
