package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

type Pipeline struct {
	cfg          *config.Config
	reg          *agent.Registry
	notifier     notifier.Notifier
	reportsDir   string
	targetDir   string
}

type Option func(*Pipeline)

func WithNotifier(n notifier.Notifier) Option {
	return func(p *Pipeline) {
		p.notifier = n
	}
}

func WithReportsDir(dir string) Option {
	return func(p *Pipeline) {
		p.reportsDir = dir
	}
}

func New(cfg *config.Config, reg *agent.Registry, opts ...Option) *Pipeline {
	if cfg == nil {
		cfg = config.Default()
	}
	p := &Pipeline{
		cfg: cfg,
		reg: reg,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.notifier == nil {
		p.notifier = notifier.NewConsoleNotifier()
	}
	return p
}

type RunConfig struct {
	Feature       string
	TaskDesc      string
	TargetDir     string
	RetryFrom     string
}

func (p *Pipeline) Run(ctx context.Context, runCfg RunConfig) error {
	task := &runtime.Task{
		Feature:      runCfg.Feature,
		TaskDesc:     runCfg.TaskDesc,
		TargetDir:    runCfg.TargetDir,
		ArtifactRoot: filepath.Join(runCfg.TargetDir, ".ai-team", "artifacts"),
	}

	if p.reportsDir == "" {
		p.reportsDir = filepath.Join(runCfg.TargetDir, ".ai-team", "reports")
	}
	if p.targetDir == "" {
		p.targetDir = runCfg.TargetDir
	}

	var results []notifier.StageResult
	var artifacts []runtime.Artifact
	var lastErr error

	agentNames := p.cfg.AgentNames()
	totalStages := len(agentNames)

	startTime := time.Now()

	ps := ui.NewPipelineStatus(filepath.Base(runCfg.TargetDir), runCfg.Feature, totalStages)

	skipUntil := -1
	if runCfg.RetryFrom != "" {
		for i, name := range agentNames {
			if name == runCfg.RetryFrom {
				skipUntil = i
				break
			}
		}
		if skipUntil == -1 {
			return fmt.Errorf("агент %s не найден в пайплайне", runCfg.RetryFrom)
		}
		fmt.Printf("%s  %s: перезапуск с %s\n",
			ui.Colorize("⟳", ui.ColorYellow),
			ui.Colorize("Retry", ui.ColorCyan),
			ui.Colorize(runCfg.RetryFrom, ui.ColorYellow))
	}

	retryCounts := make(map[string]int)

	for i := 0; i < len(agentNames); i++ {
		name := agentNames[i]
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			p.printSummary(runCfg.Feature, results)
			return lastErr
		default:
		}

		if skipUntil >= 0 && i < skipUntil {
			fmt.Printf("  %s %s пропущен\n", ui.Colorize("⏭", ui.ColorCyan), ui.Colorize(name, ui.ColorYellow))
			artifacts = append(artifacts, p.collectExisting(task, name)...)
			continue
		}

		ps.StartAgent(i+1, name)

		stageStart := time.Now()
		r := notifier.StageResult{
			Name:        name,
			StageIndex:  i + 1,
			TotalStages: totalStages,
		}

		fmt.Printf("\n%s %s\n",
			ui.Colorize("▶", ui.ColorCyan),
			ui.Colorize(name, ui.ColorBold+ui.ColorYellow))

		a, err := p.reg.Load(name)
		if err != nil {
			r.Err = fmt.Errorf("ошибка загрузки агента %s: %w", name, err)
			r.Status = "failed"
			r.Duration = time.Since(stageStart)
			results = append(results, r)
			_ = p.notifier.Notify(ctx, r)
			lastErr = r.Err
			break
		}

		agentCfg := p.cfg.AgentConfig(name)

		if agentCfg != nil && agentCfg.CLI != "" {
			a.CLI = agentCfg.CLI
		}
		if agentCfg != nil && agentCfg.Model != "" {
			if a.CLI == "" {
				a.CLI = p.cfg.CLI
			}
		}

		var inputs []runtime.Artifact
		for inName, inPath := range a.Inputs {
			replaced := runtime.ReplaceVars(inPath, runCfg.Feature)
			fullPath := filepath.Join(task.ArtifactRoot, replaced)

			info, err := os.Stat(fullPath)
			if err != nil {
				r.Err = fmt.Errorf("агент %s: вход %s (%s) не найден: %w", name, inName, fullPath, err)
				r.Status = "failed"
				r.Duration = time.Since(stageStart)
				results = append(results, r)
				_ = p.notifier.Notify(ctx, r)
				lastErr = r.Err
				break
			}

			fmt.Printf("  %s %s %s(%s, %d байт)\n",
				ui.Colorize("→", ui.ColorBlue),
				inName,
				ui.Colorize(fullPath, ui.ColorBlue),
				info.ModTime().Format(time.RFC3339),
				info.Size(),
			)

			if !info.IsDir() {
				inputs = append(inputs, runtime.Artifact{
					Name:    inName,
					Path:    fullPath,
					Size:    info.Size(),
					ModTime: info.ModTime(),
				})
			}
			r.Inputs = append(r.Inputs, runtime.Artifact{
				Name: inName,
				Path: fullPath,
				Size: info.Size(),
				ModTime: info.ModTime(),
			})
		}

		if r.Err != nil {
			results = append(results, r)
			break
		}

		rt, err := runtime.NewRuntime(a.RuntimeType)
		if err != nil {
			r.Err = fmt.Errorf("ошибка создания runtime для %s: %w", name, err)
			r.Status = "failed"
			r.Duration = time.Since(stageStart)
			results = append(results, r)
			_ = p.notifier.Notify(ctx, r)
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
			r.Status = "failed"
			r.Duration = time.Since(stageStart)
			results = append(results, r)
			_ = p.notifier.Notify(ctx, r)
			lastErr = r.Err
			break
		}

		for outName, outPath := range a.Outputs {
			replaced := runtime.ReplaceVars(outPath, runCfg.Feature)
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
			fmt.Printf("  %s %s %s(%s, %d байт)\n",
				ui.Colorize("✓", ui.ColorGreen),
				ui.Colorize(outName, ui.ColorBold),
				ui.Colorize(fullPath, ui.ColorBlue),
				info.ModTime().Format(time.RFC3339),
				info.Size(),
			)
		}

		if r.Err != nil {
			r.Status = "failed"
			r.Duration = time.Since(stageStart)
			results = append(results, r)
			_ = p.notifier.Notify(ctx, r)
			lastErr = r.Err
			break
		}

		// Git Diff Guard: если у агента нет outputs, проверяем git diff
		if len(a.Outputs) == 0 && isCoderLike(name) && hasGitDir(runCfg.TargetDir) {
			if !hasGitChanges(runCfg.TargetDir) {
				r.Err = fmt.Errorf("агент %s не создал изменений в коде", name)
				r.Status = "failed"
				r.Duration = time.Since(stageStart)
				results = append(results, r)
				_ = p.notifier.Notify(ctx, r)
				lastErr = r.Err
				break
			}
		}

		r.Status = "passed"
		r.Duration = time.Since(stageStart)
		results = append(results, r)

		if err := p.notifier.Notify(ctx, r); err != nil {
			fmt.Fprintf(os.Stderr, "  %s notifier error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
		}

		if err := report.GenerateStageReport(p.reportsDir, runCfg.Feature, name, r, task.ArtifactRoot); err != nil {
			fmt.Fprintf(os.Stderr, "  %s report error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
		}

		// Workflow Transition: by_confirm
		if agentCfg != nil && agentCfg.Transition == "by_confirm" {
			if i+1 < len(agentNames) {
				nextAgent := agentNames[i+1]
				if isTerminalStdin() {
					for {
						ans := promptContinue(name, nextAgent)
						switch ans {
						case "y", "":
							goto afterTransition
						case "n":
							lastErr = fmt.Errorf("пайплайн остановлен пользователем после %s", name)
							p.printSummary(runCfg.Feature, results)
							ps.Finalize()
							return lastErr
						case "diff":
							diffCmd := exec.Command("git", "--no-pager", "diff")
							diffCmd.Dir = runCfg.TargetDir
							out, _ := diffCmd.Output()
							fmt.Println(string(out))
						case "summary":
							s := readStageSummary(task.ArtifactRoot, runCfg.Feature, name)
							fmt.Println(s)
						default:
							fmt.Printf("  неизвестный ответ: %s (ожидалось Y/n/diff/summary)\n", ans)
						}
					}
				}
			}
		}
	afterTransition:

		// Loopback on REJECTED: проверяем вердикт reviewer-подобного агента
		if agentCfg != nil && agentCfg.MaxRetries > 0 && isReviewerLike(name) {
			verdict := readVerdictFromDir(task.ArtifactRoot, runCfg.Feature, name)
			if verdict == "REJECTED" || verdict == "CHANGES_REQUESTED" {
				retriesDone := retryCounts[name]
				if retriesDone < agentCfg.MaxRetries {
					coderIndex := findAgentIndex(agentNames, "coder")
					if coderIndex >= 0 && coderIndex < i {
						if isTerminalStdin() {
							ans := promptRetry(name, retriesDone, agentCfg.MaxRetries)
							if ans == "y" || ans == "" {
								retryCounts[name] = retriesDone + 1
								// Откатываем results до coder-а
								results = results[:coderIndex]
								// Возвращаемся к coder-у
								i = coderIndex - 1
								continue
							} else if ans == "diff" {
								diffCmd := exec.Command("git", "--no-pager", "diff")
								diffCmd.Dir = runCfg.TargetDir
								out, _ := diffCmd.Output()
								fmt.Println(string(out))
								i--
								continue
							}
						}
					}
				}
			}
		}

		ps.DoneAgent(name)
	}

	endTime := time.Now()

	if err := report.GenerateFinalReport(p.reportsDir, runCfg.Feature, results, startTime, endTime, task.ArtifactRoot); err != nil {
		fmt.Fprintf(os.Stderr, "  %s final report error: %v\n", ui.Colorize("⚠", ui.ColorYellow), err)
	}

	ps.Finalize()

	p.printSummary(runCfg.Feature, results)
	return lastErr
}

func (p *Pipeline) collectExisting(task *runtime.Task, name string) []runtime.Artifact {
	dir := filepath.Join(task.ArtifactRoot, name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var arts []runtime.Artifact
	for _, e := range entries {
		if !e.IsDir() {
			fullPath := filepath.Join(dir, e.Name())
			info, _ := e.Info()
			arts = append(arts, runtime.Artifact{
				Name:    e.Name(),
				Path:    fullPath,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}
	}
	return arts
}

func (p *Pipeline) printSummary(feature string, results []notifier.StageResult) {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	title := fmt.Sprintf("=== ИТОГ ПАЙПЛАЙНА: %s ===", feature)
	fmt.Fprintf(w, "%s\n", ui.Colorize(title, ui.ColorBold))

	fmt.Fprintf(w, "%s\t%s\t\t%s\n",
		ui.Colorize("Этап", ui.ColorCyan),
		ui.Colorize("Статус", ui.ColorCyan),
		ui.Colorize("Результат", ui.ColorCyan),
	)
	fmt.Fprintf(w, "───\t───\t\t───\n")

	for _, r := range results {
		status := ui.ColoredStatus(r.Err == nil)

		var resultStr string
		if r.Err != nil {
			resultStr = ui.Colorize(shortenError(r.Err), ui.ColorRed)
		} else {
			var labels []string
			for _, out := range r.Outputs {
				label := out.Name
				if verdict := readVerdict(out.Path); verdict != "" {
					label += " (" + verdict + ")"
				}
				labels = append(labels, ui.Colorize(label, ui.ColorGreen))
			}
			if len(labels) == 0 {
				labels = append(labels, "—")
			}
			resultStr = strings.Join(labels, ", ")
		}

		fmt.Fprintf(w, "%s\t%s\t\t%s\n",
			ui.Colorize(r.Name, ui.ColorYellow),
			status,
			resultStr,
		)
	}

	if p.reportsDir != "" {
		fmt.Fprintf(w, "\n%s  %s\n",
			ui.Colorize("📄", ui.ColorBold),
			ui.Colorize("Report: "+filepath.Join(p.reportsDir, feature, "index.html"), ui.ColorCyan),
		)
	}

	w.Flush()
	fmt.Println()
}

func (p *Pipeline) Agents() []string {
	return p.cfg.AgentNames()
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
	if strings.Contains(msg, "не создан") {
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
	if len(msg) > 50 {
		return msg[:50] + "..."
	}
	return msg
}
