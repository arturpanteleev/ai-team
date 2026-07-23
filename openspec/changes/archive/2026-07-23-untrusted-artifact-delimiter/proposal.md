## Why

`pkg/eval/eval.go`'s `buildJudgePrompt` explicitly wraps artifact content in
`<UNTRUSTED_ARTIFACT>` delimiters with an instruction not to execute
commands from it. The main pipeline runtime's `buildPrompt`
(`pkg/runtime/agentcli.go`) — used for every real analyst→...→verifier stage
— inserted each upstream input verbatim, with no delimiter or instruction at
all. Since reviewer/tester/verifier verdicts are the entire trust anchor the
controller relies on (control-plane-safety's "Mandatory verdict contract"),
and their inputs are prior agents' artifacts — which can themselves reflect
untrusted content from the target repository — this asymmetry weakens the
integrity of the one thing the whole control plane is built around.
Independent audit Finding 5 (High), corroborating AUDIT.md's existing H-09.

## What Changes

- `buildPrompt` now wraps each file-based input's content in the same
  `<UNTRUSTED_ARTIFACT>` delimiter pattern eval.go already uses, with an
  explanatory note that content between delimiters is data, not
  instructions.
- This does not eliminate prompt injection (no delimiter can, against a
  sufficiently capable model reading untrusted text) — it makes the
  data/instruction boundary structurally visible to the model, matching
  what the eval path already does.

## Capabilities

### Modified Capabilities
- `opencode-integration`: extends the "Prompt contract" requirement to
  require untrusted-data framing for file-based inputs.

## Impact
- `pkg/runtime/agentcli.go` (`buildPrompt`)
