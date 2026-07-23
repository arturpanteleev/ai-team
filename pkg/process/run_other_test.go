//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import (
	"context"
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
