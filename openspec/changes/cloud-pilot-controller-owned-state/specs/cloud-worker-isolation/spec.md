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

The worker approval adapter rejects decision writes through the normal
pipeline interface. Lifecycle create/load/save now use a run/target-scoped
controller API for ordinary web/scheduler worker execution, while local CLI
runs retain the filesystem store. These are application-level controls only:
the child does not receive `--db`, but it still has the same OS identity and
target filesystem access, so it may open known database paths or alter
lifecycle/evidence/artifact files directly. This does not satisfy the OS/API
boundary requirement.

#### Scenario: Worker attempts to alter an approval or evidence

- **WHEN** a compromised worker attempts to read a control-plane secret, alter
  or delete an existing approval, lifecycle record, evidence event, manifest,
  artifact, or control database
- **THEN** the filesystem/API boundary MUST deny the operation
- **AND** the original controller-owned state MUST still pass its integrity
  verification

This destructive scenario remains unverified and unsatisfied until the worker
runs under an enforced OS/filesystem boundary; routing normal pipeline lifecycle
calls through the controller API alone does not prevent direct file access.

### Requirement: Controller-owned lifecycle checkpoint port

For ordinary disposable worker execution, the pipeline MUST request lifecycle
create/load/save operations through a typed controller API scoped to the active
run and target. The controller MUST reject a request for a different run or
target, and MUST serialize checkpoint operations with other stateful calls for
the invocation. The API MUST use the existing bounded strict request schema and
nonce/expiry/replay guard. Local CLI runs MAY continue using the filesystem
lifecycle store. This application-level routing MUST NOT be described as
filesystem isolation: the worker still shares the OS identity and target
filesystem until a separately verified runtime boundary is deployed.

#### Scenario: Worker creates, resumes and advances its checkpoint

- **WHEN** a disposable worker starts a run, loads it on resume, and saves the
  next checkpoint
- **THEN** each operation MUST be scoped to the same run and target and reach
  the controller-owned lifecycle store
- **AND** the saved phase and approval/stage checkpoint MUST survive a fresh
  load through that store
- **AND** the local CLI MUST continue to create and update its filesystem store

#### Scenario: Worker requests lifecycle state outside its scope

- **WHEN** a worker requests another run, supplies a mismatched target, or
  attempts an unsupported lifecycle method
- **THEN** the controller MUST reject the request without changing lifecycle
  state

### Requirement: Run-scoped controller-owned business brief API

For disposable worker execution, the pipeline MUST create, append, list, and
read durable business-brief versions through typed controller API operations
scoped to the invocation's run. Requests MUST NOT accept a target path or an
arbitrary version path; reads MUST resolve a version ID from that run's store.
The controller MUST bind initial-brief creation to the immutable task in the
start job, or to the persisted lifecycle task during resume/recovery, and MUST
reject a worker-supplied initial intention that differs from that value.
The controller MUST preserve immutable version identity and return only the
requested run's brief content. The worker MAY materialize returned bytes in
per-invocation staging under `{target}/.ai-team/artifacts` for runtime inputs.
This staging directory is writable and visible to the worker and is not a
security boundary; abrupt termination MAY leave temporary `.brief-*`
directories behind. Local CLI runs MAY continue to use the filesystem-backed
implementation of the same typed store contract.
This application/API ownership boundary MUST NOT be described as OS
inaccessibility: the target remains mounted to the worker, and a compromised
worker can still read or alter the controller's on-target brief files directly.

#### Scenario: Worker creates and resumes a clarified business brief

- **WHEN** a worker creates an initial brief, records a human clarification,
  exits, and later resumes the same run
- **THEN** the controller MUST preserve both immutable versions and their
  parent/hash identity
- **AND** the resumed worker MUST receive the latest content through the
  run-scoped API without supplying a filesystem path

#### Scenario: Worker requests a brief outside its run

- **WHEN** a worker requests another run's brief or supplies a filesystem path
  in place of a version ID
- **THEN** the controller MUST reject the cross-run request or return no
  matching version
- **AND** no other run's brief content may be returned

### Requirement: Run-scoped controller-owned candidate metadata API

Disposable worker pipelines MUST create and load candidate metadata through
typed controller API operations scoped to the invocation's run and target.
The controller MUST validate schema, run identity, canonical target, and the
exact candidate worktree path before persistence and before returning metadata.
Conflicting existing metadata and symlinked candidate worktree/metadata paths
MUST be rejected. Symlinked parent components of the target path are accepted
and resolved to the same canonical target; the candidate manager requires the
final target component itself to be a directory, not a symlink.
Resume MUST fail closed when candidate metadata is missing; worker-writable
run provenance MUST NOT establish that a run had no candidate. Until a
controller-owned absence marker exists, non-Git runs without candidate
metadata cannot resume in controller-backed mode. Local CLI MAY retain legacy
non-Git resume compatibility using target-file provenance, which is not a
trust boundary.
Local CLI runs MAY use the filesystem-backed store. In the opt-in Linux
bubblewrap mode, `.ai-team/state/candidates` MUST be overlaid with a
namespace-local tmpfs so controller metadata is not readable by the worker;
the candidate worktree remains mounted and available for normal pipeline work.
This requirement covers candidate metadata only and MUST NOT be represented as
isolation of candidate contents, evidence, or artifacts.

#### Scenario: Worker creates and resumes the same candidate

- **WHEN** a disposable worker creates candidate metadata and later resumes or
  recovers that run
- **THEN** create and read operations MUST reach the controller store with the
  same run/target identity
- **AND** the returned worktree MUST match the exact target/run candidate path
- **AND** a local CLI run MUST retain its filesystem-backed behavior

#### Scenario: Missing metadata cannot be bypassed with run provenance

- **WHEN** candidate metadata is missing and worker-writable `run.json` claims
  `candidate=sha256/unknown`
- **THEN** resume MUST fail closed
- **AND** non-Git runs without a controller-owned candidate record MUST NOT
  resume in controller-backed mode

#### Scenario: Candidate metadata is masked while worktree remains available

- **WHEN** a Linux bubblewrap worker attempts to read a controller-owned
  candidate-metadata sentinel and a separate candidate-worktree sentinel
- **THEN** the metadata sentinel MUST be unreadable
- **AND** the worktree sentinel MUST remain readable

### Requirement: Opt-in Linux controller-state filesystem masking

The system MUST wrap a child in bubblewrap mount, user, PID, IPC, UTS, and network
namespaces when `AI_TEAM_WORKER_SANDBOX=bubblewrap` is set for a web or
scheduler worker. The launcher MUST canonicalize the writable target after
resolving symlinks and MUST reject it if it resolves to the filesystem root;
otherwise the writable target bind would make the entire host filesystem
writable inside the sandbox. The worker MUST see the target workspace as
writable, while the configured controller SQLite DB and its WAL/SHM/journal
paths, plus `.ai-team/state/runs` and `.ai-team/state/approvals`, MUST be masked
from direct reads. The launcher MUST
canonicalize the configured DB and existing sidecar paths and fail closed if
any existing DB or sidecar is not a regular file or
has more than one hard link; otherwise an unmasked alias could expose the same
contents. Files below the private lifecycle and legacy-approval directories
MUST also be regular files with no hard-link aliases; symlinks and special
files MUST fail closed. Missing bubblewrap or a failed sandbox launch MUST fail
the invocation; the launcher MUST NOT retry the worker without the sandbox.
The Linux host MUST allow bubblewrap to create the required unprivileged user
namespace; package presence alone is not sufficient. If host policy denies
namespace setup, the invocation MUST fail closed. The feature is opt-in and
Linux-only.

In this mode, the worker MUST NOT share host TCP loopback or outbound TCP
connectivity. The per-invocation scoped controller API MUST remain available
only through a mode-0600 Unix-domain socket under the worker's private
temporary directory. The socket MUST be removed when the invocation ends.
The launcher MUST overlay `/run` with a private tmpfs so standard host pathname
service sockets under `/run` are not exposed. Host Unix sockets at nonstandard
paths outside `/run` remain a documented residual risk and MUST NOT be described
as isolated by this slice. Before adding later mounts, the launcher MUST reject
a canonical writable target, HOME/TMPDIR, or agent-registry source/destination
that overlaps `/run` in either direction, including symlink aliases. This
rejects paths equal to or beneath `/run` and paths that contain `/run` (such as
`/`); otherwise a later bind could reveal host `/run` contents over the tmpfs. This network
namespace has no external IP egress;
remote model calls, including OpenAI/OpenCode providers, require a separately
configured allowlisted egress proxy, which this slice does not provide.

This slice does not isolate `.ai-team/runs` evidence/manifests, candidate or
other target artifacts, agent registry files, or host files other than the
configured DB paths. In particular, the read-only `/` bind
still exposes other host paths. The worker `HOME` is a fresh per-invocation
temporary directory, not the operator's home, although the operator's original
home may still be reachable by its absolute host path through the `/` bind,
subject to host permissions. This is not general host-secret isolation. The
target remains writable. It MUST NOT
be recorded as an `ENFORCED` containment receipt or represented as full cloud
worker isolation.

#### Scenario: Sandboxed worker accesses its workspace and controller API

- **WHEN** a bubblewrap worker reads/writes a target file and tries to read the
  configured controller DB, WAL, SHM, rollback-journal, or lifecycle sentinel
- **THEN** target access MUST continue to work
- **AND** the controller DB bytes and lifecycle state MUST not be readable
- **AND** the scoped controller API MUST remain callable over its Unix socket
- **AND** TCP connections to host loopback and an external endpoint MUST fail
- **AND** standard host pathname sockets under `/run` MUST not be visible

#### Scenario: Remote model provider is configured without an egress proxy

- **WHEN** an operator selects this opt-in mode with a remote model provider
  and no separately configured allowlisted egress proxy
- **THEN** provider network calls MUST fail in the isolated network namespace
- **AND** documentation MUST state that remote model calls require that proxy

#### Scenario: Controller API Unix socket setup fails

- **WHEN** the controller cannot create or secure the per-invocation Unix socket
- **THEN** the worker invocation MUST fail before the child starts
- **AND** it MUST NOT fall back to a shared-network TCP API or unsandboxed launch

#### Scenario: A protected database path has a hard-link alias

- **WHEN** the configured canonical DB or any existing canonical SQLite
  sidecar has more than one hard link
- **THEN** the launcher MUST fail before starting the child
- **AND** it MUST NOT launch the worker unsandboxed

#### Scenario: A private state file has a hard-link alias

- **WHEN** a file under a masked lifecycle or legacy-approval directory has
  more than one hard link
- **THEN** the launcher MUST fail before starting the child
- **AND** it MUST NOT launch the worker unsandboxed

#### Scenario: Writable target resolves to the filesystem root

- **WHEN** the configured target resolves to `/` after absolute-path and
  symlink canonicalization
- **THEN** the launcher MUST reject it before spawning bubblewrap or the worker
- **AND** it MUST NOT run the worker with a writable bind of the host root

#### Scenario: Bubblewrap runtime is unavailable

- **WHEN** bubblewrap is selected but missing or cannot create its namespaces
- **THEN** the worker invocation MUST fail
- **AND** the worker MUST NOT execute unsandboxed

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
