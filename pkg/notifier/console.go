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
	var status string
	switch stage.Status {
	case StatusBlocked:
		status = ui.ColoredStatusBlocked()
	case StatusRejected, StatusFailed, StatusCanceled:
		status = ui.ColoredStatus(false)
	case StatusWarning:
		status = ui.Colorize("!", ui.ColorYellow)
	case StatusSkipped:
		status = ui.Colorize("⏸", ui.ColorCyan)
	case StatusInvalidated:
		status = ui.Colorize("↺", ui.ColorCyan)
	default:
		status = ui.ColoredStatus(stage.Status == StatusPassed && stage.Err == nil)
	}

	duration := stage.Duration.Truncate(time.Millisecond * 100)

	var detail string
	switch {
	case stage.ControlStopped && stage.Err != nil:
		detail = " " + ui.Colorize(stage.Err.Error(), ui.ColorYellow)
	case stage.Err != nil:
		detail = " " + ui.Colorize(stage.Err.Error(), ui.ColorRed)
	case stage.Status == StatusBlocked:
		detail = " " + ui.Colorize("BLOCKED: "+stage.Blocker, ui.ColorYellow)
	case stage.Verdict != "":
		detail = " " + ui.Colorize(string(stage.Verdict), ui.ColorCyan)
	}

	fmt.Printf("%s [%s] %s%s (%v)\n",
		ui.Colorize("ai-team", ui.ColorBold+ui.ColorCyan),
		ui.Colorize(stage.Name, ui.ColorYellow),
		status, detail, duration,
	)
	return nil
}
