package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type AgentCLIRuntime struct{}

func (r *AgentCLIRuntime) Execute(ctx context.Context, agent *Agent, task *Task) error {
	cli := agent.CLI
	if cli == "" {
		cli = "opencode"
	}

	if _, err := exec.LookPath(cli); err != nil {
		return fmt.Errorf("%s: команда не найдена. Установите с https://opencode.ai", cli)
	}

	prompt, err := r.buildPrompt(agent, task)
	if err != nil {
		return fmt.Errorf("ошибка сборки промпта: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "ai-team-*")
	if err != nil {
		return fmt.Errorf("ошибка создания temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	promptFile := filepath.Join(tmpDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return fmt.Errorf("ошибка записи промпта: %w", err)
	}

	targetDir := task.TargetDir
	if targetDir == "" {
		targetDir = "."
	}

	cmd := exec.CommandContext(ctx, cli, "--resume", "--message-file", promptFile)
	cmd.Dir = targetDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("агент %s завершился с ошибкой: %w", agent.Name, err)
	}

	return nil
}

func (r *AgentCLIRuntime) buildPrompt(agent *Agent, task *Task) (string, error) {
	prompt := fmt.Sprintf("# %s\n\n%s\n\n", agent.Name, agent.Prompt)
	prompt += fmt.Sprintf("## Фича\n%s\n\n", task.Feature)
	prompt += fmt.Sprintf("## Описание задачи\n%s\n\n", task.TaskDesc)

	for name, path := range agent.Inputs {
		replaced := replaceVars(path, task.Feature)
		fullPath := filepath.Join(task.TargetDir, replaced)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			prompt += fmt.Sprintf("### %s\nНе удалось прочитать %s: %v\n\n", name, replaced, err)
			continue
		}
		prompt += fmt.Sprintf("### %s\n```\n%s\n```\n\n", name, string(data))
	}

	if len(agent.Outputs) > 0 {
		prompt += "## Ожидаемые результаты\n"
		for name, path := range agent.Outputs {
			replaced := replaceVars(path, task.Feature)
			prompt += fmt.Sprintf("- `%s` → %s\n", name, replaced)
		}
	}

	return prompt, nil
}

func replaceVars(s, feature string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		if s[i] == '{' && i+8 < len(s) && s[i:i+9] == "{feature}" {
			result += feature
			i += 8
		} else {
			result += string(s[i])
		}
	}
	return result
}

func CheckCLI(cli string) error {
	if _, err := exec.LookPath(cli); err != nil {
		return fmt.Errorf("%s: команда не найдена. Установите с https://opencode.ai", cli)
	}
	return nil
}

func NewRuntime(runtimeType string) (Runtime, error) {
	switch runtimeType {
	case "agentcli":
		return &AgentCLIRuntime{}, nil
	case "llm":
		return &LLMRuntime{}, nil
	default:
		return nil, fmt.Errorf("неизвестный тип runtime: %s", runtimeType)
	}
}
