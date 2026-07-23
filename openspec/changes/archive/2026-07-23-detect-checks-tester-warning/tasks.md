## 1. Implementation

- [x] 1.1 `ApplyDetectedChecks` returns `(profile, warning string)` instead of a single `string`
- [x] 1.2 Specific warning when a stack is detected but no `tester` stage exists
- [x] 1.3 `cmdInit` prints the specific warning when present, falling back to the existing generic warning otherwise

## 2. Verification

- [x] 2.1 Updated existing tests for the new two-value return
- [x] 2.2 New test: renamed `tester` stage still detects the stack correctly and produces a specific, distinguishable warning
- [x] 2.3 Manual check with the real built binary against a Go project: still prints the normal success line unchanged
- [x] 2.4 Full test suite passes
