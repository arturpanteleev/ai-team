package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

type taskRunSummary struct {
	RunID           string          `json:"run_id"`
	Feature         string          `json:"feature"`
	Task            string          `json:"task"`
	Phase           lifecycle.Phase `json:"phase"`
	NextStage       string          `json:"next_stage,omitempty"`
	PendingApproval string          `json:"pending_approval_id,omitempty"`
	AttemptOrdinal  int             `json:"attempt_ordinal"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type taskAttemptView struct {
	AttemptID  string    `json:"attempt_id"`
	Stage      string    `json:"stage"`
	Executor   string    `json:"executor,omitempty"`
	Status     string    `json:"status,omitempty"`
	Verdict    string    `json:"verdict,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type taskTokenView struct {
	Known             bool   `json:"known"`
	InputTokens       int64  `json:"input_tokens,omitempty"`
	OutputTokens      int64  `json:"output_tokens,omitempty"`
	TotalTokens       int64  `json:"total_tokens,omitempty"`
	SubscriptionShare string `json:"approximate_subscription_share"`
}

type taskPendingView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	StageID string `json:"stage_id,omitempty"`
	Result  string `json:"result,omitempty"`
	Status  string `json:"status"`
}

type taskApprovalStore interface {
	List(string) ([]approval.PendingApproval, error)
	Load(string, string) (approval.PendingApproval, error)
}

type taskRunView struct {
	RunID           string            `json:"run_id"`
	Feature         string            `json:"feature"`
	Task            string            `json:"task"`
	Phase           lifecycle.Phase   `json:"phase"`
	NextStage       string            `json:"next_stage,omitempty"`
	PendingApproval *taskPendingView  `json:"pending_approval,omitempty"`
	Attempts        []taskAttemptView `json:"attempts"`
	TokenUsage      taskTokenView     `json:"token_usage"`
	NextAction      string            `json:"next_action,omitempty"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

func cmdTaskRuns() {
	flags := flag.NewFlagSet("runs", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	_ = flags.Parse(os.Args[2:])
	if flags.NArg() != 0 {
		fatal("Использование: ai-team runs [--target <dir>]")
	}
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)
	states, err := listTaskRunStates(absolute)
	if err != nil {
		fatal("Не удалось прочитать список run: %v", err)
	}
	rows := make([]taskRunSummary, 0, len(states))
	for _, state := range states {
		rows = append(rows, summarizeTaskRun(state))
	}
	if logging.GetMode() != logging.ModeJSON {
		if len(rows) == 0 {
			logging.Printf("В проекте пока нет run.\n")
		} else {
			logging.Printf("%-34s %-18s %-12s %-18s %s\n", "Run", "Feature", "Статус", "Этап", "Обновлён")
			for _, row := range rows {
				logging.Printf("%-34s %-18s %-12s %-18s %s\n", row.RunID, row.Feature, row.Phase, row.NextStage, row.UpdatedAt.Format("2006-01-02 15:04"))
			}
		}
	}
	logging.Emit(logging.Record{Level: "ok", Command: "runs", Type: "runs", Message: fmt.Sprintf("Найдено run: %d", len(rows)), Data: map[string]any{"runs": rows, "count": len(rows)}, Exit: exitOK})
}

func listTaskRunStates(target string) ([]lifecycle.State, error) {
	if _, err := safeio.ExistingDir(target, ".ai-team"); err != nil {
		return nil, err
	}
	directory, err := safeio.ExistingDir(target, ".ai-team", "state", "runs")
	if errors.Is(err, os.ErrNotExist) {
		return []lifecycle.State{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		return nil, err
	}
	states := make([]lifecycle.State, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("lifecycle state %s is a symlink", entry.Name())
		}
		runID := strings.TrimSuffix(entry.Name(), ".json")
		state, loadErr := store.Load(runID)
		if loadErr != nil {
			return nil, loadErr
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].UpdatedAt.Equal(states[j].UpdatedAt) {
			return states[i].RunID > states[j].RunID
		}
		return states[i].UpdatedAt.After(states[j].UpdatedAt)
	})
	return states, nil
}

func summarizeTaskRun(state lifecycle.State) taskRunSummary {
	return taskRunSummary{
		RunID: state.RunID, Feature: state.Feature, Task: state.Task, Phase: state.Phase,
		NextStage: state.NextStage, PendingApproval: state.PendingApprovalID,
		AttemptOrdinal: state.AttemptOrdinal, UpdatedAt: state.UpdatedAt,
	}
}

func cmdTaskShow() {
	flags := flag.NewFlagSet("show", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	_ = flags.Parse(interspersedTaskArgs(os.Args[2:], map[string]bool{"--target": true}))
	if flags.NArg() != 1 {
		fatal("Использование: ai-team show <run_id> [--target <dir>]")
	}
	runID := flags.Arg(0)
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)
	view, err := loadTaskRunView(absolute, runID)
	if err != nil {
		fatal("Не удалось прочитать run %s: %v", runID, err)
	}
	printTaskRunView(view)
}

func loadTaskRunView(target, runID string) (taskRunView, error) {
	stateStore, err := lifecycle.NewStore(target)
	if err != nil {
		return taskRunView{}, err
	}
	state, err := stateStore.Load(runID)
	if err != nil {
		return taskRunView{}, err
	}
	runDir := filepath.Join(target, ".ai-team", "runs", runID)
	var replayed evidence.ReplayedRun
	if state.Phase == lifecycle.PhaseTerminal {
		replayed, err = evidence.VerifyTerminalEvidence(runDir, runID, target)
	} else {
		err = evidence.VerifyResumeEvidence(runDir)
		if err == nil {
			replayed, err = evidence.ReplayEventLog(filepath.Join(runDir, "events.jsonl"), runID)
		}
	}
	if err != nil {
		return taskRunView{}, fmt.Errorf("evidence verification: %w", err)
	}
	manifest, err := readTaskRunManifest(runDir)
	if err != nil {
		return taskRunView{}, fmt.Errorf("run manifest: %w", err)
	}
	if manifest.RunID != state.RunID || manifest.Feature != state.Feature || manifest.TargetDir != target ||
		manifest.ConfigSHA256 != state.ConfigSHA256 || manifest.ResolvedWorkflowSHA256 != state.WorkflowSHA256 {
		return taskRunView{}, errors.New("lifecycle identity differs from run evidence")
	}
	view := taskRunView{
		RunID: state.RunID, Feature: state.Feature, Task: state.Task, Phase: state.Phase,
		NextStage: state.NextStage, UpdatedAt: state.UpdatedAt, Attempts: make([]taskAttemptView, 0, len(replayed.Attempts)),
		TokenUsage: taskTokenView{SubscriptionShare: "нет данных"},
	}
	for _, attempt := range replayed.Attempts {
		view.Attempts = append(view.Attempts, taskAttemptView{
			AttemptID: attempt.AttemptID, Stage: attempt.Stage, Executor: attempt.Executor,
			Status: attempt.Status, Verdict: attempt.Verdict, StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt,
		})
	}
	if state.Phase == lifecycle.PhaseWaiting {
		store, closeStore, storeErr := openTaskApprovalStore(target)
		if storeErr != nil {
			return taskRunView{}, storeErr
		}
		if closeStore != nil {
			defer func() { _ = closeStore() }()
		}
		pending, loadErr := store.Load(runID, state.PendingApprovalID)
		if loadErr != nil {
			return taskRunView{}, loadErr
		}
		pendingView := taskPendingView{ID: pending.ID, Kind: string(pending.Kind), Status: string(pending.Status)}
		if pending.Kind == approval.KindInput {
			var payload approval.InputPayload
			if json.Unmarshal(pending.Payload, &payload) == nil {
				pendingView.StageID = payload.StageID
				pendingView.Result = payload.Result
			}
		}
		view.PendingApproval = &pendingView
		view.NextAction = taskNextAction(target, runID, state, pending)
	} else {
		view.NextAction = taskNextAction(target, runID, state, approval.PendingApproval{})
	}
	envelope, usageErr := metrics.ReadUsageEnvelope(target, runID)
	if errors.Is(usageErr, os.ErrNotExist) {
		reserved, reservationErr := metrics.UsageEnvelopeReservation(target, runID)
		if reservationErr != nil {
			return taskRunView{}, fmt.Errorf("usage reservation: %w", reservationErr)
		}
		if reserved {
			return taskRunView{}, fmt.Errorf("controller usage envelope is missing for run %s", runID)
		}
		envelope, usageErr = readLocalUsageEnvelope(target, runID)
	}
	if usageErr == nil {
		if envelope.HasCompleteTokenUsage() {
			view.TokenUsage = taskTokenView{
				Known: true, InputTokens: envelope.TokensInput, OutputTokens: envelope.TokensOutput,
				TotalTokens:       envelope.TokensInput + envelope.TokensOutput,
				SubscriptionShare: normalizeShareText(approximateSubscriptionShare(target, envelope)),
			}
		}
	} else if !errors.Is(usageErr, os.ErrNotExist) {
		return taskRunView{}, fmt.Errorf("usage envelope: %w", usageErr)
	}
	return view, nil
}

func readTaskRunManifest(runDir string) (evidence.RunManifest, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), 1<<20)
	if err != nil {
		return evidence.RunManifest{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var manifest evidence.RunManifest
	if err := decoder.Decode(&manifest); err != nil {
		return evidence.RunManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return evidence.RunManifest{}, errors.New("run manifest contains trailing JSON")
	}
	return manifest, nil
}

func taskNextAction(target, runID string, state lifecycle.State, pending approval.PendingApproval) string {
	targetArg := " --target " + shellQuote(target)
	if state.Phase == lifecycle.PhaseTerminal {
		return ""
	}
	if state.Phase == lifecycle.PhaseWaiting && pending.Kind == approval.KindInput {
		var payload approval.InputPayload
		if json.Unmarshal(pending.Payload, &payload) == nil && payload.StageID != "" {
			base := "ai-team stage submit " + shellQuote(runID) + " --stage " + shellQuote(payload.StageID)
			switch payload.Result {
			case "approve":
				return base + " --approve --description 'Что сделано'" + targetArg
			case "link":
				return base + " --link 'https://example.invalid/replace-me' --kind " + shellQuote(payload.LinkKind) + " --description 'Что сделано'" + targetArg
			case "md":
				return base + " --md './result.md' --description 'Что сделано'" + targetArg
			}
		}
	}
	if state.Phase == lifecycle.PhaseWaiting && pending.ID != "" {
		return "ai-team decision --target " + shellQuote(target) + " --run " + shellQuote(runID) +
			" --approval " + shellQuote(pending.ID) + " --actor '<actor-id>' --role '<role>' --action '<action>' --subject '<subject-sha256>'"
	}
	if state.Phase == lifecycle.PhaseResumable || state.Phase == lifecycle.PhaseRunning {
		return "ai-team run --resume " + shellQuote(runID) + targetArg
	}
	return ""
}

func taskNextActionForRunError(target, runID string) string {
	if runID == "" || runID == "." || runID == ".." || filepath.Base(runID) != runID {
		return ""
	}
	store, err := lifecycle.NewStore(target)
	if err != nil {
		return ""
	}
	state, err := store.Load(runID)
	if err != nil {
		return ""
	}
	var pending approval.PendingApproval
	if state.Phase == lifecycle.PhaseWaiting {
		approvalStore, closeStore, approvalErr := openTaskApprovalStore(target)
		if approvalErr != nil {
			return ""
		}
		if closeStore != nil {
			defer func() { _ = closeStore() }()
		}
		pending, err = approvalStore.Load(runID, state.PendingApprovalID)
		if err != nil {
			return ""
		}
	}
	return taskNextAction(target, runID, state, pending)
}

func openTaskApprovalStore(target string) (taskApprovalStore, func() error, error) {
	controlRoot := filepath.Join(target, ".ai-team")
	if err := safeio.ValidateTree(controlRoot); err != nil {
		return nil, nil, fmt.Errorf("unsafe control root: %w", err)
	}
	database := filepath.Join(controlRoot, "web.db")
	if info, err := os.Lstat(database); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, nil, errors.New("web.db must be a regular file without symlink")
		}
		store, err := approval.NewSQLiteStore(database)
		if err != nil {
			return nil, nil, err
		}
		return store, store.Close, nil
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	store, err := approval.NewStore(target)
	return store, nil, err
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func taskAutoConfig(cfg *config.Config, registry *agent.Registry) (*config.Config, error) {
	data, err := cfg.Marshal()
	if err != nil {
		return nil, fmt.Errorf("serialize active template: %w", err)
	}
	auto, err := config.ParseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("copy active template: %w", err)
	}
	if len(auto.Stages) == 0 {
		return nil, errors.New("--mode auto поддерживается только для шаблона этапов")
	}
	for index := range auto.Stages {
		stage := &auto.Stages[index]
		if stage.Agent != "" {
			stage.Executor = "agent"
		}
		stage.Confirm = "auto"
	}
	var lookup config.AgentLookup
	if registry != nil {
		lookup = registry
	}
	if err := auto.Validate(lookup); err != nil {
		return nil, fmt.Errorf("active template in auto mode: %w", err)
	}
	return auto, nil
}

func printTaskRunView(view taskRunView) {
	if logging.GetMode() != logging.ModeJSON {
		logging.Printf("Задача:  %s\nRun:     %s\nСтатус:  %s\n", view.Task, view.RunID, view.Phase)
		logging.Printf("Feature: %s\n", view.Feature)
		if view.NextStage != "" {
			logging.Printf("Следующий этап: %s\n", view.NextStage)
		}
		if view.PendingApproval != nil {
			logging.Printf("Ожидает: %s (%s)\n", view.PendingApproval.ID, view.PendingApproval.Kind)
		}
		logging.Printf("Попытки:\n")
		for _, attempt := range view.Attempts {
			state := attempt.Status
			if state == "" {
				state = "выполняется"
			}
			logging.Printf("  %-20s %-8s %-16s %s\n", attempt.Stage, attempt.Executor, state, attempt.Verdict)
		}
		if view.TokenUsage.Known {
			logging.Printf("Токены: %d всего (%d входящих, %d исходящих)\n", view.TokenUsage.TotalTokens, view.TokenUsage.InputTokens, view.TokenUsage.OutputTokens)
		} else {
			logging.Printf("Токены: нет данных\n")
		}
		logging.Printf("Доля подписки (приблизительно): %s\n", view.TokenUsage.SubscriptionShare)
		if view.NextAction != "" {
			logging.Printf("\nСледующая команда (замените значения в примере для сдачи результата):\n  %s\n", view.NextAction)
		}
	}
	logging.Emit(logging.Record{Level: "ok", Command: "show", Type: "run", Message: fmt.Sprintf("Run %s: %s", view.RunID, view.Phase), Data: map[string]any{"run": view}, Exit: exitOK})
}

func normalizeShareText(value string) string {
	if value == "оценка недоступна" || strings.TrimSpace(value) == "" {
		return "нет данных"
	}
	return value
}

func cmdTaskCancel() {
	flags := flag.NewFlagSet("cancel", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	reason := flags.String("reason", "", "Причина отмены")
	_ = flags.Parse(interspersedTaskArgs(os.Args[2:], map[string]bool{"--target": true, "--reason": true}))
	if flags.NArg() != 1 || strings.TrimSpace(*reason) == "" {
		fatal("Использование: ai-team cancel <run_id> --reason <причина> [--target <dir>]")
	}
	runID := flags.Arg(0)
	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absTarget)
	engine := pipeline.NewRunEngine(pipeline.New(config.Default(), nil))
	result, err := engine.Cancel(pipeline.CancelConfig{RunID: runID, TargetDir: absTarget, Reason: strings.TrimSpace(*reason)})
	if err != nil {
		code := exitFailed
		logging.Fail(logging.Record{Level: "error", Command: "cancel", Type: "run", Message: err.Error(), Exit: code}, "%s Не удалось отменить run: %v", ui.Colorize("✗", ui.ColorRed), err)
		os.Exit(code)
	}
	message := "Run отменён: " + result.RunID
	if logging.GetMode() != logging.ModeJSON {
		logging.Printf("%s\n", message)
	}
	logging.Emit(logging.Record{Level: "ok", Command: "cancel", Type: "run", Message: message, Data: map[string]any{"run_id": result.RunID, "outcome": string(result.Outcome), "reason": strings.TrimSpace(*reason)}, Exit: exitOK})
}

func interspersedTaskArgs(args []string, valueFlags map[string]bool) []string {
	options := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		options = append(options, arg)
		flagName := arg
		if equals := strings.IndexByte(flagName, '='); equals >= 0 {
			flagName = flagName[:equals]
		}
		if valueFlags[flagName] && !strings.Contains(arg, "=") && index+1 < len(args) {
			index++
			options = append(options, args[index])
		}
	}
	return append(options, positionals...)
}

// cmdTaskStageAction is the CLI integration seam for stage controls owned by
// the stage-transition capabilities. stage submit is routed by B-31; the
// remaining actions are intentionally connected only when their controller
// APIs land, so the CLI cannot manufacture a lifecycle transition.
func cmdTaskStageAction(action string, args []string) {
	if action != "take" && action != "run-agent" && action != "confirm" && action != "skip" && action != "return" && action != "override" {
		fatal("Неизвестное действие этапа %q", action)
	}
	logging.Fail(logging.Record{Level: "error", Command: "stage", Type: "stage_action", Message: fmt.Sprintf("действие %s пока не подключено к контроллеру задач", action), Exit: exitBlocked}, "Действие этапа %q пока не подключено к контроллеру задач", action)
	os.Exit(exitBlocked)
}
