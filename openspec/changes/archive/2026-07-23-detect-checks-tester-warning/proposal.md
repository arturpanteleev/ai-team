## Why

`ApplyDetectedChecks` looks up the pipeline stage named exactly `"tester"` to
attach detected required checks to. If a project's config renames or removes
that stage, detection silently returns as if nothing was found — the same
generic "unknown stack" warning appears as for a project `ai-team` genuinely
doesn't recognize, even though the stack WAS correctly identified.
Independent audit Finding 14 (Medium).

## What Changes

- `ApplyDetectedChecks` now returns a specific warning when a stack is
  detected but no `"tester"` stage exists to attach checks to, distinct from
  the existing "stack not recognized at all" warning.
- `cmdInit` prints this specific warning instead of the generic one.

## Capabilities

### Modified Capabilities
- `project-init`: adds a distinct warning scenario for "stack detected, no
  eligible stage" (previously indistinguishable from "stack not detected").

## Impact
- `pkg/config/detect.go` (`ApplyDetectedChecks` signature: now returns
  `(profile, warning string)`)
- `cmd/ai-team/main.go` (`cmdInit` call site)
