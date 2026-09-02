# structured-output delta (OPS-6)

## ADDED Requirements

### Requirement: Stable JSON output for command results

The CLI MUST provide a `--json` global mode in which the results of `run`,
`verify`, `export` and `gate` are emitted as stable machine-readable JSON
records on stdout, one object per line, parseable via `encoding/json`.

#### Scenario: Gate result as JSON

- **WHEN** `ai-team gate --json ...` completes
- **THEN** stdout MUST contain a single JSON object line with fields
  `level`, `cmd`, `type`, `message`, `data`, `exit_code`
- **AND** `data` MUST include `status`, `policy` and `bundle_sha256`

#### Scenario: Verify result as JSON

- **WHEN** `ai-team verify --json <bundle|run>` completes successfully
- **THEN** stdout MUST contain a JSON record with `cmd:"verify"` and a `type`
  of `gate_bundle`, `run_bundle` or `run`
- **AND** on failure a record with `level:"error"` MUST be emitted before the
  nonzero exit

### Requirement: Quiet mode

The CLI MUST provide `--quiet`/`-q` global flags that suppress secondary human
output from stdout while preserving critical error/warning output on stderr and
preserving exit codes.

#### Scenario: Quiet run

- **WHEN** `ai-team gate --quiet ...` completes
- **THEN** stdout MUST NOT contain a JSON result record
- **AND** exit codes MUST be identical to the non-quiet invocation

### Requirement: Global flag handling without subcommand breakage

`--json` and `--quiet`/`-q` MUST be accepted both before and after the
subcommand name, and subcommands MUST NOT fail on the presence of these global
flags.

#### Scenario: Flag after subcommand

- **WHEN** `ai-team gate --json ...` (global flag after subcommand) runs
- **THEN** the subcommand MUST parse successfully and emit JSON

#### Scenario: Flag before subcommand

- **WHEN** `ai-team --json gate ...` (global flag before subcommand) runs
- **THEN** the command MUST dispatch to `gate` and emit JSON

### Requirement: Logging package

The codebase MUST provide `pkg/logging` exposing a global mode
(`Default`/`Quiet`/`JSON`) and an `Emit(Record)` function, unit-tested for
stable JSON structure and mode roundtrip.

#### Scenario: JSON record structure

- **WHEN** `Emit` runs in JSON mode
- **THEN** the emitted line MUST unmarshal into a `Record` with matching fields
