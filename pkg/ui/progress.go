package ui

import (
	"fmt"
	"strings"
)

type ProgressBar struct {
	feature   string
	total     int
	current   int
	barWidth  int
}

func NewProgressBar(feature string, total int) *ProgressBar {
	return &ProgressBar{
		feature:  feature,
		total:    total,
		current:  0,
		barWidth: 20,
	}
}

func (pb *ProgressBar) Next(agent string) {
	pb.current++
	pb.render(agent)
}

func (pb *ProgressBar) AdvanceTo(index int, agent string) {
	pb.current = index
	pb.render(agent)
}

func (pb *ProgressBar) Clear() {
	if IsTerminal() {
		fmt.Print("\033[2K\r")
	}
}

func (pb *ProgressBar) render(agent string) {
	if !IsTerminal() {
		return
	}
	ratio := float64(pb.current) / float64(pb.total)
	filled := int(ratio * float64(pb.barWidth))
	if filled > pb.barWidth {
		filled = pb.barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", pb.barWidth-filled)

	fmt.Printf("\r%s[ai-team]%s %s | %s%s (%d/%d) %s",
		ColorBold, ColorReset,
		Colorize(pb.feature, ColorCyan),
		Colorize(agent, ColorYellow), ColorReset,
		pb.current, pb.total,
		bar,
	)
}

func (pb *ProgressBar) Done() {
	pb.current = pb.total
	if IsTerminal() {
		pb.render("")
		fmt.Println()
	}
}
