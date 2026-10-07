# MAJ-07 architecture gate tasks

- [x] Agree the trust assumptions for worker-produced checks and artifacts:
  worker output and artifacts are untrusted claims/bytes; digests bind bytes but
  do not establish correctness; checks count as verified only after an
  independent trusted verifier runs against the accepted bytes; the controller
  owns staging, validation, authoritative commit, and human decisions. See
  `design.md` and `specs/cloud-worker-isolation/spec.md`.
- [ ] Specify a versioned, bounded, job-scoped worker result protocol and
  implement controller validation/commit semantics (capability, nonce,
  allowlisted artifact transfer, independently verified check provenance, and
  atomic/idempotent commit). The design constraints are now documented; the
  protocol/API and its implementation/tests remain outstanding.
- [x] Add an application-level worker approval adapter that rejects the
  pipeline's `Decide` and `ResolveDeferred` calls; verify the row remains
  pending and an authenticated controller route can resolve it (#198). This
  is defense in depth only and does not prove process isolation.
- [x] Bind each `ProcessEngine` invocation and result to a fresh bounded
  `execution_id`; reject wrong or replayed invocation identities, and safely
  upgrade schema 1 durable queue jobs at spawn. This is correlation/replay
  hardening only; it does not prove worker honesty or process isolation.
- [x] Filter disposable worker environment inheritance: retain the documented
  runtime baseline, pass provider/runtime variables only by explicit
  `AI_TEAM_WORKER_ENV_ALLOW`, use per-invocation HOME/TMPDIR/XDG locations,
  and test that controller auth/signing/database variables are absent by
  default. This reduces accidental environment exposure; it is not OS
  isolation and does not prevent same-user filesystem access.
- [ ] Remove worker access to the controller DB path and replace direct store
  access with a controller-owned API; verify a worker cannot mutate approvals
  through raw SQL or other filesystem access. The supported web/scheduler
  launcher slice now omits `--db` and relays recorder/approval calls over a
  per-invocation scoped controller API; hostile API tests reject forged decisions,
  admin calls, and cross-run scope. An opt-in Linux bubblewrap slice now masks
  the configured SQLite database and lifecycle/legacy-approval directories;
  its integration probe verifies controller DB/lifecycle sentinels are unreadable
  while the workspace remains writable. Bubblewrap workers now use a private
  Unix socket for the scoped API in an isolated network namespace; `/run` is
  overlaid with tmpfs to hide standard host service sockets there, while custom
  pathname sockets outside `/run` remain a residual risk. A command-plan test
  checks the mount and a failure-path test checks that inability to create the
  private API socket cannot fall back to TCP or an unsandboxed worker.
  Integration probes check API reachability and denied host/outbound TCP. The
  network namespace also blocks remote model-provider calls; OpenAI/OpenCode
  need a separately configured allowlisted egress proxy, which this slice does
  not provide. The task stays open for independent approval-store placement,
  evidence/artifact isolation and runtime/recovery acceptance.
- [ ] Split lifecycle, evidence, manifests, artifacts, and queue from
  worker-writable state; verify their integrity after worker-side attempts.
  The evidence part is blocked on replacing the current filesystem-shaped
  `pipeline.EvidenceStore` contract. The inventory below is intentionally
  non-exhaustive and must be refreshed by tracing current call sites before the
  boundary is declared complete: `pipeline.RunEngine.Start` calls
  `Pipeline.RunWithResult` (also exposed through `Pipeline.Run`), while resume
  also enters `RunWithResult`; these flows create and verify evidence, replay
  events, read attempt manifests, and use `RunDir`/`LogDir`. Other direct file
  flows include immutable initial and versioned business briefs, durable
  question answers and answer inputs, human return-feedback inputs, cancellation
  request markers and recovery, and delivery verification/recovery (prepared
  workspace digests, attestations, and terminal delivery records). Stage,
  finalize, reporting, usage, containment, candidate/artifact publication, and
  runtime logs also depend on filesystem paths. Do not mask
  `.ai-team/runs/<run_id>` with tmpfs before a controller-owned typed store can
  preserve start/resume, cancellation/recovery, brief and human-input history,
  event append, attempt/artifact/log publication, and delivery verification
  semantics. A real Linux sentinel probe is required after that store boundary
  exists; a mount-plan-only test is not evidence of isolation.
- [x] Add a negative test matrix for the currently implemented worker result
  and controller API contracts: wrong run/operation/execution identity,
  replayed result and API request, expired/future/malformed API nonce,
  oversized/malformed requests and results, path traversal fields, cross-run
  scope, forbidden methods, and forged human decisions. Concurrent replay is
  covered by asserting exactly one accepted request and approval write. These
  tests validate the current application protocol only; they do not complete
  the capability, typed artifact transfer/commit, trusted check provenance,
  or OS/runtime isolation requirements above and below.
- [ ] Test worker process/network policy on a real runtime and verify there is
  no route to admin control endpoints and no control-plane secrets in worker.
- [ ] Preserve run/approval/evidence through worker loss, controller restart,
  and backup/restore tests.
- [ ] Select one supported infrastructure; add TLS ingress, isolated worker,
  least-purpose credentials, persistent volumes, restart policy, backup/restore
  instructions, and an executable deployment smoke test.
- [ ] Enable strict containment only when the above checks demonstrate effective
  OS/filesystem/network isolation.
