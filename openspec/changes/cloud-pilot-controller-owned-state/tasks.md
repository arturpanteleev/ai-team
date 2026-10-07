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
  per-invocation scoped loopback API; hostile API tests reject forged decisions,
  admin calls, and cross-run scope. The task stays open until OS-level filesystem
  isolation proves raw SQL/other filesystem access is denied.
- [ ] Split lifecycle, evidence, manifests, artifacts, and queue from
  worker-writable state; verify their integrity after worker-side attempts.
- [ ] Add replay, wrong-job, wrong-action, expiry, oversized result, malformed
  schema, path traversal, and forged-decision negative protocol tests.
- [ ] Test worker process/network policy on a real runtime and verify there is
  no route to admin control endpoints and no control-plane secrets in worker.
- [ ] Preserve run/approval/evidence through worker loss, controller restart,
  and backup/restore tests.
- [ ] Select one supported infrastructure; add TLS ingress, isolated worker,
  least-purpose credentials, persistent volumes, restart policy, backup/restore
  instructions, and an executable deployment smoke test.
- [ ] Enable strict containment only when the above checks demonstrate effective
  OS/filesystem/network isolation.
