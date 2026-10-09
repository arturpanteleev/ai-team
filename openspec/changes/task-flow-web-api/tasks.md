## Implementation

- [x] Add durable SQLite request-key claims and exact response replay.
- [x] Add run-scoped event cursor reads.
- [x] Add JSON error normalization for task API routes.
- [x] Add templates, run list/detail, result-version, event, and flow-metrics
  routes.
- [x] Add focused read-route, auth, idempotency replay, and key-conflict tests.
- [ ] Connect all stage and delivery-plan actions to controller implementations
  from B-32/B-34/B-41/B-42.
- [ ] Add per-action success, invalid-state, repeated-request, and no-token
  regressions with controller-backed fixtures.
- [ ] Complete stage-level usage and agent-acceptance metrics.
- [ ] Update OpenSpec and API documentation for final request/response shapes.
- [ ] Run `openspec validate --all --strict --no-interactive`.
- [ ] Run `GOTOOLCHAIN=go1.26.9 make verify` on the final commit.
