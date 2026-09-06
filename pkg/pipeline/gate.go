package pipeline

import (
	"fmt"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

const gateRule = "═══════════════════════════════════════════════"

// showGateSummary — резюме этапа перед gate_after.
func showGateSummary(agentName string, result notifier.StageResult, totalStages int) {
	logging.Printf("\n%s\n", ui.Colorize(gateRule, ui.ColorCyan))
	logging.Printf("  %s %s (этап %d/%d)\n",
		ui.Colorize("Gate:", ui.ColorBold),
		ui.Colorize(agentName, ui.ColorYellow),
		result.StageIndex, totalStages)
	logging.Printf("%s\n", ui.Colorize(gateRule, ui.ColorCyan))
	logging.Printf("  %s %s\n", ui.Colorize("Статус:", ui.ColorCyan), result.Status)
	if result.Verdict != "" {
		logging.Printf("  %s %s\n", ui.Colorize("Вердикт:", ui.ColorCyan), string(result.Verdict))
	}
	logging.Printf("  %s %s\n", ui.Colorize("Длительность:", ui.ColorCyan), result.Duration.Round(time.Second))
	if len(result.Outputs) > 0 {
		logging.Printf("  %s\n", ui.Colorize("Артефакты:", ui.ColorCyan))
		for _, out := range result.Outputs {
			logging.Printf("    - %s (%s, %d байт)\n", out.Name, out.Path, out.Size)
		}
	}
	logging.Printf("%s\n", ui.Colorize(gateRule, ui.ColorCyan))
}

// showPipelineSummary — сводка по выполненным этапам перед gate_before:
// количество успешных/с ошибками, вердикты, общее время (требование pipeline-gates).
func showPipelineSummary(results []notifier.StageResult) {
	passed, failed := 0, 0
	var total time.Duration
	for _, r := range results {
		if r.Superseded || r.Status == notifier.StatusInvalidated {
			continue
		}
		if r.Err == nil && r.Status == notifier.StatusPassed {
			passed++
		} else {
			failed++
		}
		total += r.Duration
	}
	logging.Printf("\n%s\n", ui.Colorize(gateRule, ui.ColorCyan))
	logging.Printf("  %s этапов: %d, успешных: %s, с ошибками: %s, время: %s\n",
		ui.Colorize("Сводка:", ui.ColorBold),
		len(results),
		ui.Colorize(fmt.Sprintf("%d", passed), ui.ColorGreen),
		ui.Colorize(fmt.Sprintf("%d", failed), ui.ColorRed),
		total.Round(time.Second))
	for _, r := range results {
		if r.Superseded || r.Status == notifier.StatusInvalidated {
			continue
		}
		mark := "✓"
		if r.Err != nil || r.Status != notifier.StatusPassed {
			mark = "✗"
		}
		line := fmt.Sprintf("    %s %s", mark, r.Name)
		if r.Verdict != "" {
			line += " (" + string(r.Verdict) + ")"
		}
		logging.Printf("%s\n", line)
	}
	logging.Printf("%s\n", ui.Colorize(gateRule, ui.ColorCyan))
}
