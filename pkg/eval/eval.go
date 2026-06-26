package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Eval struct {
	AgentName    string
	ArtifactPath string
	Criteria     []string
}

type Result struct {
	Score   int
	Comment string
	Agent   string
}

func New(agentName, artifactPath string, criteria []string) *Eval {
	return &Eval{
		AgentName:    agentName,
		ArtifactPath: artifactPath,
		Criteria:     criteria,
	}
}

func (e *Eval) Run(ctx context.Context) (*Result, error) {
	data, err := os.ReadFile(e.ArtifactPath)
	if err != nil {
		return nil, fmt.Errorf("eval: не удалось прочитать артефакт %s: %w", e.ArtifactPath, err)
	}

	prompt := e.buildJudgePrompt(string(data))
	tmpDir, err := os.MkdirTemp("", "ai-team-eval-*")
	if err != nil {
		return nil, fmt.Errorf("eval: не удалось создать temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	promptFile := filepath.Join(tmpDir, "eval-prompt.md")
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return nil, fmt.Errorf("eval: не удалось записать промпт: %w", err)
	}

	cli := "opencode"
	if _, err := exec.LookPath(cli); err != nil {
		return nil, fmt.Errorf("eval: opencode не найден, установите с https://opencode.ai")
	}

	cmd := exec.CommandContext(ctx, cli, "--resume", "--message-file", promptFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("eval: opencode судья завершился с ошибкой: %w", err)
	}

	return &Result{
		Score:   0,
		Comment: "Оценка выполнена. Проверьте вывод opencode выше.",
		Agent:   e.AgentName,
	}, nil
}

func (e *Eval) buildJudgePrompt(artifactContent string) string {
	criteria := strings.Join(e.Criteria, ", ")
	if criteria == "" {
		criteria = "полнота, тестируемость, отсутствие двусмысленностей"
	}

	return fmt.Sprintf(`# Оценка артефакта

Ты — судья, оценивающий качество артефакта, созданного AI-агентом %s.

## Артефакт

%s

## Критерии оценки

%s

## Формат ответа

Ответь строго в формате:

**Оценка:** <число от 1 до 10>
**Комментарий:** <твой комментарий>
`, e.AgentName, artifactContent, criteria)
}

func RunAndPrint(ctx context.Context, agentName, artifactPath string, criteria []string) error {
	e := New(agentName, artifactPath, criteria)
	result, err := e.Run(ctx)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("=== Результат оценки ===")
	fmt.Printf("Агент:     %s\n", result.Agent)
	if result.Score > 0 {
		fmt.Printf("Оценка:    %d/10\n", result.Score)
	}
	if result.Comment != "" {
		fmt.Printf("Комментарий: %s\n", result.Comment)
	}

	return nil
}

func extractScore(text string) int {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "**Оценка:**") || strings.HasPrefix(line, "Оценка:") {
			parts := strings.Fields(line)
			for _, p := range parts {
				if score, err := strconv.Atoi(p); err == nil && score >= 1 && score <= 10 {
					return score
				}
			}
		}
	}
	return 0
}
