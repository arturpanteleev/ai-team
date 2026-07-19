package runtime

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("LLM runtime: not implemented yet")

type Runtime interface {
	Execute(ctx context.Context, agent *Agent, task *Task, inputs []Artifact) error
}

type Agent struct {
	Name        string
	RuntimeType string
	CLI         string
	PromptFile  string
	Prompt      string
	Inputs      map[string]string
	Outputs     map[string]string
	GateAfter   bool
	GateBefore  bool
}

type Task struct {
	Feature    string
	TaskDesc   string
	TargetDir  string
	ArtifactRoot string
}
