## Why

`OpenCodeIsolationEnvironment` passes the calling process's *full* environment
to the `opencode` subprocess, minus 7 opencode/XDG-specific keys. `.env`
**files** are correctly denied via the read-permission policy, but any
secret exported as a process environment variable by whoever runs
`ai-team run` (cloud credentials, CI tokens, database URLs) reaches the
subprocess unfiltered. Independent audit Finding 13 (Medium): the isolation
harness is otherwise well-designed (default-deny bash/network/task,
`.env`/`.git`/`.ai-team` read denial, refuses to run if the target project
supplies its own `opencode.json`/plugins), but starts from an implicit
full-trust baseline rather than an explicit one.

## What Changes

- Replace the deny-list (full environment minus ~7 keys) with an allow-list:
  only a fixed baseline of standard OS/locale/session variables (`PATH`,
  `HOME`, `LANG`, etc. — see design.md) plus any variable explicitly named
  via `AI_TEAM_OPENCODE_ENV_ALLOW` (comma-separated) reaches the subprocess.
- Any provider credential opencode itself needs (e.g. an LLM API key) must
  now be named explicitly by whoever configures the project, rather than
  leaking implicitly from the calling shell.

## Capabilities

### Modified Capabilities
- `opencode-integration`: adds a subprocess environment isolation
  requirement.

## Impact
- `pkg/runtime/agentcli.go` (`OpenCodeIsolationEnvironment`,
  `withoutEnvironmentKeys` replaced by `allowedEnvironmentKeys` /
  `withAllowedEnvironmentKeys`)
- README: needs a note documenting `AI_TEAM_OPENCODE_ENV_ALLOW` for users
  whose provider setup depends on an environment-variable credential
  (tracked in the docs/onboarding change, not duplicated here)
