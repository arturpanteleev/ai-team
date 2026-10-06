# MAJ-07 architecture gate tasks

- [ ] Agree the trust assumptions for worker-produced checks and artifacts.
- [ ] Specify a versioned, bounded, job-scoped worker result protocol and
  controller validation/commit semantics.
- [ ] Split human decision storage from worker-writable state; add tests proving
  worker cannot alter an existing decision, event log, manifest, artifact,
  lifecycle record, queue, or control database.
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
