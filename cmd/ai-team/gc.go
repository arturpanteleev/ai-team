package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/retention"
)

func cmdGC() {
	flags := flag.NewFlagSet("gc", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	olderThan := flags.Duration("older-than", 720*time.Hour, "Возраст terminal-ранов для уборки state и (с --prune-runs) evidence")
	keepLast := flags.Int("keep-last", 20, "Сколько самых свежих terminal-ранов защищать всегда")
	dryRun := flags.Bool("dry-run", false, "Только напечатать план удаления, ничего не трогая")
	pruneRuns := flags.Bool("prune-runs", false, "Разрешить удаление immutable run evidence (.ai-team/runs)")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fatal("Ошибка аргументов gc: %v", err)
	}
	if flags.NArg() != 0 {
		fatal("Неожиданные аргументы gc: %s", strings.Join(flags.Args(), " "))
	}
	if *keepLast < 0 {
		fatal("--keep-last не может быть отрицательным")
	}
	absTarget, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(absTarget)

	// P1-6: retention-контракт настраивается в config (retention).
	// Явные CLI-флаги имеют приоритет над конфигом. Отсутствие config.yaml —
	// канонические дефолты.
	cfg, cfgErr := config.Load(filepath.Join(absTarget, ".ai-team", "config.yaml"))
	if cfgErr != nil {
		if _, statErr := os.Stat(filepath.Join(absTarget, ".ai-team", "config.yaml")); !os.IsNotExist(statErr) {
			fatal("Ошибка загрузки конфига: %v", cfgErr)
		}
		cfg = &config.Config{}
	}
	if err := cfg.Retention.Validate(); err != nil {
		fatal("Ошибка retention-конфига: %v", err)
	}
	flagVisited := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { flagVisited[f.Name] = true })
	if !flagVisited["older-than"] {
		d, err := cfg.Retention.EffectiveOlderThanDuration()
		if err != nil {
			fatal("retention.older_than: %v", err)
		}
		*olderThan = d
	}
	if !flagVisited["keep-last"] {
		*keepLast = cfg.Retention.EffectiveKeepLast()
	}
	if cfg.Retention != nil && cfg.Retention.PruneRuns {
		*pruneRuns = true
	}

	// gc мутирует control-каталог — как pipeline, он обязан держать
	// workspace lock и отказываться работать при параллельном run.
	lock, err := evidence.AcquireWorkspaceLock(absTarget)
	if err != nil {
		fatal("gc: %v", err)
	}
	defer lock.Close()

	options := retention.Options{
		Target: absTarget, OlderThan: *olderThan,
		KeepLast: *keepLast, PruneRuns: *pruneRuns,
	}
	plan, err := retention.Build(options)
	if err != nil {
		fatal("gc: %v", err)
	}

	printGCPlan(plan)
	for _, skipped := range plan.Skipped {
		fmt.Printf("  пропущено: %s (%s)\n", skipped.Path, skipped.Reason)
	}
	if *dryRun {
		fmt.Println("dry-run: ничего не удалено")
		return
	}
	if err := plan.Execute(absTarget); err != nil {
		fatal("gc: %v", err)
	}
	fmt.Printf("✓ gc завершён: удалено объектов: %d\n", len(plan.Actions))
}

func printGCPlan(plan *retention.Plan) {
	type row struct {
		category string
		objects  int
		bytes    int64
	}
	rows := map[string]*row{}
	var order []string
	for _, action := range plan.Actions {
		current, ok := rows[action.Category]
		if !ok {
			current = &row{category: action.Category}
			rows[action.Category] = current
			order = append(order, action.Category)
		}
		current.objects++
		current.bytes += action.Bytes
	}
	sort.Slice(order, func(i, j int) bool {
		rank := map[string]int{retention.CategoryWorktrees: 0, retention.CategoryRuns: 1, retention.CategoryState: 2}
		return rank[order[i]] < rank[order[j]]
	})
	fmt.Printf("%-12s %10s %14s\n", "Категория", "Объектов", "Байт")
	for _, category := range order {
		current := rows[category]
		fmt.Printf("%-12s %10d %14d\n", current.category, current.objects, current.bytes)
	}
	fmt.Printf("%-12s %10d %14d\n", "Итого", len(plan.Actions), plan.TotalBytes())
}
