# Tasks — CI-parity check import (P1-8)

## T1. pkg/ciimport: безопасный импорт

- [x] `ciimport.go`/`map.go`:
  - `Import(target, format)` — строгое чтение `.github/workflows/*.yml`;
    извлечение только `uses`/`run`; ограниченное whitelist-сопоставление;
    неизвестные/многокомандные шаги → `Skipped` (не исполняются).
  - `LoadWorkflows`, `Fingerprint` (SHA-256, до запуска), дедупликация +
    детерминированная сортировка; `Imported{Definitions, Fingerprint, ...}`.
  - Приватные `mapUses`/`mapRun`/`mapGoTest` — статическое сопоставление,
    без shell/template-раскрытия.

## T2. pkg/ciimport: тесты

- [x] `ciimport_test.go`: базовый импорт (`go vet/build/test -race`, golangci
      uses) — все definitions валидны; неизвестные/многокомандные шаги в
      `Skipped`; дедупликация; детерминизм fingerprint'а (повторный импорт);
      пустой target без workflow.

## T3. CLI: `ai-team ci-import`

- [x] `cmd/ai-team/main.go`: `cmdCIImport` (`--target`, `--format`), печать
      effective suite + fingerprint + пропуски, OPS-6 JSON/quiet через
      `logging.Emit`; `--json` — record `type: ci-import` с `data.fingerprint`.
- [x] README: зарегистрирована команда.

## T4. Verification

- [x] `go build ./...`, `go vet ./pkg/ciimport ./cmd/ai-team`, `gofmt -l` чисто.
- [x] `go test ./pkg/ciimport ./pkg/checks ./pkg/config` зелёные; полный
      `go test ./...` — только два известных e2e baseline-fail.
- [x] `make specs` 68/68 (67 + 1 новый delta).
- [x] Ручная проверка: `ci-import` на temp-проекте с workflow — suite +
      fingerprint + JSON mode.