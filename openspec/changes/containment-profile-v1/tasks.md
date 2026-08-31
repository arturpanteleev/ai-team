## Tasks

### T1: Containment receipt types in pkg/containment

Create `pkg/containment/containment.go`:
- `Axis` type (fs/net/proc/env string constants)
- `Level` type (ENFORCED/PARTIAL/UNAVAILABLE)
- `Receipt` struct: `{Axes map[Axis]Level, Details map[Axis]map[string]bool, Profile string}`
- `DefaultTrustedLocalReceipt()` — returns receipt for trusted-local profile
- `Receipt.Validate()` — fail-closed: unknown axis/level → error
- `Receipt.MarshalJSON()` / `UnmarshalJSON()` — strict decode

### T2: Containment config in pkg/config

Add to `Config` struct:
```go
type ContainmentConfig struct {
    Profile string `json:"profile" yaml:"profile"`
}
```
Default: `trusted-local`. Validate: only `trusted-local` or `strict` accepted.
Unknown → error. Add to `Config.Validate()`.

### T3: Env containment tightening in pkg/runtime

Extend opencode/codex/claude adapters:
- Add `.ssh/`, `.aws/`, `.gnupg/`, `credentials` to read-deny patterns
- Add `.env` (exact) to read-deny (already present in some adapters)
- Document in env isolation details

### T4: Process cleanup verification in pkg/process

Add `TrackAndCleanup(pids []int) CleanupReceipt`:
- Send SIGKILL to process group (existing code)
- Wait up to 2 seconds for each tracked PID
- Return `CleanupReceipt{Verified bool, PIDs []int, Timeout bool}`

### T5: Receipt generation in pkg/pipeline/finalize.go

In `finalize()`, after writing usage envelope:
- Build receipt from containment profile (config) + process cleanup result
- Write `{RunDir}/containment.json` (sibling of `usage.json`; `run.json`
  immutable с момента Start, поэтому не поле в manifest, а отдельный файл)

### T6: (merged into T5)

Receipt persists as separate `containment.json` evidence file, not a field in
immutable `RunManifest`. Legacy runs without the file are valid — verify/gate
treat axes as `UNAVAILABLE` without failing.

### T7: CLI receipt display

- `cmd/ai-team/usage.go`: print containment receipt in usage summary
- `cmd/ai-team/verify.go`: print receipt in verification output

### T8: Gate allow-untrusted receipt check

- `pkg/gate/gate.go`: when `AllowUntrusted=true`, read containment receipt
  from run evidence and check all axes ≠ UNAVAILABLE
- If receipt absent and profile unknown → allow (backward compat)
- If receipt has UNAVAILABLE axis → BLOCKED

### T9: Documentation

- `docs/ARCHITECTURE.md`: new section "Containment threat model"
- `README.md`: update "Граница безопасности" section
- `TASKLOG.md`: add row, update counts
- `ai-team-backlog.html`: move #18 to Review

### T10: Tests

- `pkg/containment/containment_test.go`: receipt validation, roundtrip, default
- `pkg/config/config_test.go`: containment config validation
- `pkg/process/*_test.go`: cleanup verification mock
- Pipeline test: receipt generated in finalize
- E2E: verify receipt appears in usage output
