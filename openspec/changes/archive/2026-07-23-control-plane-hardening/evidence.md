# Verification evidence

Таблица связывает требования change с исполняемыми проверками. Статус
`implemented` означает наличие теста, но не заменяет итоговый зелёный прогон
baseline-команд ниже.

| Capability / requirement | Evidence | Status |
|---|---|---|
| control-plane-safety / Mandatory verdict contract | `pkg/verdict/verdict_test.go`; `pkg/pipeline/pipeline_test.go` — missing, invalid, multiple markers | implemented |
| control-plane-safety / Consistent outcomes | `pkg/workflow/state_test.go`; `pkg/pipeline/pipeline_test.go`; `pkg/report/generator_test.go` | implemented |
| control-plane-safety / Freshness and stale cleanup | `pkg/pipeline/pipeline_test.go` — stale output/status/summary | implemented |
| control-plane-safety / Explicit approvals | `pkg/pipeline/pipeline_test.go` — gate vs delivery approval and preconditions | implemented |
| control-plane-safety / Mutation boundaries | `pkg/scope/path_test.go`; `pkg/pipeline/pipeline_test.go` — exact scopes, pre-existing dirt, read-only, non-Git snapshot | partial: detective guard; isolated candidate pending |
| run-evidence / Identity and immutable layout | `pkg/evidence/store_test.go`; `pkg/pipeline/pipeline_test.go` | implemented |
| run-evidence / Hash provenance and atomic publication | `pkg/evidence/store_test.go` — SHA-256, symlink refusal, collision/atomic publish | implemented |
| run-evidence / Invalidation and locking | `pkg/evidence/store_test.go`; `pkg/pipeline/pipeline_test.go` — loopback attempts and workspace contention | implemented |
| workflow-engine / Pure state model | `pkg/workflow/state_test.go`, `pkg/workflow/identity_test.go` | implemented |
| workflow-engine / Strict configuration | `pkg/config/config_test.go`; `pkg/agent/registry_test.go` — schemas, duplicate/unknown fields, exact loopback, layered fail-closed override | implemented |
| workflow-engine / Explicit non-interactive policy | `pkg/pipeline/pipeline_test.go` — missing pre-authorization stops | implemented |
| deterministic-checks / Runner evidence | `pkg/checks/checks_test.go` — argv, timeout, output bounds, tool/version, required/optional | implemented |
| deterministic-checks / Pipeline enforcement | `pkg/pipeline/pipeline_test.go` — required failure overrides PASS, unavailable optional is warning | implemented |
| deterministic-checks / Candidate handoff | `pkg/pipeline/pipeline_test.go`; `e2etest/e2e_test.go` — bounded exact patch hashing, reviewed workspace identity and verifier typed-check candidate evidence | implemented |
| delivery-executor / Strict plan and exact staging | `pkg/delivery/plan_test.go` | implemented |
| delivery-executor / Partial resume | `pkg/delivery/plan_test.go` — real local git/bare remote, fake gh and exact recovery after an injected post-commit persistence gap | partial: normal/post-commit retry covered; exhaustive persist/effect fault matrix pending |
| delivery-executor / Full run | `e2etest/e2e_test.go` — deterministic check, exact commit, push and PR | implemented |
| observability / Events and projections | `pkg/evidence/store_test.go` — hash-chain tamper detection plus lifecycle replay and manifest binding; `pkg/web/store/sqlite_test.go`; `pkg/web/recorder_test.go` | partial: lifecycle replay covered; projector recovery/effect replay pending |
| observability / Immutable history and safe serving | `pkg/web/server_test.go` — old run history, traversal, symlink, size and pagination | implemented |
| observability / Interrupted reconciliation | `pkg/web/store/sqlite_test.go` | implemented |
| observability / Reports | `pkg/report/generator_test.go`; pipeline/E2E report generation | implemented |
| evals / Strict and isolated LLM judge | `pkg/eval/eval_test.go` — exact markers, samples/statistics, isolation and atomic JSON | implemented |
| evals / Layer model | `pkg/eval/eval_test.go` — deterministic, behavioral, fault-injection, advisory invariant | partial: framework covered; calibrated corpus pending |
| web / WebSocket and frontend contracts | `pkg/web/websocket_test.go`; `web/src/components/*.test.tsx`; TypeScript build | implemented |
| dependency security | pinned `govulncheck`, `npm audit --audit-level=high`, Go 1.26.5 | implemented |

## Baseline verification commands

- `npx --yes @fission-ai/openspec@1.4.1 validate --all --strict --no-interactive`
- `gofmt -l .` (пустой вывод)
- `git diff --check`
- `go mod verify`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- `go test -coverprofile=coverage.out ./pkg/...` with total coverage >= 60%
- `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`
- `npm ci && npm audit --audit-level=high && npm run lint && npm test && npm run build` in `web/`
