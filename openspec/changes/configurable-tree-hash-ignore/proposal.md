# Configurable tree-hash ignore list (OPS-2)

## Why

`pkg/checks.DefaultIgnoreDirs()` — единственный канонический ignore-list для
workspace tree hashing — жёстко зашит. Проекты не могут исключать из digest
собственные каталоги артефактов (coverage, вендорные таргеты, кэши сборки и
т.п.), что приводит к ложной "грязи" workspace и срывам проверок/delivery на
нерелевантных файлах. Нужен строгий project-specific ignore config БЕЗ
ослабления baseline identity.

## What Changes

- **`pkg/checks`**:
  - `ValidIgnoreDirName(name)` — строгий шаблон простого имени каталога
    (без `/`, не `.`/`..`, без ведущего `-`, непустое, ограниченной длины,
    только `[A-Za-z0-9._-]`). Path'ы и glob'ы запрещены.
  - `SetExtraIgnoreDirs(names)` / `ResetExtraIgnoreDirs()` — процесс-глобальный
    дополнительный набор; `DefaultIgnoreDirs()` возвращает baseline + extra
    (baseline неизменяем). Канонический tree hash использует его на ВСЕХ
    call-site (checks, pipeline, delivery planner/executor) — согласованность
    digest между check-time и delivery-time гарантирована.
- **`pkg/config`**:
  - `TreeHashConfig{IgnoreDirs []string}` в `Config.TreeHash` (yaml `tree_hash`).
  - Строгая `TreeHashConfig.Validate()`: каждое имя через
    `checks.ValidIgnoreDirName`, без дубликатов.
  - `Config.Validate()` включает валидацию `TreeHash`.
  - `Load()` регистрирует валидированный набор через `checks.SetExtraIgnoreDirs`,
    поэтому все вычисления digest в процессе используют его.

## Acceptance Criteria

1. Проект может указать `tree_hash.ignore_dirs` простыми именами каталогов;
   они исключаются из workspace digest.
2. Пути/glob'ы/небезопасные имена (`..`, `/`, ведущий `-`, пробелы) отвергаются
   строгой валидацией.
3. Канонический baseline (`DefaultIgnoreDirs`) никогда не ослабляется.
4. Digest согласован: check-time и delivery-time используют идентичный ignore-list.
5. `make specs` 67/67, `go test ./...` зелёные (кроме двух известных e2e baseline-fail).
