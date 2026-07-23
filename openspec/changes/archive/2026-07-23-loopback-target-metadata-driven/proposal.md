## Why

Agent-registry validation (`pkg/agent/registry.go`) is metadata-driven
throughout — `Kind`/`Mutation` invariants, `allowed_paths`, verdict
contracts — with one exception: when a stage's `loopback_to` isn't set
explicitly, the default target was the string literal `"coder"`
(`pkg/pipeline/pipeline.go`). For a pipeline whose source-writing stage is
named anything else, loopback silently never triggers — no error, no
warning, it just doesn't retry. Independent audit Finding 12 (Medium),
found independently by two workstreams (core-architecture review and the
gates/mutation-testing pass).

## What Changes

- Default loopback target (used only when `loopback_to` is unset) is now
  the closest preceding stage whose definition declares `mutation: source`,
  rather than a name lookup for the literal string `"coder"`.
- Explicit `loopback_to: <name>` is unaffected — exact-name lookup still
  applies when a stage names its target explicitly.

## Capabilities

### Modified Capabilities
- `workflow-loopback`: clarifies that the *default* (unset `loopback_to`)
  target is chosen by mutation metadata, not by the name "coder" — the
  existing coder-named scenarios remain accurate for the default pipeline,
  where the source-writing stage happens to be named "coder".

## Impact
- `pkg/pipeline/workflow.go` (new `defaultLoopbackTarget`)
- `pkg/pipeline/pipeline.go` (`enforce`, wiring)
