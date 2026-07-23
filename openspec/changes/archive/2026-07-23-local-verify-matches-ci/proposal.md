## Why

`make verify` is the only documented "run everything before you push" command
(README's "Разработка и verification" section). It silently diverges from
what CI actually enforces: it never runs `gofmt` (only CI's `lint` job does),
never runs the 60%/50% coverage gates (only the separate, uninvoked
`test-coverage` target does), and never runs e2e tests (only the separate,
uninvoked `test-e2e` target does). A contributor who trusts a green
`make verify` can still fail CI on any of these three, and independently, CI
never runs standalone as one command a contributor can reproduce locally
either. Independent audit Finding 8 (High).

## What Changes

- `make verify` gains a gofmt check (matching CI's exact wording) and chains
  `test-coverage` and `test-e2e` as prerequisites, so it exercises everything
  the `build`/`lint`/`unit-tests`/`race-tests`/`e2e-tests` CI jobs do.
- `ci-pipeline` spec gains an explicit local/CI parity requirement so this
  doesn't silently drift again.

## Capabilities

### Modified Capabilities
- `ci-pipeline`: adds a requirement that the local verify command exercises
  the same checks as CI.

## Impact
- `Makefile` (`verify` target only; no other target's behavior changes)
- `openspec/specs/ci-pipeline/spec.md`
