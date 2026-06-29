package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type AgentCLIRuntime struct{}

func (r *AgentCLIRuntime) Execute(ctx context.Context, agent *Agent, task *Task, inputs []Artifact) error {
	cli := agent.CLI
	if cli == "" {
		cli = "opencode"
	}

	if _, err := exec.LookPath(cli); err != nil {
		return fmt.Errorf("%s: команда не найдена. Установите с https://opencode.ai", cli)
	}

	prompt, err := r.buildPrompt(agent, task, inputs)
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

func (r *AgentCLIRuntime) buildPrompt(agent *Agent, task *Task, inputs []Artifact) (string, error) {
	prompt := fmt.Sprintf("# %s\n\n%s\n\n", agent.Name, agent.Prompt)
	prompt += fmt.Sprintf("## Фича\n%s\n\n", task.Feature)
	prompt += fmt.Sprintf("## Описание задачи\n%s\n\n", task.TaskDesc)

	for _, input := range inputs {
		data, err := os.ReadFile(input.Path)
		if err != nil {
			prompt += fmt.Sprintf("### %s\nНе удалось прочитать %s: %v\n\n", input.Name, input.Path, err)
			continue
		}
		prompt += fmt.Sprintf("### %s\n```\n%s\n```\n\n", input.Name, string(data))
	}

	for name, path := range agent.Inputs {
		replaced := ReplaceVars(path, task.Feature)
		fullPath := filepath.Join(task.ArtifactRoot, replaced)
		info, err := os.Stat(fullPath)
		if err == nil && info.IsDir() {
			prompt += fmt.Sprintf("### %s\nФайлы находятся в: `%s`\n\n", name, fullPath)
		}
	}

	if len(agent.Outputs) > 0 {
		prompt += "## Ожидаемые результаты\n"
		for name, path := range agent.Outputs {
			replaced := ReplaceVars(path, task.Feature)
			fullPath := filepath.Join(task.ArtifactRoot, replaced)
			prompt += fmt.Sprintf("- `%s` → %s\n", name, fullPath)
		}
	}

	return prompt, nil
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
