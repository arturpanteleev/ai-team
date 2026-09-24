# configurable-tree-hash-ignore Specification

## Purpose
Проектно-настраиваемые дополнительные ignore-каталоги для workspace tree hash поверх неослабляемого канонического baseline, со строгой валидацией имён.
## Requirements
### Requirement: Canonical baseline is controller-owned directories only

Канонический baseline ignore-набор workspace tree hash MUST состоять ТОЛЬКО из
каталогов, которые ведёт сам контроллер (`.git`, `.ai-team`). Каталоги
зависимостей и сборки (`node_modules`, `vendor`, `dist`, `.venv`,
`__pycache__` и прочие) MUST NOT исключаться по умолчанию: их содержимое —
часть проекта, и запись в них — мутация.

#### Scenario: Dependency directory is part of the digest

- **WHEN** файл внутри `node_modules`, `vendor`, `dist`, `.venv` или
  `__pycache__` изменён, а проект не объявил этот каталог в
  `tree_hash.ignore_dirs`
- **THEN** workspace digest MUST измениться

### Requirement: Project-specific tree-hash ignore directories

A project MUST be able to configure additional directory names that are excluded
from workspace tree hashing, without weakening the canonical baseline ignore set.

#### Scenario: Ignore directories excluded from digest

- **WHEN** a project config declares `tree_hash.ignore_dirs` containing simple
  directory names (e.g. `coverage`)
- **THEN** those directories MUST be excluded from the workspace digest
- **AND** the digest MUST differ from the digest computed without the extra ignore
- **AND** every digest computation in the process (checks, pipeline, delivery
  planning and execution) MUST use the same expanded ignore set

#### Scenario: Baseline is never weakened

- **WHEN** a project config declares extra ignore directories
- **THEN** the canonical baseline entries (`.git`, `.ai-team`) MUST remain ignored

### Requirement: Configured ignores never reach mutation attribution

Project-specific `tree_hash.ignore_dirs` MUST влиять только на workspace digest.
Per-attempt mutation guard MUST использовать собственный, неконфигурируемый
набор (см. capability `git-diff-guard`), чтобы конфигурация не могла превратить
`mutation: none` в «пишем куда угодно».

#### Scenario: Read-only stage writes into a configured ignore directory

- **WHEN** проект объявил каталог в `tree_hash.ignore_dirs`
- **AND** этап с `mutation: none` записал файл внутрь этого каталога
- **THEN** mutation guard MUST отклонить попытку
- **AND** путь MUST попасть в mutations манифеста попытки

### Requirement: Strict validation of ignore directory names

Ignore entries MUST be validated strictly: only simple directory names are
accepted; paths, glob patterns and unsafe names are rejected.

#### Scenario: Invalid names are rejected

- **WHEN** an ignore entry is empty, `.`, `..`, contains `/` or `\`, starts with
  `-`, contains whitespace, or is longer than the limit
- **THEN** configuration validation MUST reject it with an error

#### Scenario: Duplicate entries are rejected

- **WHEN** the same directory name appears more than once in `ignore_dirs`
- **THEN** configuration validation MUST reject it with an error
