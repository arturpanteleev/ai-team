## Context

The default pipeline's source-writing stage happens to be named "coder",
which made the old hardcoded default look correct for the common case while
silently failing for any renamed or custom role set.

## Goals / Non-Goals

**Goals:** default loopback target selection works for any pipeline
configuration, not just the built-in role names.

**Non-Goals:** changing explicit `loopback_to` behavior (already
name-exact, already correct) or adding new configuration surface.

## Decisions

Search backward from the current stage for the closest preceding stage
whose loaded definition has `Mutation == "source"`, reusing the registry
`Load` the pipeline already has rather than requiring a fully pre-resolved
stage list. `mutation: source` is the existing, already-declared signal for
"this stage writes source code" — no new metadata field needed.

## Risks / Trade-offs

A pipeline with more than one `mutation: source` stage before the
verdict-bearing stage will loop back to whichever is closest, which matches
the old literal-name behavior's implicit assumption (there was only ever
one "coder") reasonably well; an explicit `loopback_to` remains available
for anyone who needs a different target.
