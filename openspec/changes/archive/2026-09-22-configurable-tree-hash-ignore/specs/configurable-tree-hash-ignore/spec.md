# configurable-tree-hash-ignore delta (OPS-2)

## ADDED Requirements

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
- **THEN** the canonical baseline entries (`.git`, `.ai-team`, `node_modules`,
  `vendor`, `dist`, `.venv`, `__pycache__`) MUST remain ignored

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
