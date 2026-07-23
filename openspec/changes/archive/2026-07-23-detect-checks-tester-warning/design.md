## Context

`cmdInit` doesn't load an agent registry — it works from `config.Default()`
alone, which has no `mutation`/verdict metadata (that only exists on the
full `agent.Agent` definition). A fully metadata-driven fix would mean
loading a registry inside `init`, a larger change than this warning gap
warrants.

## Goals / Non-Goals

**Goals:** a user who renamed the tester stage gets a specific, actionable
message instead of a generic one that implies their project type wasn't
recognized at all.

**Non-Goals:** making detection itself metadata-driven (tracked as a
follow-on if it recurs elsewhere; out of scope for this narrow fix).

## Decisions

Changed `ApplyDetectedChecks`'s signature from a single `string` return to
`(profile, warning string)` rather than adding a third out-parameter or a
struct — small enough call-site surface (exactly one caller) that a plain
second return value is simplest.

## Risks / Trade-offs

None — purely additive information; the existing single-return behavior's
callers (all in this repo) are updated in the same change.
