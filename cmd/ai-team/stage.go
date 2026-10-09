package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

type stageApprovalStore interface {
	List(runID string) ([]approval.PendingApproval, error)
	Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error)
}

type localStageApprovalController struct{ stageApprovalStore }

func (c localStageApprovalController) Approvals(runID string) ([]approval.PendingApproval, error) {
	return c.List(runID)
}

func cmdStage() {
	if len(os.Args) < 3 || os.Args[2] != "submit" {
		fatal("Использование: ai-team stage submit <run_id> --stage <id> (--md <file> | --text <text> | --link <url> --kind <kind> | --approve) [--note <text>] [--description <text>]")
	}
	cmdStageSubmit(os.Args[3:])
}

func cmdStageSubmit(args []string) {
	flags := flag.NewFlagSet("stage submit", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	dbPath := flags.String("db", "", "SQLite approvals DB внутри .ai-team; по умолчанию выбирается существующая web.db")
	stageID := flags.String("stage", "", "ID human stage")
	mdPath := flags.String("md", "", "Путь к markdown-файлу")
	textValue := flags.String("text", "", "Markdown-текст")
	linkURL := flags.String("link", "", "HTTP(S) URL результата")
	linkKind := flags.String("kind", "", "Вид ссылки: pr, build или other")
	approve := flags.Bool("approve", false, "Одобрить stage с необязательным текстом --note")
	note := flags.String("note", "", "Короткий комментарий к результату; для approve — текст решения")
	description := flags.String("description", "", "Что сделано; пропуск создаст description_missing")
	actorID := flags.String("actor", os.Getenv("USER"), "Идентификатор человека")
	actorRole := flags.String("role", "", "Роль человека; если не задана, берётся единственная роль stage")
	if err := flags.Parse(interspersedStageSubmitArgs(args)); err != nil {
		fatal("Ошибка аргументов stage submit: %v", err)
	}
	if flags.NArg() != 1 || strings.TrimSpace(*stageID) == "" {
		fatal("stage submit требует <run_id> и --stage")
	}
	runID := strings.TrimSpace(flags.Arg(0))
	targetDir, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(targetDir)

	inputKinds := 0
	if *mdPath != "" {
		inputKinds++
	}
	if *textValue != "" {
		inputKinds++
	}
	if *linkURL != "" {
		inputKinds++
	}
	if *approve {
		inputKinds++
	}
	if inputKinds != 1 {
		fatal("выберите ровно один вариант: --md, --text, --link или --approve")
	}
	command := humanartifact.SubmissionCommand{
		StageID: strings.TrimSpace(*stageID), Note: *note, Description: *description,
		ActorID: strings.TrimSpace(*actorID), ActorRole: strings.TrimSpace(*actorRole),
	}
	switch {
	case *mdPath != "":
		data, readErr := safeio.ReadRegularFile(*mdPath, humanartifact.MaxContentBytes)
		if readErr != nil {
			fatal("Не удалось прочитать markdown: %v", readErr)
		}
		command.Result, command.Content = "md", string(data)
	case *textValue != "":
		command.Result, command.Content = "md", *textValue
	case *linkURL != "":
		command.Result, command.Content, command.LinkKind = "link", *linkURL, strings.TrimSpace(*linkKind)
	case *approve:
		command.Result, command.Note = "approve", *note
	}
	if command.ActorID == "" {
		command.ActorID = "local-user"
	}

	approvals, closeStore, err := openStageApprovalStore(targetDir, *dbPath)
	if err != nil {
		fatal("Не удалось открыть approval store: %v", err)
	}
	if closeStore != nil {
		defer func() { _ = closeStore() }()
	}
	if command.ActorRole == "" {
		values, listErr := approvals.List(runID)
		if listErr != nil {
			fatal("Не удалось прочитать approvals: %v", listErr)
		}
		roles := map[string]bool{}
		for _, value := range values {
			if value.Kind == approval.KindInput && value.Trigger == "human_input" && value.FromStage == command.StageID && value.Status == approval.StatusPending {
				for _, role := range value.RequiredRoles {
					roles[role] = true
				}
			}
		}
		if len(roles) != 1 {
			fatal("укажите --role: stage должен иметь ровно одну назначенную роль")
		}
		for role := range roles {
			command.ActorRole = role
		}
	}
	store, err := humanartifact.New(targetDir)
	if err != nil {
		fatal("Не удалось открыть хранилище результатов: %v", err)
	}
	result, err := store.Submit(localStageApprovalController{stageApprovalStore: approvals}, runID, command)
	if err != nil {
		fatal("Сдача результата отклонена: %v", err)
	}
	logging.Printf("✓ Результат этапа %s сдан: версия %d, sha256 %s\n", command.StageID, result.Revision.Revision, result.Revision.SHA256)
	if logging.GetMode() == logging.ModeJSON || logging.GetMode() == logging.ModeQuiet {
		logging.Emit(logging.Record{Level: "ok", Command: "stage submit", Type: "human_submission",
			Message: "Результат этапа сдан", Data: map[string]any{
				"run_id": runID, "stage_id": command.StageID, "approval_id": result.Approval.ID,
				"version": result.Revision.Revision, "sha256": result.Revision.SHA256,
			}, Exit: 0})
	}
}

func interspersedStageSubmitArgs(args []string) []string {
	knownValueFlags := map[string]bool{
		"--target": true, "--db": true, "--stage": true, "--md": true, "--text": true,
		"--link": true, "--kind": true, "--note": true, "--description": true,
		"--actor": true, "--role": true,
	}
	options, positionals := make([]string, 0, len(args)), make([]string, 0, 1)
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
		if knownValueFlags[flagName] && !strings.Contains(arg, "=") && index+1 < len(args) {
			index++
			options = append(options, args[index])
		}
	}
	return append(options, positionals...)
}

func openStageApprovalStore(targetDir, dbPath string) (stageApprovalStore, func() error, error) {
	controlRoot := filepath.Join(targetDir, ".ai-team")
	if err := safeio.ValidateTree(controlRoot); err != nil {
		return nil, nil, fmt.Errorf("unsafe control root: %w", err)
	}
	if dbPath == "" {
		candidate := filepath.Join(controlRoot, "web.db")
		if info, err := os.Lstat(candidate); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, nil, errors.New("web.db must be a regular file without symlink")
			}
			dbPath = candidate
		} else if !os.IsNotExist(err) {
			return nil, nil, err
		}
	}
	if dbPath == "" {
		store, err := approval.NewStore(targetDir)
		return store, nil, err
	}
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(targetDir, dbPath)
	}
	dbPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, nil, err
	}
	relative, err := filepath.Rel(controlRoot, dbPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, nil, errors.New("SQLite approvals DB must be inside .ai-team")
	}
	if err := safeio.RejectSymlink(dbPath); err != nil {
		return nil, nil, err
	}
	sqlite, err := approval.NewSQLiteStore(dbPath)
	if err != nil {
		return nil, nil, err
	}
	return sqlite, sqlite.Close, nil
}
