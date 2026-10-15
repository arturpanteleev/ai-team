// Package stageexec defines the executor of one workflow stage: the thing that
// produces the stage result for the controller. It is deliberately separate
// from the gate runner: a gate checks a result, an executor creates it.
//
// Two implementations exist: InternalAgent wraps the in-process LLM runtime and
// External runs a trusted program from the config under a JSON contract. The
// Docker executor builds on the same contract by supplying its own Launcher.
//
// An executor never decides anything about the workflow graph. It only writes
// the declared outputs; the controller then checks result type, mutation scope
// and verdict exactly as for the internal agent, and records which executor
// actually ran in the attempt evidence.
package stageexec

import (
	"context"
	"errors"

	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

// Executor kinds recorded in attempt evidence.
const (
	KindInternalAgent = "internal_agent"
	KindExternal      = "external"
)

// ErrTimeout reports that an executor exceeded its own time limit while the
// surrounding stage context was still alive. It is an execution failure, never
// a completed stage.
var ErrTimeout = errors.New("stage executor timeout")

// Identity names the executor that actually ran an attempt.
type Identity struct {
	Kind string `json:"kind"`
	// Name is the adapter name for external executors and the runtime type
	// for the internal agent.
	Name string `json:"name,omitempty"`
}

// Output is one declared stage output with its resolved absolute path.
type Output struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Request is everything the controller hands to an executor for one attempt.
type Request struct {
	RunID     string
	AttemptID string
	Stage     string
	Feature   string
	Task      string
	// Instructions is the stage contract prompt from the agent definition.
	Instructions string
	WorkDir      string
	ArtifactRoot string
	LogDir       string
	// Inputs are the declared stage inputs (immutable evidence snapshots).
	Inputs []runtime.Artifact
	// Outputs are the declared outputs; the executor must produce them.
	Outputs      []Output
	Mutation     string
	AllowedPaths []string
}

// Result is the provenance an executor reports back. Outputs are not part of
// it: they live on disk and the controller collects them by contract.
type Result struct {
	Identity Identity
	Summary  string
	// Verdict is the verdict the executor claims; the controller cross-checks
	// it against the verdict parsed from the outputs and never trusts it alone.
	Verdict  string
	Metadata map[string]string
	// LogPath is the redacted executor log, empty when logging is off.
	LogPath string
}

// StageExecutor produces the result of one stage attempt. Execute returns a
// best-effort Result even on error so failed attempts keep their provenance.
type StageExecutor interface {
	Identity() Identity
	Execute(ctx context.Context, request Request) (Result, error)
}

// InternalAgent adapts the existing runtime.Runtime to StageExecutor without
// changing how the runtime is invoked.
type InternalAgent struct {
	runtime runtime.Runtime
	agent   *runtime.Agent
	task    *runtime.Task
}

// NewInternalAgent wraps a prepared runtime invocation.
func NewInternalAgent(rt runtime.Runtime, agent *runtime.Agent, task *runtime.Task) *InternalAgent {
	return &InternalAgent{runtime: rt, agent: agent, task: task}
}

// Identity reports the runtime that serves the agent.
func (e *InternalAgent) Identity() Identity {
	name := ""
	if e.agent != nil {
		name = e.agent.RuntimeType
	}
	return Identity{Kind: KindInternalAgent, Name: name}
}

// Execute runs the wrapped runtime with the request inputs.
func (e *InternalAgent) Execute(ctx context.Context, request Request) (Result, error) {
	result := Result{Identity: e.Identity()}
	if e.agent != nil && e.agent.Model != "" {
		result.Metadata = map[string]string{"model": e.agent.Model}
	}
	return result, e.runtime.Execute(ctx, e.agent, e.task, request.Inputs)
}
