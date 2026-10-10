# Stage-scoped MCP servers for Codex agents — tasks

- [x] Add strict schema-v5 MCP definitions and stage-level server selection.
- [x] Validate server IDs, command paths, argv/env bounds, credentials, server
      count, runtime selection, and timeouts.
- [x] Pass only the stage-selected server definitions to Codex's ephemeral
      session config; isolate MCP HOME and retain Codex subscription auth.
- [x] Add bounded Codex shutdown grace and supervised process-tree cleanup.
- [x] Add config/runtime/process regressions for isolation, refusal paths,
      credentials and cancellation.
- [x] Update security/config documentation and OpenSpec requirements.
- [ ] Run `GOTOOLCHAIN=go1.26.9 make verify` on final commit SHA and request
      independent exact-SHA review before PR.
