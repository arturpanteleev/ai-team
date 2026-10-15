package dockerrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// Labels every container started by Runner carries. LabelOwner marks it as
// ours and LabelStarted holds the unix time of the start, so SweepOrphans can
// find containers whose controller died without removing them. A caller adds
// its own label (the gate adapter uses ai-team.gate-attempt) to sweep only its
// containers.
const (
	LabelOwner   = "ai-team.dockerrun"
	LabelStarted = "ai-team.started"
)

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
	labels := make(map[string]string, len(spec.Labels)+2)
	for key, value := range spec.Labels {
		labels[key] = value
	}
	labels[LabelOwner] = "1"
	labels[LabelStarted] = strconv.FormatInt(time.Now().Unix(), 10)
	spec.Labels = labels
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
	case strings.Contains(strings.ToLower(string(out)), "already in progress"):
		// Another removal (the --rm of the daemon) is under way: the container
		// is on its way out, which is what was asked for.
		return "container " + name + " is already being removed", false
	default:
		return fmt.Sprintf("container %s could NOT be removed (%v); remove it manually", name, err), true
	}
}

// SweepOrphans force-removes containers that carry LabelOwner and labelKey (a
// caller's own label, so callers sweep only what they started) and were
// started at least minAge ago: the leftovers of a controller that was killed
// (kill -9, power loss) before it could remove its container. minAge must
// exceed the longest legitimate run, so a container of another live controller
// on the same daemon is never touched. It returns the removed container IDs
// and the first error; one failed removal does not stop the others. This is
// best effort: it runs when a controller starts, not while one is dead.
func (r Runner) SweepOrphans(ctx context.Context, labelKey string, minAge time.Duration) ([]string, error) {
	if !labelPattern.MatchString(labelKey) {
		return nil, fmt.Errorf("docker: sweep label %q is invalid", labelKey)
	}
	timeout := r.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(listCtx, r.binary(), "ps", "-a", "--no-trunc", // #nosec G204 -- labelKey is validated
		"--filter", "label="+LabelOwner, "--filter", "label="+labelKey,
		"--format", `{{.ID}} {{.Label "`+LabelStarted+`"}}`)
	command.Env = r.clientEnv()
	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("docker: listing containers to sweep: %w", err)
	}
	var removed []string
	var firstErr error
	now := time.Now()
	for _, line := range strings.Split(string(out), "\n") {
		id, started, _ := strings.Cut(strings.TrimSpace(line), " ")
		if id == "" {
			continue
		}
		// A container without a readable start time cannot be told from an old
		// one; it is ours by label, so it is treated as old.
		if unix, perr := strconv.ParseInt(strings.TrimSpace(started), 10, 64); perr == nil && now.Sub(time.Unix(unix, 0)) < minAge {
			continue
		}
		if _, failed := r.remove(id); failed {
			if firstErr == nil {
				firstErr = fmt.Errorf("docker: orphan container %s could not be removed", id)
			}
			continue
		}
		removed = append(removed, id)
	}
	return removed, firstErr
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
