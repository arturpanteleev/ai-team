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

#### Scenario: A template submitted over the web names a command gate

- **КОГДА** a template submitted through REST, MCP, or the Web UI (validate or
  publish) references `external_program` or `docker_program`
- **ТОГДА** validation MUST report it and publish MUST answer `400` with the code
  `trusted_config_only`, and the active template MUST stay unchanged
- **И** the message MUST say that such gates are allowed only in the repository's
  file configuration (`.ai-team/config.yaml`)
- **И** the same gate in the file configuration MUST still load and validate

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

### Requirement: The Docker gate runs the same protocol in a pinned container under strict defaults

The built-in library MUST contain `docker_program@1` of kind `docker` with the
parameters of `external_program@1` plus `image` (required), `allow_unpinned_image`,
`pull` (`missing`|`never`), `network` (`none`|`bridge`), `cpus`, `memory_mb`,
`pids_limit`, `tmpfs_mb`, `user`, `repo_access` (`ro`|`rw`), and `mounts`
(`<repository path>:<container path>[:ro|rw]`). `image` MUST carry a sha256
digest; a reference without one MUST be rejected before a run unless
`allow_unpinned_image` is set. The launcher MUST run `docker run` as an argv
without a shell with `--rm`, a unique container name, a read-only root file
system, `--cap-drop ALL`, `no-new-privileges`, an explicit non-root user (the
controller's uid:gid, or `65534:65534` when that is root, unless configured),
`--network none` unless `bridge` is configured, and CPU, memory (without swap) and
PID limits. It MUST NOT pass the Docker socket, the home directory, controller
credentials, or any controller environment variable other than `env_allow` and
the reserved protocol variables, and MUST NOT mount anything but the repository
(read-only unless `repo_access` is `rw`), the private evidence directory, the
stage result file (read-only, when it lies outside the repository), and the
configured `mounts`. A mount source MUST be a repository subpath without a
symlink component; a writable mount of `.git`, a mount target under `/proc`,
`/sys`, `/dev` or the reserved gate directories, and an `env_allow` name of the
Docker client environment (compared without case) MUST be rejected before a run.
When `repo_access` is `rw`, the launcher MUST additionally mount `.git` (and any
other git directory inside the repository) read-only over the writable repository,
MUST NOT mount a git directory outside the repository, and MUST refuse a symlinked
`.git`. With a remote daemon (`DOCKER_HOST`, `DOCKER_CONTEXT`) mount sources
resolve on the remote machine; this is unsupported. With `network: bridge` the
repository content, including `.git/config` remote URLs, is reachable from the
network, and the exceptions record MUST say so by listing `network=bridge`. The input document MUST
carry container paths only. Environment values MUST NOT appear in the `docker`
argv or in any stored record.

#### Scenario: Unpinned image

- **КОГДА** `image` is `alpine:3.20`
- **ТОГДА** validation MUST reject the configuration unless `allow_unpinned_image`
  is true, and the exception MUST be recorded in the attempt's `invocation.json`

#### Scenario: Declared mounts and variables only

- **КОГДА** a gate is launched with no `mounts` and no `env_allow`
- **ТОГДА** the `docker run` argv MUST contain exactly two `--mount` options (the
  repository read-only and the evidence directory) and only the two reserved
  variables

#### Scenario: Writable repository keeps .git read-only

- **КОГДА** `repo_access` is `rw` and a program writes an ordinary repository file
  and a file under `.git/hooks`
- **ТОГДА** the first write MUST succeed and the second MUST fail

#### Scenario: Closed sandbox

- **КОГДА** a program inside the container tries to write to the repository or the
  root file system, reach the network, read a controller variable, find the Docker
  socket, or run as root
- **ТОГДА** each attempt MUST fail

### Requirement: Containers left by a dead controller are swept at the next start

Every container MUST carry the labels `ai-team.dockerrun`, `ai-team.gate-attempt`
and `ai-team.started` (unix start time). The first Docker gate of a Runner MUST
remove containers with those labels that are older than the maximum gate timeout
plus a margin. A sweep failure MUST NOT block the gate. Removal of a container
whose removal is already in progress MUST NOT count as a failed removal. The
guarantee that no container is left behind is best effort after a controller crash:
the leftover lives until the next controller start.

#### Scenario: Old and fresh containers

- **КОГДА** an old and a fresh container carry the gate labels and a third lacks the
  owner label
- **ТОГДА** the sweep MUST remove only the old one of the labeled two

### Requirement: Docker failures are technical errors; a timeout removes the container

The attempt MUST end `error` without a verdict when the Docker daemon is
unreachable, the image is missing or cannot be pulled, the runtime cannot start the
command, the container is killed (including out of memory), the `docker` binary is
missing, or the output limit is exceeded; its reason MUST name the failure class. A
valid result document from a container that exited with code 0 MUST be classified
exactly as a host result. On timeout the attempt MUST end `timeout` and on
cancellation `canceled`; in both cases the `docker` process MUST be killed and the
container MUST be force-removed by name, and a failed removal MUST be stated in the
reason. `invocation.json` MUST record the executor, image, network, user, mounts
(source, target, mode), variable names, limits, the `docker run` argv, and a list of
every exception to the defaults, and MUST NOT contain secret values or host paths of
the controller.

#### Scenario: Daemon down or wrong image

- **КОГДА** `docker run` exits with 125
- **ТОГДА** the attempt MUST end `error`, not `fail`

#### Scenario: Same fixture on both adapters

- **КОГДА** one gate fixture runs through the host and the Docker launcher
- **ТОГДА** the execution, verdict, reason, and the evidence files (except
  `invocation.json`) MUST be identical

#### Scenario: Timeout

- **КОГДА** the containerized program exceeds `timeout_seconds`
- **ТОГДА** the attempt MUST end `timeout` and no container with its name MAY remain
