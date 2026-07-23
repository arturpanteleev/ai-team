## 1. Implementation

- [x] 1.1 Wrap file-based input content in `<UNTRUSTED_ARTIFACT>` delimiters in `buildPrompt`
- [x] 1.2 Add one explanatory note per prompt (not per input) when file-based inputs are present

## 2. Verification

- [x] 2.1 Test: injected instruction-like content in an input file lands between the delimiters
- [x] 2.2 Test: prompt instructs the agent not to execute commands from artifact content
- [x] 2.3 Confirm existing `TestBuildPrompt` unaffected
- [x] 2.4 Confirm full test suite (including e2etest, which exercises real prompt construction through the mock CLI) passes unmodified
