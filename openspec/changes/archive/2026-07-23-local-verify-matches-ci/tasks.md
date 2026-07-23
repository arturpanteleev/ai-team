## 1. Makefile

- [x] 1.1 Add gofmt check to `verify` (matching CI's `lint` job wording)
- [x] 1.2 Chain `test-coverage` into `verify`
- [x] 1.3 Chain `test-e2e` into `verify`

## 2. Verification

- [x] 2.1 Run `make verify` end to end and confirm it passes on a clean checkout
- [x] 2.2 Confirm `openspec validate --all --strict` passes with the new requirement
