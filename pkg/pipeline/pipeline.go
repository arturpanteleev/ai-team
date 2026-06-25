package pipeline

import (
	"context"
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

type Pipeline struct {
	Agents  []string
	runtime runtime.Runtime
	reg     *agent.Registry
}

func New(agents []string, r runtime.Runtime, reg *agent.Registry) *Pipeline {
	return &Pipeline{
		Agents:  agents,
		runtime: r,
		reg:     reg,
	}
}

func (p *Pipeline) Run(ctx context.Context, task *runtime.Task) error {
	for _, name := range p.Agents {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fmt.Printf("▶ Запуск агента: %s\n", name)

		a, err := p.reg.Load(name)
		if err != nil {
			return fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
		}

		rt, err := runtime.NewRuntime(a.RuntimeType)
		if err != nil {
			return fmt.Errorf("ошибка создания runtime для %s: %w", name, err)
		}

		runtimeAgent := &runtime.Agent{
			Name:        a.Name,
			RuntimeType: a.RuntimeType,
			CLI:         a.CLI,
			PromptFile:  a.PromptFile,
			Prompt:      a.Prompt,
			Inputs:      a.Inputs,
			Outputs:     a.Outputs,
		}

		if err := rt.Execute(ctx, runtimeAgent, task); err != nil {
			return fmt.Errorf("агент %s упал: %w", name, err)
		}

		fmt.Printf("✓ Агент %s завершён\n", name)
	}
	return nil
}
