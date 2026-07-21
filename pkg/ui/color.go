package ui

import (
	"os"

	"github.com/mattn/go-isatty"
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
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func ColoredStatus(success bool) string {
	if success {
		return Colorize("✓", ColorGreen)
	}
	return Colorize("✗", ColorRed)
}

func ColoredStatusBlocked() string {
	return Colorize("⊘", ColorYellow)
}

// Truncate обрезает строку до maxLen рун (безопасно для UTF-8).
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
