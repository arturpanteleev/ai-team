//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// Note: shares its build tag with run_other.go — cannot be compiled or run
// on this project's development machine (darwin) or in its current CI
// (ubuntu-only runners); verified by cross-compilation
// (`GOOS=windows go vet`, `GOOS=windows go test -c`) and code review.

func TestRunKillsProcessOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// A long-lived command any target OS running this test provides.
	cmd := exec.CommandContext(context.Background(), "cmd", "/C", "ping -n 30 127.0.0.1 >NUL")
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cmd) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation (ctx.Err() at minimum)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return promptly after context cancellation — process likely not killed")
	}
}

// Regression: the graceful path used to signal with Process.Signal(os.Interrupt)
// (unimplemented on Windows, EWINDOWS — no signal delivered) and then wait on
// <-done before force-killing, so a command that outlived the grace window
// blocked the supervisor forever. The force kill (`taskkill /T /F`) must happen
// when the grace timer expires, and RunGraceful must return within context
// deadline + grace + ε.
func TestRunGracefulForceKillsLeaderIgnoringGracefulStop(t *testing.T) {
	const deadlineDelay = 100 * time.Millisecond
	const grace = 300 * time.Millisecond
	const margin = 2 * time.Second // taskkill spawns helper processes; keep CI slack
	ctx, cancel := context.WithTimeout(context.Background(), deadlineDelay)
	defer cancel()
	cmd := exec.Command("cmd", "/C", "ping -n 30 127.0.0.1 >NUL")

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- RunGraceful(ctx, cmd, grace) }()

	bound := deadlineDelay + grace + margin
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline error, got %v", err)
		}
		if elapsed := time.Since(started); elapsed > bound {
			t.Fatalf("RunGraceful took %s, want at most %s (deadline + grace + ε)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("RunGraceful did not return within %s: force kill unreachable after the grace window", bound)
	}
}
