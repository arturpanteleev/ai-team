package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/eval"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
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
		data := []byte(fmt.Sprintf("pipeline: [%s]\ncli: %s\nmodel: %s\n",
			strings.Join(cfg.Pipeline, ", "), cfg.CLI, cfg.Model))
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

func cmdRun() {
	runFlags := flag.NewFlagSet("run", flag.ExitOnError)
	feature := runFlags.String("feature", "", "Имя фичи")
	taskDesc := runFlags.String("task", "", "Описание задачи")
	target := runFlags.String("target", ".", "Путь к целевому проекту")

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

	task := &runtime.Task{
		Feature:      *feature,
		TaskDesc:     *taskDesc,
		TargetDir:    *target,
		ArtifactRoot: filepath.Join(*target, ".ai-team", "artifacts"),
	}

	taskDir := filepath.Join(task.ArtifactRoot, "tasks", *feature)
	os.MkdirAll(taskDir, 0755)
	taskFile := filepath.Join(taskDir, "task.md")
	os.WriteFile(taskFile, []byte(*taskDesc), 0644)

	agentsDir := findAgentsDir()
	reg := agent.NewRegistry(agentsDir)
	rt, _ := runtime.NewRuntime("agentcli")
	p := pipeline.New(cfg.Pipeline, rt, reg)

	ctx := context.Background()
	if err := p.Run(ctx, task); err != nil {
		fmt.Fprintf(os.Stderr, "Пайплайн упал: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✓ Пайплайн выполнен")
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

		task := &runtime.Task{
			Feature:      *feature,
			TaskDesc:     *taskDesc,
			TargetDir:    *target,
			ArtifactRoot: filepath.Join(*target, ".ai-team", "artifacts"),
		}

		agentsDir := findAgentsDir()
		reg := agent.NewRegistry(agentsDir)
		rt, _ := runtime.NewRuntime("agentcli")
		p := pipeline.New(cfg.Pipeline, rt, reg)

		if err := p.Run(ctx, task); err != nil {
			fmt.Fprintf(os.Stderr, "Пайплайн упал: %v\n", err)
			os.Exit(1)
		}

		for _, name := range cfg.Pipeline {
			artifactCandidate := filepath.Join(*target, *feature, name+".md")
			if _, err := os.Stat(artifactCandidate); err == nil {
				fmt.Printf("\n--- Оценка агента: %s ---\n", name)
				if err := eval.RunAndPrint(ctx, name, artifactCandidate, nil); err != nil {
					fmt.Fprintf(os.Stderr, "Ошибка оценки %s: %v\n", name, err)
				}
			}
		}
		return
	}

	fmt.Fprintln(os.Stderr, "Укажите --artifact + --agent или --feature + --task")
	os.Exit(1)
}

func cmdList() {
	agentsDir := findAgentsDir()
	reg := agent.NewRegistry(agentsDir)

	fmt.Printf("%-20s %-15s %-10s %s\n", "Имя", "Runtime", "CLI", "Описание")
	fmt.Println(strings.Repeat("-", 80))
	for _, a := range reg.List() {
		fmt.Printf("%-20s %-15s %-10s %s\n", a.Name, a.RuntimeType, a.CLI, a.Description)
	}
}

func findAgentsDir() string {
	candidates := []string{
		"agents",
		"../../agents",
		filepath.Join(os.Getenv("HOME"), ".ai-team", "agents"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return "agents"
}
