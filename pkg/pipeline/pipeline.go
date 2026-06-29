package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

type StageResult struct {
	Name    string
	Err     error
	Inputs  []runtime.Artifact
	Outputs []runtime.Artifact
}

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
	var results []StageResult
	var artifacts []runtime.Artifact
	var lastErr error

	for _, name := range p.Agents {
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			p.printSummary(task.Feature, results)
			return lastErr
		default:
		}

		r := StageResult{Name: name}

		fmt.Printf("▶ Запуск агента: %s\n", name)

		a, err := p.reg.Load(name)
		if err != nil {
			r.Err = fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
			results = append(results, r)
			lastErr = r.Err
			break
		}

		var inputs []runtime.Artifact
		for inName, inPath := range a.Inputs {
			replaced := runtime.ReplaceVars(inPath, task.Feature)
			fullPath := filepath.Join(task.ArtifactRoot, replaced)

			info, err := os.Stat(fullPath)
			if err != nil {
				r.Err = fmt.Errorf("агент %s: вход %s (%s) не найден: %w", name, inName, fullPath, err)
				results = append(results, r)
				lastErr = r.Err
				break
			}

			fmt.Printf("  → %s (%s, %s, %d байт)\n", inName, fullPath, info.ModTime().Format(time.RFC3339), info.Size())

			if !info.IsDir() {
				art := runtime.Artifact{
					Name:    inName,
					Path:    fullPath,
					Size:    info.Size(),
					ModTime: info.ModTime(),
				}
				inputs = append(inputs, art)
				r.Inputs = append(r.Inputs, art)
			} else {
				r.Inputs = append(r.Inputs, runtime.Artifact{
					Name: inName,
					Path: fullPath,
				})
			}
		}

		if r.Err != nil {
			results = append(results, r)
			break
		}

		rt, err := runtime.NewRuntime(a.RuntimeType)
		if err != nil {
			r.Err = fmt.Errorf("ошибка создания runtime для %s: %w", name, err)
			results = append(results, r)
			lastErr = r.Err
			break
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
			r.Err = fmt.Errorf("агент %s упал: %w", name, err)
			results = append(results, r)
			lastErr = r.Err
			break
		}

		for outName, outPath := range a.Outputs {
			replaced := runtime.ReplaceVars(outPath, task.Feature)
			fullPath := filepath.Join(task.ArtifactRoot, replaced)

			info, err := os.Stat(fullPath)
			if err != nil {
				r.Err = fmt.Errorf("агент %s: выход %s (%s) не создан: %w", name, outName, fullPath, err)
				break
			}

			art := runtime.Artifact{
				Name:    outName,
				Path:    fullPath,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			}
			artifacts = append(artifacts, art)
			r.Outputs = append(r.Outputs, art)
			fmt.Printf("  ✓ %s создан (%s, %s, %d байт)\n", outName, fullPath, info.ModTime().Format(time.RFC3339), info.Size())
		}

		if r.Err != nil {
			results = append(results, r)
			lastErr = r.Err
			break
		}

		fmt.Printf("✓ Агент %s завершён\n", name)
		results = append(results, r)
	}

	p.printSummary(task.Feature, results)
	return lastErr
}

func (p *Pipeline) printSummary(feature string, results []StageResult) {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "=== ИТОГ ПАЙПЛАЙНА: %s ===\n", feature)
	fmt.Fprintf(w, "Этап\tСтатус\tВходы\tВыходы\n")
	fmt.Fprintf(w, "───\t───\t───\t───\n")

	for _, r := range results {
		status := "✓"
		if r.Err != nil {
			status = "✗"
		}

		var inNames, outNames []string
		for _, in := range r.Inputs {
			inNames = append(inNames, in.Name)
		}
		inputsStr := strings.Join(inNames, ", ")
		if inputsStr == "" {
			inputsStr = "—"
		}

		if r.Err != nil {
			outNames = append(outNames, shortenError(r.Err))
		} else {
			for _, out := range r.Outputs {
				label := out.Name
				if verdict := readVerdict(out.Path); verdict != "" {
					label += " (" + verdict + ")"
				}
				outNames = append(outNames, label)
			}
		}
		outputsStr := strings.Join(outNames, ", ")
		if outputsStr == "" && r.Err == nil {
			outputsStr = "—"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, status, inputsStr, outputsStr)
	}
	w.Flush()
	fmt.Println()
}

func readVerdict(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)
	if strings.Contains(content, "**Verdict:** APPROVED") {
		return "APPROVED"
	}
	if strings.Contains(content, "**Verdict:** CHANGES_REQUESTED") {
		return "CHANGES_REQ"
	}
	if strings.Contains(content, "**Verdict:** REJECTED") {
		return "REJECTED"
	}
	if strings.Contains(content, "**Result:** PASS") {
		return "PASS"
	}
	if strings.Contains(content, "**Result:** FAIL") {
		return "FAIL"
	}
	return ""
}

func shortenError(err error) string {
	msg := err.Error()
	// кратко: что произошло, без лишних деталей
	if strings.Contains(msg, "не создан") {
		// "агент X: выход proposal (...) не создан: ..." → "выход proposal не создан"
		parts := strings.SplitN(msg, "выход ", 2)
		if len(parts) == 2 {
			parts2 := strings.SplitN(parts[1], " (", 2)
			return "выход " + parts2[0] + " не создан"
		}
	}
	if strings.Contains(msg, "не найден") {
		parts := strings.SplitN(msg, "вход ", 2)
		if len(parts) == 2 {
			parts2 := strings.SplitN(parts[1], " (", 2)
			return "вход " + parts2[0] + " не найден"
		}
	}
	if strings.Contains(msg, "завершился с ошибкой") {
		return "ошибка выполнения"
	}
	if strings.Contains(msg, "загрузки") {
		return "агент не загружен"
	}
	// fallback: первые N символов
	if len(msg) > 50 {
		return msg[:50] + "..."
	}
	return msg
}
