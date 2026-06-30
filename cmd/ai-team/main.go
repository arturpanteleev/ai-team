package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team"
	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/eval"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"io/fs"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		cmdInit()
	case "run":
		cmdRun()
	case "list":
		cmdList()
	case "eval":
		cmdEval()
	case "version":
		fmt.Println(version)
	default:
		fmt.Printf("Неизвестная команда: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`ai-team — AI-команда для spec-driven разработки

Использование:
  ai-team init              Инициализировать .ai-team/ в текущем проекте
  ai-team run               Запустить пайплайн агентов
  ai-team list              Список доступных агентов
  ai-team eval              Оценить качество артефакта или агента
  ai-team version           Версия

Флаги run:
  --feature <name>          Имя фичи
  --task <description>      Описание задачи
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)

Флаги eval:
  --agent <name>            Имя агента для оценки
  --artifact <path>         Путь к артефакту для оценки
  --feature <name>          Запустить пайплайн и оценить артефакты
  --task <description>      Описание задачи для пайплайна
  --target <path>           Путь к проекту (по умолчанию текущая директория)`)
}

func cmdInit() {
	target := "."
	if len(os.Args) > 2 && os.Args[2] == "--target" && len(os.Args) > 3 {
		target = os.Args[3]
	}

	artifactsDirs := []string{
		filepath.Join(target, ".ai-team"),
		filepath.Join(target, ".ai-team", "artifacts", "product"),
		filepath.Join(target, ".ai-team", "artifacts", "tech"),
		filepath.Join(target, ".ai-team", "artifacts", "reviews"),
		filepath.Join(target, ".ai-team", "artifacts", "tasks"),
		filepath.Join(target, ".ai-team", "reports"),
	}
	for _, d := range artifactsDirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка создания %s: %v\n", d, err)
			os.Exit(1)
		}
	}

	cfg := config.Default()
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		data := []byte(fmt.Sprintf("pipeline: [%s]\ncli: %s\nmodel: %s\neffort: %s\n",
			strings.Join(cfg.AgentNames(), ", "), cfg.CLI, cfg.Model, cfg.Effort))
		if err := os.WriteFile(cfgPath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка создания конфига: %v\n", err)
			os.Exit(1)
		}
	}

	if err := runtime.CheckCLI(cfg.CLI); err != nil {
		fmt.Fprintf(os.Stderr, "Предупреждение: %v\n", err)
	}

	giPath := filepath.Join(target, ".gitignore")
	if _, err := os.Stat(giPath); err == nil {
		data, _ := os.ReadFile(giPath)
		if !strings.Contains(string(data), ".ai-team") {
			f, _ := os.OpenFile(giPath, os.O_APPEND|os.O_WRONLY, 0644)
			if f != nil {
				f.WriteString("\n# ai-team\n.ai-team/\n")
				f.Close()
			}
		}
	}

	fmt.Printf("✓ .ai-team/ инициализирован в %s\n", target)
}

func agentsFS() fs.FS {
	s, err := fs.Sub(agentdata.Agents, "agents")
	if err != nil {
		return agentdata.Agents
	}
	return s
}

func cmdRun() {
	runFlags := flag.NewFlagSet("run", flag.ExitOnError)
	feature := runFlags.String("feature", "", "Имя фичи")
	taskDesc := runFlags.String("task", "", "Описание задачи")
	target := runFlags.String("target", ".", "Путь к целевому проекту")
	retryFrom := runFlags.String("retry-from", "", "Перезапустить с указанного агента")

	runFlags.Parse(os.Args[2:])

	if *feature == "" {
		fmt.Fprintln(os.Stderr, "Укажите --feature")
		os.Exit(1)
	}
	if *taskDesc == "" {
		fmt.Fprintln(os.Stderr, "Укажите --task")
		os.Exit(1)
	}

	cfgPath := filepath.Join(*target, ".ai-team", "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка загрузки конфига: %v\n", err)
		os.Exit(1)
	}

	os.MkdirAll(filepath.Join(*target, ".ai-team", "artifacts", "tasks", *feature), 0755)
	os.WriteFile(filepath.Join(*target, ".ai-team", "artifacts", "tasks", *feature, "task.md"), []byte(*taskDesc), 0644)

	reg := agent.NewFS(agentsFS())
	p := pipeline.New(cfg, reg)

	ctx := context.Background()
	if err := p.Run(ctx, pipeline.RunConfig{
		Feature:   *feature,
		TaskDesc:  *taskDesc,
		TargetDir: *target,
		RetryFrom: *retryFrom,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "%s Пайплайн упал: %v\n", ui.Colorize("✗", ui.ColorRed), err)
		os.Exit(1)
	}

	fmt.Printf("\n%s Пайплайн выполнен\n", ui.Colorize("✓", ui.ColorGreen))
}

func cmdEval() {
	evalFlags := flag.NewFlagSet("eval", flag.ExitOnError)
	agentName := evalFlags.String("agent", "", "Имя агента для оценки")
	artifactPath := evalFlags.String("artifact", "", "Путь к артефакту для оценки")
	feature := evalFlags.String("feature", "", "Запустить пайплайн и оценить")
	taskDesc := evalFlags.String("task", "", "Описание задачи")
	target := evalFlags.String("target", ".", "Путь к проекту")

	evalFlags.Parse(os.Args[2:])

	ctx := context.Background()

	if *artifactPath != "" && *agentName != "" {
		if err := eval.RunAndPrint(ctx, *agentName, *artifactPath, nil); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка оценки: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *feature != "" && *taskDesc != "" {
		cfgPath := filepath.Join(*target, ".ai-team", "config.yaml")
		cfg, err := config.Load(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка загрузки конфига: %v\n", err)
			os.Exit(1)
		}

		reg := agent.NewFS(agentsFS())
		p := pipeline.New(cfg, reg)

		if err := p.Run(ctx, pipeline.RunConfig{
			Feature:   *feature,
			TaskDesc:  *taskDesc,
			TargetDir: *target,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Пайплайн упал: %v\n", err)
			os.Exit(1)
		}

		for _, name := range cfg.AgentNames() {
			artifactCandidate := filepath.Join(*target, *feature, name+".md")
			if _, err := os.Stat(artifactCandidate); err == nil {
				fmt.Printf("\n--- Оценка агента: %s ---\n", name)
				if err := eval.RunAndPrint(ctx, name, artifactCandidate, nil); err != nil {
					fmt.Errorf("Ошибка оценки %s: %v\n", name, err)
				}
			}
		}
		return
	}

	fmt.Fprintln(os.Stderr, "Укажите --artifact + --agent или --feature + --task")
	os.Exit(1)
}

func cmdList() {
	reg := agent.NewFS(agentsFS())

	fmt.Printf("%-20s %-15s %-10s %s\n", "Имя", "Runtime", "CLI", "Описание")
	fmt.Println(strings.Repeat("-", 80))
	for _, a := range reg.List() {
		fmt.Printf("%-20s %-15s %-10s %s\n", a.Name, a.RuntimeType, a.CLI, a.Description)
	}
}
