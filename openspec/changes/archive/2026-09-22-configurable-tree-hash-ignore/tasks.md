# Tasks — Configurable tree-hash ignore list (OPS-2)

## T1. pkg/checks: extra ignore set

- [x] `pkg/checks/ignore.go`:
  - `ValidIgnoreDirName(name)` — строгий шаблон (канонический).
  - `SetExtraIgnoreDirs` / `ResetExtraIgnoreDirs` — mutex-защищённый
    процесс-глобальный набор; `DefaultIgnoreDirs()` возвращает baseline + extra.
- [x] `ignore_test.go`: строгая валидация имён; extra изменяет digest;
      reset возвращает baseline; baseline неизменяем.

## T2. pkg/config: параметр + строгая валидация

- [x] `Config.TreeHash *TreeHashConfig` (yaml `tree_hash.ignore_dirs`),
      разобран в `UnmarshalYAML` (в allowlist и rawConfig).
- [x] `TreeHashConfig.Validate()`: `checks.ValidIgnoreDirName` + анти-дубликат;
      включён в `Config.Validate`.
- [x] `Load()` применяет набор через `checks.SetExtraIgnoreDirs` — все digest
      вычисления процесса используют его.
- [x] `treehash_config_test.go`: validate-кейсы; load применяет ignore;
      недопустимый путь отвергается Validate.

## T3. Verification

- [x] `go build ./...`, `gofmt -l` чисто, `go vet ./...`.
- [x] `go test ./pkg/checks ./pkg/config ./pkg/pipeline ./pkg/delivery`
      зелёные; полный `go test ./...` — только два известных e2e baseline-fail.
- [x] `make specs` 67/67.
