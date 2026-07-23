## 1. Implementation

- [x] 1.1 `lock_other.go`: write pid file on acquire, `reclaimStaleLock` on definitive-death evidence only
- [x] 1.2 `lock_other.go` `Close`: `os.RemoveAll` (was `os.Remove`, which would have failed once a pid file lives inside the lock dir)
- [x] 1.3 `run_other.go`: `killTree` via `taskkill /T /F`, falling back to single-process `Kill`

## 2. Verification

- [x] 2.1 Cross-compile `GOOS=windows GOARCH=amd64` and `GOOS=windows GOARCH=arm64`: `go build ./...`, `go vet`
- [x] 2.2 Cross-compile `GOOS=plan9 GOARCH=amd64`: `go build`, `go vet` (same negated build tag also covers this platform)
- [x] 2.3 Cross-compiled test binaries (`go test -c`) for both packages under `GOOS=windows` — compiles, not executed (no Windows machine available)
- [x] 2.4 New tests for `reclaimStaleLock`: live pid (current test process) left alone, missing pid file left alone, unparseable pid left alone
- [x] 2.5 New tests for `AcquireWorkspaceLock`: pid recorded correctly, concurrent acquire against a live holder fails, re-acquire after clean `Close` succeeds
- [x] 2.6 New test for `Run`/`killTree`: process actually terminated promptly after context cancellation
- [x] 2.7 Confirmed the existing darwin build/test suite is completely unaffected (these files never compile on darwin)

**Explicitly not done**: execution on a real Windows or plan9 machine — none available in this environment. This is cross-compilation and code-review confidence only, not run-time confidence.
