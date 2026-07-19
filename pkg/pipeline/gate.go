package pipeline

import (
	"fmt"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

type GateType string

const (
	GateAfter  GateType = "after"
	GateBefore GateType = "before"
)

type PipelineGate struct {
	AgentName string
	Type      GateType
}

func findGates(agents []*runtime.Agent) []PipelineGate {
	var gates []PipelineGate
	for _, a := range agents {
		if a.GateAfter {
			gates = append(gates, PipelineGate{AgentName: a.Name, Type: GateAfter})
		}
		if a.GateBefore {
			gates = append(gates, PipelineGate{AgentName: a.Name, Type: GateBefore})
		}
	}
	return gates
}

func hasGate(gates []PipelineGate, agentName string, gateType GateType) bool {
	for _, g := range gates {
		if g.AgentName == agentName && g.Type == gateType {
			return true
		}
	}
	return false
}

func showGateSummary(agentName string, result notifier.StageResult, totalStages int) {
	fmt.Printf("\n%s\n", ui.Colorize("═══════════════════════════════════════════════", ui.ColorCyan))
	fmt.Printf("  %s %s (этап %d/%d)\n",
		ui.Colorize("Gate:", ui.ColorBold),
		ui.Colorize(agentName, ui.ColorYellow),
		result.StageIndex, totalStages)
	fmt.Printf("%s\n", ui.Colorize("═══════════════════════════════════════════════", ui.ColorCyan))
	fmt.Printf("  %s %s\n", ui.Colorize("Статус:", ui.ColorCyan), result.Status)
	fmt.Printf("  %s %s\n", ui.Colorize("Длительность:", ui.ColorCyan), result.Duration.Round(1))
	if len(result.Outputs) > 0 {
		fmt.Printf("  %s\n", ui.Colorize("Артефакты:", ui.ColorCyan))
		for _, out := range result.Outputs {
			fmt.Printf("    - %s (%s)\n", out.Name, out.Path)
		}
	}
	fmt.Printf("%s\n\n", ui.Colorize("═══════════════════════════════════════════════", ui.ColorCyan))
}

func promptGate(agentName string) bool {
	fmt.Printf("%s %s. %s\n",
		ui.Colorize("Gate:", ui.ColorBold),
		ui.Colorize("после "+agentName, ui.ColorYellow),
		ui.Colorize("Продолжить? [Y/n]", ui.ColorBold))
	fmt.Print("> ")
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	return input == "" || strings.ToLower(input) == "y"
}
