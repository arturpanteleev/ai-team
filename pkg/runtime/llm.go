package runtime

import "context"

type LLMRuntime struct{}

func (r *LLMRuntime) Execute(ctx context.Context, agent *Agent, task *Task) error {
	return ErrNotImplemented
}
