# external-gate Specification

## Purpose

Гейт внешней программы: доверенная команда из конфигурации репозитория читает
JSON-контекст и возвращает один JSON-вердикт по протоколу `ai-team.gate/v1`.
Протокол и классификация исхода не зависят от способа запуска, поэтому
Docker-адаптер использует их без изменений. Справочник: `docs/reference/gate-protocol.md`.

## Requirements

### Requirement: External program gate is a library definition with trusted parameters

The built-in library MUST contain `external_program@1` of kind `external` with
parameters `command` (required argv list), `timeout_seconds` (default 60,
maximum 3600), `cwd` (repository-relative, default `.`), `env_allow` (variable
names), and `max_output_bytes` (default 1 MiB, maximum 16 MiB). The program MUST
come only from repository code or configuration; the Web UI, REST API, and MCP
MUST NOT accept a command. An absolute `cwd`, a `cwd` escaping the repository,
an invalid or reserved (`AI_TEAM_GATE_*`) environment name, an out-of-range
limit, and an `argv` element that embeds a credential MUST be rejected before a
run starts.

#### Scenario: Inline secret in argv

- **КОГДА** `command` contains an argument such as `--token=ghp_…`
- **ТОГДА** validation MUST reject the configuration without echoing the value
- **И** the message MUST direct the author to `env_allow`

### Requirement: The program is launched without a shell under strict limits

A launcher MUST run `command` as an argv without a shell, in the resolved
working directory, with exactly the environment made of `env_allow` variables
set in the controller and the reserved `AI_TEAM_GATE_PROTOCOL` and
`AI_TEAM_GATE_ATTEMPT_ID`; no other controller variable MAY be inherited. The
program MUST run in its own process group, and that whole group MUST be killed
on timeout, cancellation, and exceeding the output limit, and what remains of
the group after the program exits on its own MUST be killed as well (on
platforms where the group can still be addressed). A relative program path MUST
stay inside the repository. Stdout and stderr MUST
each be bounded by `max_output_bytes`.

#### Scenario: Environment isolation

- **КОГДА** the controller has variables outside `env_allow`
- **ТОГДА** the program MUST NOT see them, including `PATH` and `HOME`

#### Scenario: Timeout kills descendants

- **КОГДА** the program starts a background child and exceeds its timeout
- **ТОГДА** the attempt MUST end `timeout` and no process of the group MAY survive

#### Scenario: Background child after a clean exit

- **КОГДА** the program exits normally leaving a background child
- **ТОГДА** the child MUST be killed

### Requirement: The JSON protocol separates input and result

The controller MUST write one `ai-team.gate/v1` input document to stdin with the
task, stage, visit, attempt ID, gate ID, version and name, result type, result
SHA-256, readable paths, and limits. The program MUST print exactly one result
document to stdout with `verdict` (`pass`, `fail`, `not_applicable`), a `reason`
(required for `fail`, at most 2000 bytes), and at most 20 evidence references
with relative clean paths. Unknown fields, a second document, trailing text, a
wrong protocol, and an unknown verdict MUST make the result invalid. A
duplicated key (also after escape decoding) or a key spelled in a different
case than the protocol's MUST make the result invalid.

#### Scenario: Malformed result

- **КОГДА** stdout is text, two documents, or has an unknown verdict
- **ТОГДА** the attempt MUST end `error` with no verdict

#### Scenario: Ambiguous keys

- **КОГДА** stdout has `verdict` twice (`fail` then `pass`) or the key `Verdict`
- **ТОГДА** the attempt MUST end `error` with no verdict

### Requirement: A valid fail is distinct from a technical failure

An attempt MUST be `succeeded` with the document's verdict only when the program
exited with code 0 in time and printed a valid result. A non-zero exit, a death by
signal, a start failure, an invalid result, and exceeding the output limit MUST
end `error`; exceeding the timeout MUST end `timeout`; cancellation MUST end
`canceled`. None of these MUST carry a verdict or be treated as `pass`; a
required attempt in any of these states MUST block.

#### Scenario: Demo outcomes are distinguishable

- **КОГДА** the demo program runs in modes `pass`, `fail`, and `crash`
- **ТОГДА** the attempts MUST be `succeeded/pass`, `succeeded/fail`, and `error`
  with no verdict

#### Scenario: Output flood

- **КОГДА** the program prints more than `max_output_bytes`
- **ТОГДА** it MUST be killed and the attempt MUST end `error`

### Requirement: Evidence and logs never disclose secrets

The runner MUST store redacted stdout and stderr logs bounded by the limit, a
redacted invocation record that lists environment variable names but not values,
and copies of the evidence files named by the program. An evidence file MUST be
stored only if it is a plain text regular file inside the evidence directory
without symlinks, within 1 MiB, matching a declared digest, and free of secrets;
otherwise the attempt MUST end `error` and the file MUST NOT be stored. Reasons,
logs, and the invocation record MUST have credential patterns and the values of
all allowed variables of at least 4 bytes replaced, whatever their names.

#### Scenario: Secret in the program output

- **КОГДА** the program prints an allowed secret variable's value in stderr and in
  its reason
- **ТОГДА** no stored file and no field of the attempt MAY contain the value

#### Scenario: Secret in an evidence file

- **КОГДА** an evidence file contains a credential
- **ТОГДА** the attempt MUST end `error` and the file MUST NOT be stored

### Requirement: Running is idempotent by attempt ID and independent of the launcher

Running an attempt ID that already has a committed record MUST return that record
without starting the program, and concurrent runs of one ID MUST start it once.
Reusing an ID for another task, stage, visit, result hash, or gate MUST be an
error. A record MUST be committed last, and an uncommitted directory MUST be
discarded and re-run. A launcher MUST be replaceable behind one interface that
receives the input document and returns stdout, stderr, exit status, and the
output-limit flag, while parsing, classification, redaction, and evidence storage
stay in the runner. The idempotence guarantee is per controller process; two
processes sharing one evidence root are not coordinated.

The program MUST NOT be given, and MUST NOT be able to use, the directory where
the controller keeps the attempt record: its evidence directory MUST be a
separate directory outside it, anything found in the attempt directory after the
run MUST be discarded and the attempt MUST end `error`, and a stored record
MUST be trusted only when its signature, made with a key the program is not
given, verifies; otherwise reading it MUST fail. The key MUST be created
atomically (never observable partially written) before any program runs, and a
key of the wrong length MUST be an error. The host launcher runs the program as
the same user, so this signature protects against writes into the attempt
directory during a run, accidental corruption, and processes without access to
the key; it does not protect against a hostile program, which requires
isolation (container launcher or a separate uid).

#### Scenario: Forged attempt record

- **КОГДА** the program writes a passing `attempt.json` into the attempt directory
  and prints a failing result
- **ТОГДА** the attempt MUST NOT be `pass` and a repeated run MUST NOT return the
  forged record

#### Scenario: Repeated call

- **КОГДА** the same attempt ID is run twice
- **ТОГДА** the program MUST run once and both calls MUST return the same attempt

#### Scenario: Second launcher

- **КОГДА** a Docker-kind launcher returns a valid result document
- **ТОГДА** the attempt MUST carry executor `docker` and the same classification
