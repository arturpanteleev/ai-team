# Redaction + retention contract (P1-6) — tasks

1. `pkg/redact`: scanner (правила + фильтр ложных срабатываний),
   `ClassifyField`, policy (include/exclude via `pkg/scope`), `ScanDir`/`Verify`
   (fail-closed), `RedactFile`. Тесты: детекция, ignore benign, redaction,
   classify, policy globs, verify fail-closed/exclude.
2. `pkg/config`: `RedactionConfig` + `RetentionConfig` (+ эффективные дефолты),
   allowlist/rawConfig/assignment/`Config.Validate`. Тесты валидации.
3. CLI: `cmd/ai-team/redact.go` (`verify|scan|redact`, policy из config, scanning
   `.ai-team/runs`), dispatch + usage.
4. `cmd/ai-team/export.go`: fail-closed блокер (`redact.Verify` перед Build).
5. `cmd/ai-team/gc.go`: retention-дефолты из config (явные флаги приоритетны).
6. README usage roots; OpenSpec delta `redaction-retention-contract`.

Verification: `go build ./...`, `go vet`, `gofmt -l`, `go test ./...` (только 2
известных e2e baseline fail), `make specs`.