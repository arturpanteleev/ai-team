package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

// deliver.go (V0-9): ручной повтор отложенной (deferred) доставки. Post-terminal
// хук исполняет delivery в той же инвокации run; если он упал (сетевой сбой
// push, ошибка контроллера), доставка повторяема через эту команду —
// enforcement детерминированный (plan hash из delivery_deferred event).
func cmdDeliver() {
	deliverFlags := flag.NewFlagSet("deliver", flag.ExitOnError)
	target := deliverFlags.String("target", ".", "Путь к целевому проекту (delivery state)")
	feature := deliverFlags.String("feature", "", "Feature name (если не задан — из run manifest)")
	runID := deliverFlags.String("run", "", "Run ID завершённого run с deferred delivery")
	if err := deliverFlags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов deliver: %v", err)
	}
	if *runID == "" {
		fatal("Использование: ai-team deliver --run <run_id> [--target <dir>] [--feature <name>]")
	}
	if len(*runID) > 1024 || strings.ContainsAny(*runID, `/\`) {
		fatal("недопустимый run_id")
	}
	absolute, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absolute)
	runDir := filepath.Join(absolute, ".ai-team", "runs", *runID)

	record, err := pipeline.New(nil, nil).DeliverDeferred(runDir, *feature, absolute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Deliver run %s: %v\n", *runID, ui.Colorize(err.Error(), ui.ColorRed))
		os.Exit(exitFailed)
	}
	fmt.Printf("✓ Deliver run %s завершён: commit=%s pr=%s\n", *runID, record.CommitSHA, record.PRURL)
	for _, trailer := range record.Trailers {
		fmt.Printf("    %s\n", trailer)
	}
}
