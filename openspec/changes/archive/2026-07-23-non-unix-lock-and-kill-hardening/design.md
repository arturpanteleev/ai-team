## Context

Both gaps exist because the Unix implementations lean on OS primitives
(`flock` releases automatically on process death; `Setpgid` + group
`SIGKILL` reaches descendants) without a simple equivalent short of a
Windows-specific syscall dependency (Job Objects) or shelling out to a
standard OS utility. This change takes the second path deliberately.

## Goals / Non-Goals

**Goals:** close the gap where safely possible without a real Windows
machine to validate against; never regress current behavior even in the
worst case.

**Non-Goals:** a fully Job-Object-based Windows process tree — requires
`golang.org/x/sys/windows` syscalls this session cannot validate beyond
compiling. Tracked as a follow-on for anyone with a real Windows
environment; `taskkill /T /F` is a reasonable intermediate step.

## Decisions

**Lock staleness — err toward never reclaiming rather than ever
mis-reclaiming.** A false "the old process is dead" conclusion is a
correctness regression far worse than a stuck lock requiring manual
cleanup. `reclaimStaleLock` only acts on `os.FindProcess` returning an
error (on Windows: `OpenProcess` failed, pid doesn't exist) — never on
inconclusive signals (missing/unparseable pid file, a successful
`FindProcess`, which proves nothing on POSIX-like semantics).

**Process tree kill via `taskkill`, not Job Objects.** Stable, decades-old
behavior, easy to reason about correctly without a Windows machine to test
against. Job Objects have subtler failure modes (assignment timing, nested
job semantics varying by Windows version) that would be harder to get right
blind.

## Risks / Trade-offs

**Cannot be executed in this environment.** Verified by cross-compilation
(`GOOS=windows`, `GOOS=plan9`: `go vet`, `go build`, `go test -c`) and code
review only — no substitute for running on a real Windows machine at least
once, stated explicitly rather than glossed over.

**`taskkill` availability.** If missing/blocked by policy on some Windows
configuration, `killTree` falls back to single-process kill silently —
could mask the tree-kill not actually working there. Acceptable versus
erroring out entirely, but worth knowing.
