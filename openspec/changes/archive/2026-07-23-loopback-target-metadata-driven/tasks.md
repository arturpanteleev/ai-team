## 1. Implementation

- [x] 1.1 Add `defaultLoopbackTarget` (pkg/pipeline/workflow.go), searching by `mutation: source` metadata
- [x] 1.2 Wire into `enforce`'s default (unset `loopback_to`) path only; explicit `loopback_to` unaffected

## 2. Verification

- [x] 2.1 Unit test for `defaultLoopbackTarget` directly
- [x] 2.2 End-to-end pipeline test with a renamed source-writing stage ("implementer" instead of "coder"), no explicit `loopback_to`, confirming loopback triggers
- [x] 2.3 Confirmed the new end-to-end test fails against the old hardcoded-"coder" behavior (reverted temporarily to verify), then restored the fix
- [x] 2.4 Existing loopback/retry tests (which use the standard "coder"-named registry) unaffected
