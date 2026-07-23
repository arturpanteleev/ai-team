## Context

CI runs 8 separate jobs; the Makefile has `verify` as the closest local
equivalent, but it was assembled incrementally as CI grew and never
re-synced. There's no distributed-systems complexity here — it's a
straightforward gap between two lists of shell commands.

## Goals / Non-Goals

**Goals:** `make verify` exercises everything CI's `lint`, `unit-tests`,
`race-tests` and `e2e-tests` jobs check, so a green local `verify` is a
reliable predictor of a green CI run.

**Non-Goals:** Not attempting perfect environment parity (CI runs on
`ubuntu-latest`; this only aligns which *commands* run, not the OS). Not
adding a new tool/dependency — everything needed already exists as other
Makefile targets.

## Decisions

Chain the existing `test-coverage` and `test-e2e` targets as recipe steps in
`verify` via `$(MAKE)` rather than duplicating their logic — one source of
truth for the coverage-gate math and the e2e invocation. Add the gofmt check
inline in `verify` (mirroring CI's `lint` job text exactly) rather than a new
Makefile target, since nothing else needs to invoke gofmt standalone today.

## Risks / Trade-offs

`make verify` gets slower (adds a coverage run and an e2e run on top of what
it already did). This is the intended trade-off — the whole point is that it
should take as long as it needs to actually mean something.
