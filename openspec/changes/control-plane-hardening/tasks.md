## 1. Safety and false-green

- [x] 1.1 Declare required verdict contracts per agent and fail on missing/invalid/multiple markers
- [x] 1.2 Introduce consistent execution/decision/outcome mapping for CLI, reports and store
- [x] 1.3 Add freshness checks and remove stale status/summary/output acceptance
- [x] 1.4 Capture git baseline and enforce per-stage mutation scopes without name heuristics
- [x] 1.5 Disable automatic delivery and require explicit policy approval
- [x] 1.6 Normalize target and workspace paths before filesystem/runtime operations
- [x] 1.7 Bind web to localhost and block symlink traversal/oversized artifact reads

## 2. OpenSpec and CI conformance

- [x] 2.1 Convert all main specs and active changes to strict-valid OpenSpec format
- [x] 2.2 Resolve accepted-spec vs implementation contradictions
- [x] 2.3 Add strict OpenSpec validation, frontend lint, race and coverage gates to CI
- [x] 2.4 Add requirement-to-test evidence traceability

## 3. Run and attempt evidence

- [x] 3.1 Introduce run ID, attempt ID and schema version
- [x] 3.2 Add immutable run layout for artifacts, logs, reports and events
- [x] 3.3 Add artifact manifest, SHA-256 provenance and atomic publication
- [x] 3.4 Invalidate downstream attempts on loopback/retry
- [x] 3.5 Add feature/workspace locking and concurrent-run tests

## 4. Workflow engine refactor

- [x] 4.1 Move domain types out of notifier/runtime packages
- [x] 4.2 Extract pure deterministic state machine
- [x] 4.3 Replace overlapping transition/gate mechanisms with explicit checkpoint policy
- [x] 4.4 Add strict versioned workflow/agent configuration
- [x] 4.5 Add built-in, project and user/plugin registry layers

## 5. Deterministic verification

- [x] 5.1 Implement command runner and evidence capture
- [x] 5.2 Add check classes and required/optional policies
- [x] 5.3 Separate test authoring from test execution
- [x] 5.4 Enforce read/write capability scopes for every stage

## 6. Delivery

- [x] 6.1 Add structured delivery plan schema and validation
- [x] 6.2 Add exact-file staging, branch protection and explicit approval
- [ ] 6.3 Complete crash-safe idempotent resume at every commit/persist/push/PR boundary (exact post-commit recovery is covered; exhaustive persist/effect fault matrix remains)
- [x] 6.4 Add partial-delivery E2E tests

## 7. Observability, evals and web

- [x] 7.1 Make append-only lifecycle events tamper-evident and sufficient for deterministic lifecycle/manifest replay
- [x] 7.2 Make reports and web run/attempt-aware
- [x] 7.3 Add reconciliation for interrupted runs and DB concurrency settings
- [x] 7.4 Add deterministic, behavioral, fault-injection and LLM quality eval suites
- [x] 7.5 Add web security, API pagination and frontend tests
- [ ] 7.6 Rebuild SQLite projections from ledger replay and reconcile side-effect state after crash

## 8. Verification

- [ ] 8.1 Unit tests for all new domain, policy, manifest and executor code
- [ ] 8.2 E2E matrix for false-green, stale, retry, cancel, timeout and dirty workspace
- [x] 8.3 Race, coverage, lint, build, OpenSpec strict and dependency security checks
- [ ] 8.4 External senior review, self-review and Definition of Done evidence

## 9. Strict-profile release blockers

- [ ] 9.1 Add OS-level sandbox backend with filesystem, network, environment and resource policy
- [ ] 9.2 Execute agents and checks in an isolated immutable candidate worktree
- [ ] 9.3 Bind review, typed tests, verification, approvals and delivery to one candidate identity
- [ ] 9.4 Add exact per-checkpoint subject approval and immutable cross-run resume lineage
- [ ] 9.5 Add machine-readable AC-to-test-to-execution traceability
- [ ] 9.6 Add typed non-Go test adapters before claiming those stacks
- [ ] 9.7 Pin/probe agent runtime identity and record it per attempt
- [ ] 9.8 Add a durable terminal ledger root plus signature or external append-only anchor
