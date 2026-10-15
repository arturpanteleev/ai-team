package dockerrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/process"
)

// Result is what a container run observed. Err is nil only when the program
// exited with code 0; every other end sets Err.
type Result struct {
	Stdout, Stderr []byte
	// ExitCode is the exit code of `docker run` (the container's own code, or
	// 125/126/127 when docker or the runtime failed), -1 when docker itself did
	// not run to an exit.
	ExitCode       int
	OutputExceeded bool
	Err            error
	// Detail explains the end in words for the reason shown to people: which
	// class of failure it was and whether the container was removed.
	Detail string
	// Docker is the full `docker run` argv, recorded as evidence.
	Docker []string
}

// Runner runs Specs with the docker CLI.
type Runner struct {
	// Binary is the docker CLI; defaults to "docker" looked up in PATH.
	Binary string
	// ClientEnv is the environment of the docker CLI process (how it reaches
	// the daemon). It never reaches the container. Defaults to the docker
	// client variables of the controller's environment (PATH, HOME, DOCKER_*,
	// XDG_*).
	ClientEnv func() []string
	// CleanupTimeout bounds `docker rm -f`; defaults to 30 seconds.
	CleanupTimeout time.Duration
}

func (r Runner) binary() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "docker"
}

func (r Runner) clientEnv() []string {
	if r.ClientEnv != nil {
		return r.ClientEnv()
	}
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if ClientEnvName(name) {
			env = append(env, entry)
		}
	}
	return env
}

// Run starts the container, feeds stdin, collects at most maxOutput bytes of
// each output stream, and always leaves no container behind: when ctx ends,
// the output limit is hit or the run breaks, the container is removed by name.
func (r Runner) Run(ctx context.Context, spec Spec, stdin []byte, maxOutput int64) Result {
	args, err := spec.RunArgs()
	if err != nil {
		return Result{ExitCode: -1, Err: err, Detail: "container not started: invalid specification"}
	}
	result := Result{Docker: append([]string{r.binary()}, args...)}

	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &limited{limit: maxOutput, cancel: cancel}
	stderr := &limited{limit: maxOutput, cancel: cancel}

	command := exec.Command(r.binary(), args...) // #nosec G204 -- argv is built from a validated Spec and run without a shell
	command.Env = append(r.clientEnv(), spec.Env...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 2 * time.Second

	runErr := process.Run(inner, command)
	_ = process.KillGroup(command)
	result.Stdout, result.Stderr = stdout.bytes(), stderr.bytes()
	result.OutputExceeded = stdout.over() || stderr.over()
	result.Err = runErr
	if runErr == nil {
		return result
	}
	result.ExitCode = -1
	var exit *exec.ExitError
	var pathErr *exec.Error
	switch {
	case errors.As(runErr, &exit):
		result.ExitCode = exit.ExitCode()
	case errors.As(runErr, &pathErr) || errors.Is(runErr, os.ErrNotExist):
		result.Detail = fmt.Sprintf("docker CLI %q could not be started (is Docker installed and on PATH?)", r.binary())
		return result
	}
	// The docker CLI is gone but the container may not be (the CLI is killed
	// on cancellation or timeout and does not forward SIGKILL). Remove it by
	// name; "no such container" means it was already gone.
	removed, failed := r.remove(spec.Name)
	interrupted := ctx.Err() != nil || inner.Err() != nil
	if !interrupted && !failed {
		removed = "" // the container ended on its own and --rm took it; nothing to report
	}
	result.Detail = joinDetail(exitDetail(result.ExitCode, interrupted), removed)
	return result
}

// remove force-removes the container and reports the outcome in words; failed
// is true when the container may still exist.
func (r Runner) remove(name string) (message string, failed bool) {
	timeout := r.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, r.binary(), "rm", "-f", name) // #nosec G204 -- name is validated
	command.Env = r.clientEnv()
	out, err := command.CombinedOutput()
	switch {
	case err == nil:
		return "container " + name + " removed", false
	case strings.Contains(strings.ToLower(string(out)), "no such container"):
		return "container " + name + " already gone", false
	default:
		return fmt.Sprintf("container %s could NOT be removed (%v); remove it manually", name, err), true
	}
}

// exitDetail classifies docker run exit codes. 125, 126 and 127 are reserved
// by docker for its own failures but a program may also use them, so the
// wording stays a hint.
func exitDetail(code int, interrupted bool) string {
	switch {
	case interrupted:
		return ""
	case code == 125:
		return "docker could not run the container (daemon unreachable, image not found or pull failed, or invalid options)"
	case code == 126:
		return "the container command was found but cannot be executed"
	case code == 127:
		return "the container command was not found in the image"
	case code == 137:
		return "the container was killed (SIGKILL: out of memory limit or external kill)"
	case code == 139:
		return "the container program crashed (SIGSEGV)"
	case code < 0:
		return "docker CLI ended without an exit code"
	}
	return ""
}

func joinDetail(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

// limited keeps at most limit bytes and cancels the run when more arrive. It
// reports full writes so the CLI is stopped by the kill, not by a broken pipe.
type limited struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	exceeded bool
	cancel   context.CancelFunc
}

func (w *limited) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	room := w.limit - int64(w.buf.Len())
	if int64(len(p)) > room {
		if room > 0 {
			w.buf.Write(p[:room])
		}
		if !w.exceeded {
			w.exceeded = true
			w.cancel()
		}
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *limited) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

func (w *limited) over() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exceeded
}
