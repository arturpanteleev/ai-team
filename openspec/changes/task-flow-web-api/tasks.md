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
- [x] Add stage-level attested token usage, agent-acceptance metrics, and
  token-weighted subscription-share estimates with explicit provenance.
- [x] Document the current flow-metrics response and CSV attestation semantics.
- [x] Add execution/decision/outcome stage breakdowns, per-stage agent share,
  and per-stage subscription-share estimates from attested manifest tokens.
- [x] Attribute override, skip, and return actions from structured event stage
  fields and transition-selection evidence.
- [x] Include the full flow metric set in CSV and cover the 1000-run/300 ms
  target with a performance regression test.
- [ ] Update action request/response documentation after controller integration.
- [x] Run `openspec validate --all --strict --no-interactive`.
- [x] Run `GOTOOLCHAIN=go1.26.9 make verify` on the final commit.
