## Context

Two prompt-construction paths exist: `pkg/eval` (the LLM-quality judge,
advisory only) and `pkg/runtime/agentcli.go` (the main pipeline, whose
verdicts are load-bearing). Only the first marked artifact content as
untrusted data before this change.

## Goals / Non-Goals

**Goals:** close the specific asymmetry — same delimiter convention on both
paths.

**Non-Goals:** solving prompt injection generally. Reviewer/tester/verifier
outputs are still ultimately parsed as text by `pkg/verdict`; a delimiter is
a defense-in-depth signal to the model, not a hard control boundary. The
hard boundary is what the controller already does independent of model
behavior: fail-closed verdict parsing, required deterministic checks that
don't trust LLM-claimed pass/fail, and mutation-scope enforcement.

## Decisions

Reused the exact `<UNTRUSTED_ARTIFACT>` tag and framing language from
`eval.go` rather than inventing a new convention, and added the explanatory
note once per prompt (not once per input) to avoid repeating the same
boilerplate for agents with many inputs.

## Risks / Trade-offs

None — purely additive to the prompt text; no behavior change for
non-adversarial artifact content, confirmed by the existing `TestBuildPrompt`
and full e2etest suite passing unmodified.
