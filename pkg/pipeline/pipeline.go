package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

type Pipeline struct {
	Agents []string
	reg    *agent.Registry
}

func New(agents []string, reg *agent.Registry) *Pipeline {
	return &Pipeline{
		Agents: agents,
		reg:    reg,
	}
}

func (p *Pipeline) Run(ctx context.Context, task *runtime.Task) error {
	var artifacts []runtime.Artifact

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

		var inputs []runtime.Artifact
		for inName, inPath := range a.Inputs {
			replaced := runtime.ReplaceVars(inPath, task.Feature)
			fullPath := filepath.Join(task.ArtifactRoot, replaced)

			info, err := os.Stat(fullPath)
			if err != nil {
				return fmt.Errorf("агент %s: вход %s (%s) не найден: %w", name, inName, fullPath, err)
			}

			fmt.Printf("  → %s (%s, %s, %d байт)\n", inName, fullPath, info.ModTime().Format(time.RFC3339), info.Size())

			if !info.IsDir() {
				inputs = append(inputs, runtime.Artifact{
					Name:    inName,
					Path:    fullPath,
					Size:    info.Size(),
					ModTime: info.ModTime(),
				})
			}
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

		if err := rt.Execute(ctx, runtimeAgent, task, inputs); err != nil {
			return fmt.Errorf("агент %s упал: %w", name, err)
		}

		for outName, outPath := range a.Outputs {
			replaced := runtime.ReplaceVars(outPath, task.Feature)
			fullPath := filepath.Join(task.ArtifactRoot, replaced)

			info, err := os.Stat(fullPath)
			if err != nil {
				return fmt.Errorf("агент %s: выход %s (%s) не создан: %w", name, outName, fullPath, err)
			}

			artifacts = append(artifacts, runtime.Artifact{
				Name:    outName,
				Path:    fullPath,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
			fmt.Printf("  ✓ %s создан (%s, %s, %d байт)\n", outName, fullPath, info.ModTime().Format(time.RFC3339), info.Size())
		}

		fmt.Printf("✓ Агент %s завершён\n", name)
	}

	return nil
}
