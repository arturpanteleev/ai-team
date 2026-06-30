package notifier

import (
	"context"
	"fmt"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/ui"
)

type ConsoleNotifier struct{}

func NewConsoleNotifier() *ConsoleNotifier {
	return &ConsoleNotifier{}
}

func (n *ConsoleNotifier) Notify(ctx context.Context, stage StageResult) error {
	status := ui.ColoredStatus(stage.Err == nil)

	duration := stage.Duration.Truncate(time.Millisecond * 100)

	var msg string
	if stage.Err != nil {
		msg = fmt.Sprintf("%s%s [%s%s] %s %s (%v)",
			ui.ColorBold, ui.Colorize("ai-team", ui.ColorCyan),
			ui.Colorize(stage.Name, ui.ColorYellow), ui.ColorReset,
			status, ui.Colorize(stage.Err.Error(), ui.ColorRed),
			duration,
		)
	} else {
		msg = fmt.Sprintf("%s%s [%s%s] %s (%v)",
			ui.ColorBold, ui.Colorize("ai-team", ui.ColorCyan),
			ui.Colorize(stage.Name, ui.ColorYellow), ui.ColorReset,
			status, duration,
		)
	}

	fmt.Println(msg)
	return nil
}
