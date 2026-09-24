# CI-parity check import (P1-8)

## Why

Проекты-adoptеры хотят, чтобы локальная проверка ai-team повторяла их реальный
CI. Ручное копирование check-определений из CI в `config.yaml` дублируется и
расходится. P1-8: импортировать ограниченный объяснимый набор checks из
реального project CI **без исполнения произвольного YAML**. Effective suite
показывается и fingerprinted до запуска. Начинаем с одного adopter-формата:
GitHub Actions workflow (`.github/workflows/*.yml`).

## What Changes

- **`pkg/ciimport`** (новый): 
  - `Import(targetDir, format)` — читает workflow'ы строгой логикой; из `jobs[].steps[]`
    извлекаются только `uses`/`run`; ограниченное явное сопоставление →
    `checks.Definition`. **Произвольный YAML не исполняется**: нет shell/
    template/run-раскрытия; неизвестные шаги попадают в `Skipped`, а не
    запускаются.
  - Формат по умолчанию `github-actions`: whitelist шагов `go vet`→lint,
    `go build`→build, `go test [-race -json]`→unit (go-test-json adapter),
    `golangci/golangci-lint-action`→lint, `github/codeql-action`→security.
    Многокомандные `run` (с `&&`, `;`, `|`, `\n`) не отображаются.
  - `Fingerprint(definitions, sources)` — детерминированный SHA-256 effective
    suite **до запуска**; `Imported{Definitions, Fingerprint, SourceFiles,
    Skipped, WorkflowCount}`.
  - Дедупликация по имени + детерминированная сортировка.
- **CLI**: `ai-team ci-import [--target <dir>] [--format github-actions]` —
  печатает effective suite, fingerprint и пропуски; JSON/quiet режимы (OPS-6)
  работают. Команда checks не запускает (только импорт, показ, fingerprint).

## Acceptance Criteria

1. Workflow GitHub Actions с известными шагами (`go vet`, `go build`,
   `go test[-race]`, golangci-lint, codeql) импортируется в форму checks,
   все определения проходят `Definition.Validate()`.
2. Неизвестные/многокомандные шаги не импортируются и НЕ исполняются — они
   фиксируются в `Skipped` с причиной.
3. Effective suite детерминирована и fingerprinted (SHA-256) до запуска;
   повторный импорт того же дерева даёт тот же отпечаток; дубликаты схлопываются.
4. `ai-team ci-import ` показывает suite + отпечаток; `--json` печатает stable
   record (`type: ci-import`, `data.fingerprint`).
5. Регрессии нет: `go test ./...` зелёный (кроме двух известных e2e baseline-fail),
   `make specs` 68/68.