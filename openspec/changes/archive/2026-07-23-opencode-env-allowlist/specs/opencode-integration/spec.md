## ADDED Requirements

### Requirement: Subprocess environment isolation
The opencode subprocess MUST receive only an explicit allow-list of
environment variables — a fixed baseline of standard OS/locale/session
variables, plus any variable name explicitly opted in via
`AI_TEAM_OPENCODE_ENV_ALLOW` — rather than the calling process's full
environment.

#### Scenario: Unlisted variable
- **WHEN** the process invoking `ai-team run` has an environment variable set that is not in the baseline and not named in `AI_TEAM_OPENCODE_ENV_ALLOW`
- **THEN** the opencode subprocess MUST NOT receive that variable

#### Scenario: Explicitly allowed variable
- **WHEN** a variable name is listed in `AI_TEAM_OPENCODE_ENV_ALLOW`
- **THEN** the opencode subprocess MUST receive that variable's current value from the calling process's environment

#### Scenario: Baseline variables always present
- **WHEN** the opencode subprocess is started
- **THEN** it MUST receive `PATH` and `HOME` regardless of any allow-list configuration
