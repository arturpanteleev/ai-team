# coder-stage-independence delta (P1-9)

## ADDED Requirements

### Requirement: Forward-pass coder inputs are declared-only

On a forward graph pass without loopback, the source stage MUST receive exactly
its declared `inputs` and MUST NOT receive any reviewer, tester, or verifier
artifact (`review`, `report`, `verification`), even when that artifact exists on
disk in the artifact root.

#### Scenario: Coder runs forward, reviewer artifact exists

- **WHEN** a forward pipeline runs `analyst → coder → tester → reviewer` so the
  reviewer produces `review.md`
- **THEN** the `coder` stage's runtime input set MUST be exactly `proposal`
- **AND** MUST NOT contain `review`, `report`, or `verification`
- **AND** this MUST hold even though `review.md` exists on disk

### Requirement: Loopback is the only leak channel

A reviewer/tester/verifier artifact MUST reach the source stage's inputs only via
an explicit backward (loopback) graph edge with a persisted human decision. This
MUST be locked by both a negative forward assertion and the existing positive
loopback assertions.

#### Scenario: No loopback, no leak

- **WHEN** no rejected edge routes the reviewer output back to coder
- **THEN** coder MUST NOT receive `review` in any of its forward inputs

#### Scenario: Loopback injects review on retry

- **WHEN** the reviewer rejects and an approved loopback edge returns to coder
- **THEN** the coder's retry input set MUST include `review`

### Requirement: Runtime read denial of control-plane artifacts

The runtime adapter MUST deny reading `.ai-team/**` and allow only explicitly
passed inputs, which MUST be covered by an existing runtime isolation test
(`TestOpenCodeIsolationDeniesEffectsAndNarrowsEdits`).

#### Scenario: Coder attempts to read future artifact from disk

- **WHEN** a coder agent attempts to read a path under `.ai-team/**`
- **THEN** the adapter's read rules MUST deny it
- **AND** only the declared/passed input paths MUST be allowed in read scope
