package ui

import (
	"fmt"
	"os"
)

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorBold   = "\033[1m"
)

func Colorize(text, color string) string {
	if !IsTerminal() {
		return text
	}
	return color + text + ColorReset
}

func IsTerminal() bool {
	stat, _ := os.Stdout.Stat()
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func ColoredStatus(success bool) string {
	if success {
		return Colorize("✓", ColorGreen)
	}
	return Colorize("✗", ColorRed)
}

func FormatTime(duration interface{}) string {
	switch d := duration.(type) {
	case float64:
		if d < 60 {
			return fmt.Sprintf("%.1fs", d)
		}
		return fmt.Sprintf("%.0fm%.0fs", d/60, float64(int(d)%60))
	}
	return ""
}

func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
