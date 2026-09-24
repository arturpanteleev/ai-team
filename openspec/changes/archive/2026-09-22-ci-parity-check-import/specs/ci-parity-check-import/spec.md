# ci-parity-check-import delta (P1-8)

## ADDED Requirements

### Requirement: Import checks from project CI without executing arbitrary YAML

A project MUST be able to import a limited, explainable set of checks from its
real CI configuration, without executing arbitrary YAML (no shell, templates, or
arbitrary commands).

#### Scenario: Known CI steps map to checks

- **WHEN** a GitHub Actions workflow contains `run: go vet ./...`,
  `run: go build ./...` or `run: go test ./... [-race]`
- **THEN** the importer produces corresponding check definitions with class
  `lint`, `build` or `unit` (go-test-json adapter for tests)
- **AND** every imported definition passes `checks.Definition.Validate()`

#### Scenario: Known actions map to checks

- **WHEN** a step uses `golangci/golangci-lint-action` or
  `github/codeql-action`
- **THEN** it maps to an optional lint / security check

#### Scenario: Unknown or multi-command steps are never executed

- **WHEN** a step does not match the whitelist or its `run` contains shell
  operators (`&&`, `;`, `|`, newline)
- **THEN** it MUST be recorded as skipped with a reason
- **AND** MUST NOT be imported or executed

### Requirement: Deterministic effective suite fingerprint

The effective suite MUST be deterministic and fingerprinted (SHA-256) before any
check is executed, so adopters can pin and detect suite drift.

#### Scenario: Deterministic fingerprint across imports

- **WHEN** the same workflow tree is imported twice
- **THEN** the fingerprint MUST be identical

#### Scenario: Duplicate checks are collapsed

- **WHEN** two workflows produce the same check name
- **THEN** the effective suite MUST contain it once

### Requirement: Import command shows suite without running

The CLI MUST provide a read-only command that imports and shows the effective
suite and its fingerprint, without executing the checks.

#### Scenario: ci-import prints suite and fingerprint

- **WHEN** `ai-team ci-import --target <dir>` runs
- **THEN** the effective suite, skipped steps and the fingerprint are shown
- **AND** no check is executed
- **AND** in `--json` mode a stable record with `type: ci-import` and
  `data.fingerprint` is emitted