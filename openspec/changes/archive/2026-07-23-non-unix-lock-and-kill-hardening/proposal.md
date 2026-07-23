## Why

Two of `pkg/evidence`/`pkg/process`'s non-Unix (`_other.go`) fallback
implementations degrade silently compared to their Unix counterparts:

- `pkg/evidence/lock_other.go`'s workspace lock is a bare `os.Mkdir` with no
  PID tracking or staleness detection. A killed/crashed process leaves the
  lock directory stuck forever with no recovery path — `lock_unix.go` uses a
  real `flock`, which the OS releases automatically when the holding process
  dies, so this gap is Unix-fallback-only.
- `pkg/process/run_other.go` kills only the direct child process on
  cancellation/timeout; `run_unix.go` kills the whole process group. A
  check/agent process that spawns children (e.g. a shell script) can leave
  orphaned descendants running past a timeout on the non-Unix path.

Independent audit Findings 6 (High/Medium) and 16 (Low-Medium). Neither path
is exercised by CI (ubuntu-only) or this dev machine (darwin) — both are
Unix-like and always compile the `_unix.go` variant. Verification here is
cross-compilation (`GOOS=windows`, `GOOS=plan9`) plus code review only.

## What Changes

- `AcquireWorkspaceLock` (non-Unix path) writes a pid file into the lock
  directory and, on an existing lock, checks whether that pid still exists
  before refusing to acquire. Reclaims only on strong positive evidence the
  owning process is gone (Windows: `os.FindProcess` itself fails for a dead
  pid) — anything inconclusive leaves the lock in place, identical to today.
- `process.Run` (non-Unix path) attempts `taskkill /T /F` on cancellation
  (terminates the whole tree on Windows), falling back to the previous
  single-process `Kill()` if `taskkill` isn't available.

## Capabilities

### Modified Capabilities
- `run-evidence`: workspace lock gains non-Unix staleness recovery.
- `deterministic-checks`: process cancellation gains non-Unix tree-kill.

## Impact
- `pkg/evidence/lock_other.go`, new `pkg/evidence/lock_other_test.go`
- `pkg/process/run_other.go`, new `pkg/process/run_other_test.go`
- None of these files/tests compile on this repo's dev machine or CI —
  verified via `GOOS=windows`/`GOOS=plan9` cross-compilation only.
