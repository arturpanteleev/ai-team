# Stage-scoped agent MCP servers delta (B-85)

## ADDED Requirements

### Requirement: MCP servers are selected per agent stage

The project configuration MUST support defining local stdio MCP servers in
schema v5. An agent invocation MUST receive only the server IDs explicitly selected on
its stage. MCP selection MUST require an agent and `cli: codex`; unknown server
IDs and selection by any other runtime MUST fail before model execution.

#### Scenario: Knowledge context is available only to selected stages

- **WHEN** the project defines a knowledge server and selects it only on the
  product-spec stage
- **THEN** that stage's Codex invocation MUST contain the knowledge server
- **AND** other stages MUST NOT receive it

#### Scenario: Monitoring is selected for observation

- **WHEN** the observation stage selects a monitoring server
- **THEN** its Codex invocation MUST contain the monitoring server
- **AND** stages without that selection MUST NOT contain it

#### Scenario: Unsupported runtime or server reference

- **WHEN** an MCP server is selected with a non-Codex runtime or the ID is not
  defined in the project allowlist
- **THEN** configuration validation MUST fail closed before model execution

### Requirement: MCP server definitions are explicit and bounded

An MCP definition MUST specify an absolute executable command and fixed argv;
the command MUST be invoked without shell parsing. Definitions MUST be limited
to local stdio transport, a bounded number and size of arguments/environment
entries, and startup/tool timeouts with positive effective values and documented
ceilings. The runtime MUST reject known shell executables, malformed names, control
characters, unavailable commands, and values outside these limits.

#### Scenario: Malformed command is rejected

- **WHEN** an MCP definition uses a relative path, shell executable, malformed
  argument or exceeds an input limit
- **THEN** configuration or runtime setup MUST reject it before agent execution

#### Scenario: MCP server hangs

- **WHEN** a server exceeds its startup timeout or a tool call exceeds its
  timeout
- **THEN** Codex MUST bound the operation and the agent stage MUST remain
  subject to its stage timeout

### Requirement: MCP credentials do not inherit from the runtime

The system MUST NOT load user/global Codex MCP configuration. The system MUST
build MCP configuration only from the stage's project allowlist. Provider,
subscription, cloud and control-plane credentials MUST NOT be passed to MCP
servers through configured environment variables. Known credential-like names
MUST be rejected in both literal `env` and inherited `env_vars`, even when
explicitly listed; other inherited names MUST be explicitly opted in both by
the MCP definition and `AI_TEAM_HARNESS_ENV_ALLOW`.

Codex subscription `auth.json` MAY be copied to the private Codex CLI home for
model authentication, but MUST NOT be configured as an MCP server environment
variable. MCP servers MUST receive a separate temporary `HOME`. This contract
reduces environment credential exposure and MUST NOT be described as OS-level
isolation from the current user.

#### Scenario: User MCP config exists

- **WHEN** the user's global Codex config contains MCP servers not selected by
  this project stage
- **THEN** the agent invocation MUST NOT load those servers
- **AND** its generated Codex config MUST include only selected project servers

#### Scenario: Runtime credential name is requested

- **WHEN** an MCP definition includes a known provider/subscription credential
  name in `env` or `env_vars`
- **THEN** validation MUST reject the definition, even if the name appears in
  `AI_TEAM_HARNESS_ENV_ALLOW`

#### Scenario: Non-secret host setting is requested

- **WHEN** an MCP server names a non-credential host variable in `env_vars`
- **THEN** the variable MUST be present and allowed through both the MCP
  definition and `AI_TEAM_HARNESS_ENV_ALLOW`
- **AND** the server MUST receive only those selected variables and the fixed
  minimal runtime environment

### Requirement: MCP processes stop with the agent invocation

On stage timeout or cancellation, the controller MUST give Codex a bounded
graceful shutdown window to stop its MCP child processes, then force-kill and
wait for the supervised process tree. Normal completion MUST also run temporary
config/home cleanup.

#### Scenario: Stage cancellation

- **WHEN** an agent invocation is canceled while its MCP tools are active
- **THEN** the supervisor MUST signal Codex, wait the configured grace
  interval, force-kill remaining processes, and await Codex completion
- **AND** temporary Codex and MCP homes MUST be removed
