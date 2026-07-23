package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	agentdata "github.com/arturpanteleev/ai-team"
	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/eval"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/web"
	webstore "github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const version = "0.2.0"

// Exit-коды run (см. спеку cli-interface).
const (
	exitOK          = 0
	exitFailed      = 1
	exitBlocked     = 2
	exitUserStopped = 3
)

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
	case "web":
		cmdWeb()
	case "version":
		fmt.Println(version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`ai-team — AI-команда для spec-driven разработки

Использование:
  ai-team init [--target <path>]   Инициализировать .ai-team/ в проекте
  ai-team run                      Запустить пайплайн агентов
  ai-team list                     Список доступных агентов
  ai-team eval                     Оценить качество артефакта или агента
  ai-team web                      Запустить web-дашборд
  ai-team version                  Версия
  ai-team help                     Эта справка

Флаги run:
  --feature <name>          Имя фичи (буквы, цифры, "-", "_", ".")
  --task <description>      Описание задачи
  --target <path>           Путь к целевому проекту (по умолчанию текущая директория)
	  --retry-from <agent>      Перезапустить с указанного агента (--task не обязателен)
	  --approve-gates           Явно подтвердить обычные gate-точки в non-interactive режиме
	  --approve-plan <sha256>    Разрешить только показанный canonical delivery plan

Exit-коды run: 0 — успех, 1 — ошибка или негативный вердикт,
               2 — BLOCKED (нужно вмешательство), 3 — остановлен пользователем

Флаги eval:
  --agent <name>            Имя агента
  --artifact <path>         Путь к артефакту для оценки (без запуска пайплайна)
  --feature <name>          Запустить одного агента и оценить его артефакты
  --task <description>      Описание задачи для запуска
  --target <path>           Путь к проекту (по умолчанию текущая директория)
	  --samples <1-20>          Число независимых LLM-оценок (advisory)
	  --json-out <path>         Путь JSON evidence (по умолчанию .ai-team/evals/...)

Флаги web:
	  --port <port>             Порт (по умолчанию 8080)
	  --host <host>             Адрес bind (по умолчанию 127.0.0.1)
  --db <path>               Путь к SQLite (по умолчанию .ai-team/web.db)
  --dist <path>             Каталог собранного frontend (по умолчанию web/dist)
  --artifacts <path>        Корень артефактов (по умолчанию .ai-team/artifacts)`)
}

func validFeature(name string) bool {
	return workflow.ValidFeature(name)
}

func absoluteTarget(target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("не удалось определить абсолютный target path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func agentsFS() fs.FS {
	s, err := fs.Sub(agentdata.Agents, "agents")
	if err != nil {
		return agentdata.Agents
	}
	return s
}

func newAgentRegistry(target string) (*agent.Registry, error) {
	projectAgents := filepath.Join(target, ".ai-team", "agents")
	if err := safeio.ValidateTree(projectAgents); err != nil {
		return nil, err
	}
	layers := []agent.Layer{{Name: "project", FS: os.DirFS(filepath.Join(target, ".ai-team", "agents"))}}
	for index, pluginDir := range filepath.SplitList(os.Getenv("AI_TEAM_AGENT_PATH")) {
		if pluginDir == "" {
			continue
		}
		if absolute, err := filepath.Abs(pluginDir); err == nil {
			pluginDir = absolute
		}
		layers = append(layers, agent.Layer{Name: fmt.Sprintf("plugin-%d:%s", index, pluginDir), FS: os.DirFS(pluginDir)})
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		userDir := filepath.Join(configDir, "ai-team", "agents")
		layers = append(layers, agent.Layer{Name: "user:" + userDir, FS: os.DirFS(userDir)})
	}
	layers = append(layers, agent.Layer{Name: "builtin", FS: agentsFS()})
	return agent.NewLayered(layers...), nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func cmdInit() {
	target := "."
	if len(os.Args) > 2 && os.Args[2] == "--target" && len(os.Args) > 3 {
		target = os.Args[3]
	}
	var err error
	target, err = absoluteTarget(target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	if _, err := safeio.EnsureDir(target, ".ai-team"); err != nil {
		fatal("Небезопасный control root: %v", err)
	}

	dirs := [][]string{
		{".ai-team", "artifacts", "tasks"},
		{".ai-team", "reports"},
		{".ai-team", "logs"},
	}
	for _, components := range dirs {
		if _, err := safeio.EnsureDir(target, components...); err != nil {
			fatal("Ошибка создания %s: %v", filepath.Join(components...), err)
		}
	}

	cfg := config.Default()
	switch profile, warning := cfg.ApplyDetectedChecks(target); {
	case warning != "":
		fmt.Fprintf(os.Stderr, "Предупреждение: %s\n", warning)
	case profile != "":
		fmt.Printf("✓ Обнаружен verification profile: %s\n", profile)
	default:
		fmt.Fprintln(os.Stderr, "Предупреждение: тестовый профиль не обнаружен; delivery будет запрещён до настройки required unit/integration/e2e check")
	}
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		data, err := cfg.Marshal()
		if err != nil {
			fatal("Ошибка сериализации конфига: %v", err)
		}
		if err := os.WriteFile(cfgPath, data, 0644); err != nil {
			fatal("Ошибка создания конфига: %v", err)
		}
	}

	if err := runtime.CheckCLI(cfg.CLI); err != nil {
		fmt.Fprintf(os.Stderr, "Предупреждение: %v\n", err)
	}

	ensureGitignore(target)

	fmt.Printf("✓ .ai-team/ инициализирован в %s\n", target)
}

// ensureGitignore гарантирует исключение .ai-team/ из git: дописывает в
// существующий .gitignore или создаёт его (только внутри git-репозитория).
func ensureGitignore(target string) {
	giPath := filepath.Join(target, ".gitignore")
	data, err := os.ReadFile(giPath)
	switch {
	case err == nil:
		if strings.Contains(string(data), ".ai-team") {
			return
		}
		f, err := os.OpenFile(giPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Предупреждение: не удалось обновить .gitignore: %v\n", err)
			return
		}
		defer f.Close()
		if _, err := f.WriteString("\n# ai-team\n.ai-team/\n"); err != nil {
			fmt.Fprintf(os.Stderr, "Предупреждение: не удалось обновить .gitignore: %v\n", err)
		}
	case os.IsNotExist(err):
		if _, gErr := os.Stat(filepath.Join(target, ".git")); gErr != nil {
			return // не git-репозиторий — .gitignore не нужен
		}
		if wErr := os.WriteFile(giPath, []byte("# ai-team\n.ai-team/\n"), 0644); wErr != nil {
			fmt.Fprintf(os.Stderr, "Предупреждение: не удалось создать .gitignore: %v\n", wErr)
		}
	default:
		fmt.Fprintf(os.Stderr, "Предупреждение: не удалось прочитать .gitignore: %v\n", err)
	}
}

func loadValidatedConfig(target string, reg *agent.Registry) *config.Config {
	cfgPath := filepath.Join(target, ".ai-team", "config.yaml")
	if err := safeio.RejectSymlink(cfgPath); err != nil {
		fatal("Небезопасный config path: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fatal("Ошибка загрузки конфига: %v", err)
	}
	if err := cfg.Validate(reg); err != nil {
		fatal("%v", err)
	}
	return cfg
}

func cmdRun() {
	runFlags := flag.NewFlagSet("run", flag.ExitOnError)
	feature := runFlags.String("feature", "", "Имя фичи")
	taskDesc := runFlags.String("task", "", "Описание задачи")
	target := runFlags.String("target", ".", "Путь к целевому проекту")
	retryFrom := runFlags.String("retry-from", "", "Перезапустить с указанного агента")
	approveGates := runFlags.Bool("approve-gates", false, "Подтвердить gate-точки в non-interactive режиме")
	approvePlan := runFlags.String("approve-plan", "", "SHA-256 ранее показанного delivery plan")

	runFlags.Parse(os.Args[2:])
	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absTarget
	if _, err := safeio.ExistingDir(*target, ".ai-team"); err != nil {
		fatal("Небезопасный или отсутствующий control root: %v", err)
	}

	if *feature == "" {
		fatal("Укажите --feature")
	}
	if !validFeature(*feature) {
		fatal("недопустимое имя фичи: %q (допустимы буквы, цифры, \"-\", \"_\", \".\")", *feature)
	}

	// Config/registry validation is intentionally before task.md writes: a bad
	// control-plane definition must fail without mutating the target workspace.
	reg, err := newAgentRegistry(*target)
	if err != nil {
		fatal("Небезопасный project agent registry: %v", err)
	}
	cfg := loadValidatedConfig(*target, reg)

	if *retryFrom == "" {
		if *taskDesc == "" {
			fatal("Укажите --task")
		}
	} else if *taskDesc != "" {
		fatal("--task нельзя менять вместе с --retry-from; используется сохранённый task.md")
	}

	opts := []pipeline.Option{}
	if recorder, closeStore := openRecorder(*target); recorder != nil {
		opts = append(opts, pipeline.WithRecorder(recorder))
		defer closeStore()
	}

	p := pipeline.New(cfg, reg, opts...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runResult, err := p.RunWithResult(ctx, pipeline.RunConfig{
		Feature:         *feature,
		TaskDesc:        *taskDesc,
		TargetDir:       *target,
		RetryFrom:       *retryFrom,
		ApproveGates:    *approveGates,
		ApprovePlanHash: *approvePlan,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s Пайплайн остановлен: %v\n", ui.Colorize("✗", ui.ColorRed), err)
		os.Exit(exitCodeFor(err))
	}

	if string(runResult.Outcome) == "completed_with_warnings" {
		fmt.Printf("\n%s Пайплайн выполнен с предупреждениями\n", ui.Colorize("!", ui.ColorYellow))
	} else {
		fmt.Printf("\n%s Пайплайн выполнен\n", ui.Colorize("✓", ui.ColorGreen))
	}
}

func exitCodeFor(err error) int {
	var runErr *pipeline.RunError
	var blocked *pipeline.BlockedError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &runErr):
		switch string(runErr.Outcome) {
		case "blocked":
			return exitBlocked
		case "stopped":
			return exitUserStopped
		default:
			return exitFailed
		}
	case errors.As(err, &blocked):
		return exitBlocked
	case errors.Is(err, pipeline.ErrUserStopped):
		return exitUserStopped
	default:
		return exitFailed
	}
}

// openRecorder открывает SQLite-store для записи запусков (web-дашборд).
// Недоступность БД не мешает запуску.
func openRecorder(target string) (pipeline.Recorder, func()) {
	dbPath := filepath.Join(target, ".ai-team", "web.db")
	if _, err := safeio.ExistingDir(target, ".ai-team"); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store: %v — запись запусков отключена\n", err)
		return nil, nil
	}
	if err := safeio.RejectSymlink(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store: %v — запись запусков отключена\n", err)
		return nil, nil
	}
	s, err := webstore.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store: %v — запись запусков отключена\n", err)
		return nil, nil
	}
	return web.NewStoreRecorder(s), func() { s.Close() }
}

func cmdEval() {
	evalFlags := flag.NewFlagSet("eval", flag.ExitOnError)
	agentName := evalFlags.String("agent", "", "Имя агента для оценки")
	artifactPath := evalFlags.String("artifact", "", "Путь к артефакту для оценки")
	feature := evalFlags.String("feature", "", "Запустить одного агента и оценить")
	taskDesc := evalFlags.String("task", "", "Описание задачи")
	target := evalFlags.String("target", ".", "Путь к проекту")
	samples := evalFlags.Int("samples", 1, "Число независимых LLM-оценок (1-20)")
	jsonOut := evalFlags.String("json-out", "", "Путь JSON evidence")

	evalFlags.Parse(os.Args[2:])
	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	*target = absTarget
	if _, err := safeio.ExistingDir(*target, ".ai-team"); err != nil {
		fatal("Небезопасный или отсутствующий control root: %v", err)
	}
	if *samples < 1 || *samples > 20 {
		fatal("--samples должен быть от 1 до 20")
	}
	if *jsonOut == "" && *agentName != "" {
		*jsonOut = defaultEvalOutput(*target, *agentName)
	} else if *jsonOut != "" && !filepath.IsAbs(*jsonOut) {
		*jsonOut = filepath.Join(*target, *jsonOut)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *artifactPath != "" && *agentName != "" {
		if err := eval.RunAndPrintQuality(ctx, *agentName, *artifactPath, nil, *target, *samples, *jsonOut); err != nil {
			fatal("Ошибка оценки: %v", err)
		}
		return
	}

	if *feature != "" && *taskDesc != "" && *agentName != "" {
		if !validFeature(*feature) {
			fatal("недопустимое имя фичи: %q", *feature)
		}
		if err := evalSingleAgent(ctx, *target, *feature, *taskDesc, *agentName, *samples, *jsonOut); err != nil {
			fatal("Ошибка оценки: %v", err)
		}
		return
	}

	fatal("Укажите --artifact + --agent, либо --feature + --task + --agent")
}

// evalSingleAgent запускает пайплайн из одного агента и оценивает его
// фактические выходные артефакты (пути из def.yaml).
func evalSingleAgent(ctx context.Context, target, feature, taskDesc, agentName string, samples int, outputPath string) error {
	reg, err := newAgentRegistry(target)
	if err != nil {
		return err
	}
	a, err := reg.Load(agentName)
	if err != nil {
		return err
	}

	base := loadValidatedConfig(target, reg)
	cfg := &config.Config{
		PipelineAgents: []config.AgentConfig{{Name: agentName}},
		CLI:            base.CLI,
		Model:          base.Model,
		Effort:         base.Effort,
		StageTimeout:   base.StageTimeout,
	}

	p := pipeline.New(cfg, reg)
	if err := p.Run(ctx, pipeline.RunConfig{Feature: feature, TaskDesc: taskDesc, TargetDir: target}); err != nil {
		return fmt.Errorf("пайплайн упал: %w", err)
	}

	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	evaluated := 0
	for outputName, outPath := range a.Outputs {
		fullPath := filepath.Join(artifactRoot, runtime.ReplaceVars(outPath, feature))
		if info, err := os.Stat(fullPath); err != nil || info.IsDir() {
			continue
		}
		fmt.Printf("\n--- Оценка артефакта: %s ---\n", fullPath)
		artifactOutput := outputPath
		if len(a.Outputs) > 1 {
			extension := filepath.Ext(outputPath)
			safeOutputName := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(outputName, "-")
			artifactOutput = strings.TrimSuffix(outputPath, extension) + "-" + safeOutputName + extension
		}
		if err := eval.RunAndPrintQuality(ctx, agentName, fullPath, nil, target, samples, artifactOutput); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка оценки %s: %v\n", fullPath, err)
		} else {
			evaluated++
		}
	}
	if evaluated == 0 {
		return fmt.Errorf("не найдено артефактов для оценки у агента %s", agentName)
	}
	return nil
}

func defaultEvalOutput(target, agentName string) string {
	safeAgent := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(agentName, "-")
	return filepath.Join(target, ".ai-team", "evals", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+safeAgent+".json")
}

func cmdList() {
	target, err := absoluteTarget(".")
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(target, ".ai-team")); statErr == nil {
		if _, err := safeio.ExistingDir(target, ".ai-team"); err != nil {
			fatal("Небезопасный control root: %v", err)
		}
	} else if !os.IsNotExist(statErr) {
		fatal("Ошибка control root: %v", statErr)
	}
	reg, err := newAgentRegistry(target)
	if err != nil {
		fatal("Небезопасный project agent registry: %v", err)
	}

	fmt.Printf("%-20s %-15s %-10s %-20s %s\n", "Имя", "Runtime", "CLI", "Источник", "Описание")
	fmt.Println(strings.Repeat("-", 80))
	for _, a := range reg.List() {
		fmt.Printf("%-20s %-15s %-10s %-20s %s\n", a.Name, a.RuntimeType, a.CLI, a.Source, a.Description)
	}
}

func cmdWeb() {
	webFlags := flag.NewFlagSet("web", flag.ExitOnError)
	port := webFlags.String("port", "8080", "Port for web server")
	host := webFlags.String("host", "127.0.0.1", "Bind host")
	dbPath := webFlags.String("db", ".ai-team/web.db", "Path to SQLite database")
	distDir := webFlags.String("dist", "web/dist", "Path to frontend dist directory")
	artifacts := webFlags.String("artifacts", ".ai-team/artifacts", "Artifact root directory")
	webFlags.Parse(os.Args[2:])
	target, err := absoluteTarget(".")
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	if _, err := safeio.EnsureDir(target, ".ai-team"); err != nil {
		fatal("Небезопасный control root: %v", err)
	}
	if filepath.Clean(*dbPath) == filepath.Join(".ai-team", "web.db") {
		if err := safeio.RejectSymlink(filepath.Join(target, *dbPath)); err != nil {
			fatal("Небезопасный путь БД: %v", err)
		}
	}

	if dir := filepath.Dir(*dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fatal("Ошибка создания каталога БД: %v", err)
		}
	}

	srv, err := web.NewServer(*dbPath, *distDir, *artifacts)
	if err != nil {
		fatal("Ошибка запуска web сервера: %v", err)
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	addr := net.JoinHostPort(*host, *port)
	if *host != "127.0.0.1" && *host != "localhost" && *host != "::1" {
		fatal("web UI не имеет authentication и может bind только loopback host")
	}
	fmt.Printf("Web UI available at http://%s\n", addr)
	if err := srv.ListenAndServe(addr); err != nil {
		fatal("Ошибка сервера: %v", err)
	}
}
