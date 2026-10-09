# Stage-scoped MCP servers for Codex agents (B-85)

## Why

Agents need project knowledge and post-deployment observation data from existing
company tools. Loading a user's global MCP configuration would silently widen
the agent's tool access and could expose provider credentials to unrelated
servers.

## Scope

- Define local stdio MCP servers in the project schema-v5 configuration and
  select server IDs on individual stages.
- Support the allowlist only for Codex, whose adapter creates a private
  per-invocation config. Keep OpenCode/Claude MCP disabled.
- Restrict command paths, argv, environment names and sizes, server counts, and
  Codex startup/tool timeouts. Never load user/global/project Codex MCP settings.
- Keep subscription auth for Codex CLI only; deny known credential-like env
  names from MCP server configuration and inherited env forwarding.
- Document the trusted-local limitation and scenarios for knowledge context
  and observation monitoring.

## Acceptance criteria

- A stage receives only the MCP servers it explicitly names; an unknown server
  or a non-Codex runtime fails before the model starts.
- A Codex run's generated config excludes unselected and user/global servers.
- MCP servers do not receive provider or subscription credential environment
  variables; explicit non-secret host variables require both allowlists.
- Startup, tool calls, and cancellation have bounded shutdown behavior.
- Negative and isolation regressions pass, and security/config docs match the
  implemented boundary.
