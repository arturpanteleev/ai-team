# configurable-tree-hash-ignore Specification

## Purpose
Проектно-настраиваемые дополнительные исключения для workspace identity hash
поверх `.git` и `.ai-team`, со строгой валидацией имён. Mutation guard
использует отдельный набор и всегда видит файлы проекта в этих каталогах.
## Requirements
### Requirement: Project-specific tree-hash ignore directories

A project MUST be able to configure additional directory names that are excluded
from workspace identity hashing, without weakening the `.git` and `.ai-team`
baseline. Mutation attribution MUST use its own stricter ignore set.

#### Scenario: Ignore directories excluded from digest

- **WHEN** a project config declares `tree_hash.ignore_dirs` containing simple
  directory names (e.g. `coverage`)
- **THEN** those directories MUST be excluded from the workspace digest
- **AND** the digest MUST differ from the digest computed without the extra ignore
- **AND** checks, pipeline, delivery planning and execution MUST use the same
  identity digest policy

#### Scenario: Identity baseline

- **WHEN** a project config declares extra ignore directories
- **THEN** `.git` and `.ai-team` MUST remain ignored
- **AND** dependency and build directories MUST remain included unless named
  explicitly in project configuration

#### Scenario: Mutation guard keeps full attribution

- **WHEN** an agent or check changes a file in a configured or conventional
  build/dependency directory
- **THEN** mutation guard MUST detect and attribute the change
- **AND** it MUST ignore only `.git` and `.ai-team`

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
