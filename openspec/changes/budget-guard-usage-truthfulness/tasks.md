# Budget guard + usage truthfulness (P1-7) — tasks

Все шаги выполнены в одном кодовом изменении.

1. `pkg/config`: `BudgetConfig` + `Validate`/effective-helpers, allowlist/
   rawConfig/`Config.Validate`; юнит-тесты дефолтов и reject-кейсов.
2. `pkg/runtime`: `UsageReporter` optional interface; `AgentCLIRuntime`
   парсит usage из stdout при успехе (только attested).
3. `pkg/pipeline`: runState `budgetConfig`/`usageTotal`; wall-time ctx в
   `RunWithResult`; попытка-cap до `runStage` в `executeGraph`; агрегация
   attested usage в `runStage`.
4. `pkg/metrics`: `Usage`, `UsageEnvelope`-поля, `Build(..., usage)`;
   обновление существующих тестов + новые кейсы.
5. `finalize.go`: передача usage в `runState` → `metrics.Build`.
6. OpenSpec delta `budget-guard-usage-truthfulness`.

Verification: `go build ./...`, `go vet ./pkg/...`, `gofmt -l`, `go test ./...`
(только 2 известных e2e baseline fail), `make specs`.