## Context

`OpenCodeIsolationEnvironment` already redirects `XDG_CONFIG_HOME` to a fresh
temp directory per invocation, so opencode's own persisted config/auth under
the user's real `~/.config/opencode` is already invisible to this call
regardless of environment filtering. Whatever authentication opencode
actually uses in this setup must therefore already flow through an
environment variable (or be a no-auth/local setup) — meaning a naive,
maximally-restrictive allow-list risks breaking real usage silently.

## Goals / Non-Goals

**Goals:** default to passing nothing beyond standard OS/locale/session
plumbing; make any credential passthrough an explicit, visible, per-project
opt-in instead of an implicit accident of "whatever the deny-list didn't
name."

**Non-Goals:** Not trying to guess or auto-detect which provider credential
env var a given opencode setup needs — that's project-specific and the
person configuring it knows better than a hardcoded guess would.

## Decisions

Baseline allow-list: `PATH, HOME, USER, LOGNAME, SHELL, PWD, LANG, LC_ALL,
LC_CTYPE, TMPDIR, TERM, COLORTERM, NO_COLOR`. These are standard, essentially
never secret, and commonly required for a CLI tool to run and produce sane
output at all.

Extension mechanism: `AI_TEAM_OPENCODE_ENV_ALLOW` (comma-separated variable
names, not values) lets a project or user opt specific additional variables
through — e.g. `AI_TEAM_OPENCODE_ENV_ALLOW=ANTHROPIC_API_KEY`. Chose an
environment variable over a `config.yaml` field to avoid a schema/migration
change for what is fundamentally a local, per-machine trust decision (the
allow-list itself never needs to be committed to the repo or shared).

## Risks / Trade-offs

If a real opencode setup depends on some environment variable not in the
baseline and the user hasn't set `AI_TEAM_OPENCODE_ENV_ALLOW`, `ai-team run`
may start failing where it previously worked (e.g. opencode can't
authenticate). This is the correct trade-off — implicit credential
passthrough is exactly the problem being fixed — but it's a real, visible
behavior change or existing users, worth flagging prominently in the
CHANGELOG/README rather than only in this spec.
