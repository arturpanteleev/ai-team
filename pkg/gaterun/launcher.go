package gaterun

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/process"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// LaunchSpec is everything a Launcher needs to run one gate program. It is
// deliberately independent of the protocol: a launcher moves bytes, it does
// not interpret them.
type LaunchSpec struct {
	// Argv is the program and its arguments. Never interpreted by a shell.
	Argv []string
	// Dir is the absolute working directory.
	Dir string
	// Env is the complete environment of the program (not merged with the
	// controller's). Entries are NAME=value.
	Env []string
	// Stdin is the input document.
	Stdin []byte
	// MaxOutputBytes bounds stdout and stderr each. Exceeding it must stop the
	// program and set Outcome.OutputExceeded.
	MaxOutputBytes int64
	// Input is the decoded protocol input, for adapters that must rewrite
	// paths (a container adapter mounts directories elsewhere).
	Input Input
	// Docker carries the container settings of a docker_program gate; nil for
	// host gates. A launcher that cannot honor them must refuse the run.
	Docker *DockerSettings
}

// Outcome is what a Launcher observed. Err is nil only when the program
// exited with code 0 on its own; any other end (non-zero code, signal, start
// failure, kill by cancellation or by the output limit, descendants holding
// the pipes open) sets Err. The runner decides what Err means from the
// context and the other fields.
type Outcome struct {
	Stdout, Stderr []byte
	// ExitCode is the process exit code, or -1 when it did not exit normally.
	ExitCode       int
	OutputExceeded bool
	Err            error
	// Detail explains the end in words (docker failure class, container
	// removal); the runner appends it to the reason.
	Detail string
	// Record is launcher-specific evidence stored in invocation.json under
	// "launcher": what the program was allowed to see. Names only, no values.
	Record map[string]any
}

// Launcher runs one gate program. The host adapter below runs an argv on the
// controller's machine; a Docker adapter (S08) implements the same interface
// and speaks the same protocol.
type Launcher interface {
	// Kind is the gate kind recorded as GateAttempt.Executor.
	Kind() workflow.GateKind
	// Launch runs the program until it exits or ctx ends. It must stop the
	// whole process tree when ctx ends.
	Launch(ctx context.Context, spec LaunchSpec) Outcome
}

// HostLauncher runs the argv directly on the host without a shell, in its own
// process group (killed as a group on cancellation), with exactly the
// environment from the spec. Whatever is left of the process group after the
// program exits (background descendants) is killed.
type HostLauncher struct{}

// Kind implements Launcher.
func (HostLauncher) Kind() workflow.GateKind { return workflow.GateKindExternal }

// descendantGrace bounds how long Wait lingers for output pipes held by
// descendants after the program itself has exited.
const descendantGrace = 2 * time.Second

// Launch implements Launcher.
func (HostLauncher) Launch(ctx context.Context, spec LaunchSpec) Outcome {
	if len(spec.Argv) == 0 {
		return Outcome{ExitCode: -1, Err: errors.New("empty argv")}
	}
	if spec.Docker != nil {
		return Outcome{ExitCode: -1, Err: errors.New("host launcher refuses a docker gate: the container settings would be ignored")}
	}
	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &capWriter{limit: spec.MaxOutputBytes, cancel: cancel}
	stderr := &capWriter{limit: spec.MaxOutputBytes, cancel: cancel}

	command := exec.Command(spec.Argv[0], spec.Argv[1:]...) // #nosec G204 -- argv comes from trusted repository configuration
	command.Dir = spec.Dir
	command.Env = append([]string{}, spec.Env...)
	command.Stdin = bytes.NewReader(spec.Stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = descendantGrace

	err := process.Run(inner, command)
	// process.Run kills the group only when ctx ends. A program that exits on
	// its own may still have left background descendants: no gate leaves
	// processes behind, so the group is killed after every end. Their pipe
	// holding was already bounded by WaitDelay.
	_ = process.KillGroup(command)
	outcome := Outcome{
		Stdout:         stdout.bytes(),
		Stderr:         stderr.bytes(),
		OutputExceeded: stdout.over() || stderr.over(),
		Err:            err,
	}
	if err == nil {
		return outcome
	}
	outcome.ExitCode = -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		outcome.ExitCode = exit.ExitCode()
	}
	return outcome
}

// capWriter keeps at most limit bytes and cancels the run when more arrive.
// It always reports full writes so the child is stopped by the kill, not by a
// broken pipe it could interpret as something else.
type capWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	exceeded bool
	cancel   context.CancelFunc
}

func (w *capWriter) Write(p []byte) (int, error) {
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

func (w *capWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

func (w *capWriter) over() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exceeded
}
