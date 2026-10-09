# Stage-scoped MCP servers for Codex agents — design

## Configuration

Schema v5 gains a top-level `mcp_servers` map. A definition contains an
absolute executable path, fixed argv, non-secret literal environment values,
explicit non-secret `env_vars` names, and bounded startup/tool timeouts. Each
`stages[]` entry may select up to four named servers. A selection requires an
agent and `cli: codex`; config validation rejects unknown IDs and malformed
definitions. No MCP entries are synthesized by default.

## Runtime boundary

The pipeline resolves MCP definitions from the stage ID and passes copies to
that one runtime invocation. The Codex adapter writes only those selected
entries into its fresh 0700 `CODEX_HOME`. It does not read or merge the user's
Codex config, and the existing project `.codex/config.toml` execution-surface
guard remains active. Codex's existing subscription `auth.json` copy stays in
the Codex home; each server receives a separate empty temporary `HOME`.

The implementation supports only stdio MCP servers. The command is an absolute
path and is executed as argv without a shell. `env_vars` names must also be
allowed through `AI_TEAM_HARNESS_ENV_ALLOW` and be present at runtime. Both
literal and inherited environment settings reject known credential-like names
and runtime control variables. A separate service-credential broker is outside
this change and credentials fail closed.

The default server startup/tool limits are 10/60 seconds, with ceilings 60/300
seconds. At stage timeout or cancellation, Codex receives SIGTERM and a two
second grace period to stop its MCP children; the process supervisor then
force-kills remaining processes and waits for Codex. This is process lifecycle control,
not an OS security boundary: local MCP commands still run as the current OS user.

## Validation

Unit tests cover config selection and invalid references, server command/env
denials, generated per-stage TOML, user/global config exclusion, independent
temporary homes, provider-secret denial, env allowlist requirements, and
graceful cancellation. Full repository verification uses the pinned Go toolchain.
