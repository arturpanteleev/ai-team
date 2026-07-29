## ADDED Requirements

### Requirement: Sandbox backend interface
The system MUST expose a `SandboxBackend` interface that executes an agent or
check command under explicit filesystem, network, environment and resource
policy, and that every deterministic check and every agent CLI invocation
MUST go through this interface rather than an unconfined `exec.Command`
against the live filesystem.

#### Scenario: No backend available in strict profile
- **WHEN** the configured profile is `strict` and no sandbox backend probes successfully at startup
- **THEN** the run MUST fail closed before any agent or check executes
- **AND** the failure MUST name which capability probe failed

#### Scenario: No backend available in trusted-local profile
- **WHEN** the configured profile is `trusted-local` and no sandbox backend is available
- **THEN** the run MAY proceed without sandboxing
- **AND** the run evidence MUST record that no sandbox backend was used

### Requirement: Default-deny network policy
The sandbox backend MUST deny all network access by default and MUST require
an explicit host:port allow-list entry for any network access an agent or
check needs.

#### Scenario: Unlisted host
- **WHEN** a sandboxed process attempts to reach a host:port not in the run's allow-list
- **THEN** the connection MUST be denied by the backend, not by application-level policy alone

### Requirement: Explicit environment allow-list
The sandbox backend MUST pass only an explicitly allow-listed set of
environment variables to the sandboxed process; it MUST NOT pass the calling
process's full environment by default.

#### Scenario: Secret in parent environment
- **WHEN** the process invoking `ai-team run` has a credential set in its environment that is not on the allow-list
- **THEN** the sandboxed agent or check process MUST NOT receive that variable

### Requirement: Resource limits
The sandbox backend MUST enforce CPU, memory, process count and wall-clock
time limits on every sandboxed invocation, in addition to the existing
per-stage timeout.

#### Scenario: Runaway process tree
- **WHEN** a sandboxed check spawns more processes than its configured pid limit
- **THEN** the backend MUST refuse to create further processes for that invocation

### Requirement: Backend identity in evidence
Every sandboxed invocation's evidence MUST record which backend executed it,
its image/profile digest where applicable, the resolved network and
environment policy, and the resource limits actually applied.

#### Scenario: Two attempts, two backend versions
- **WHEN** the sandbox backend's own version or image digest differs between two attempts of the same stage
- **THEN** both attempts' evidence MUST record their own backend identity distinctly
