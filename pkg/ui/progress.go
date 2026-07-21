package ui

import (
	"fmt"
	"strings"
	"sync"
)

type ProgressBar struct {
	feature  string
	total    int
	current  int
	barWidth int
}

func NewProgressBar(feature string, total int) *ProgressBar {
	return &ProgressBar{
		feature:  feature,
		total:    total,
		current:  0,
		barWidth: 20,
	}
}

// Next увеличивает счётчик выполненных этапов.
func (pb *ProgressBar) Next(agent string) {
	if pb.current < pb.total {
		pb.current++
	}
	pb.render(agent)
}

// AdvanceTo устанавливает счётчик (loopback может откатывать назад).
func (pb *ProgressBar) AdvanceTo(index int, agent string) {
	if index < 0 {
		index = 0
	}
	if index > pb.total {
		index = pb.total
	}
	pb.current = index
	pb.render(agent)
}

func (pb *ProgressBar) render(agent string) {
	if !IsTerminal() {
		return
	}
	fmt.Print("\033[s\033[K" + pb.BarText(agent) + "\033[u")
}

func (pb *ProgressBar) Done() {
	pb.current = pb.total
	if IsTerminal() {
		pb.render("")
		fmt.Println()
	}
}

func (pb *ProgressBar) BarText(agent string) string {
	if !IsTerminal() {
		return ""
	}
	ratio := 0.0
	if pb.total > 0 {
		ratio = float64(pb.current) / float64(pb.total)
	}
	filled := int(ratio * float64(pb.barWidth))
	if filled > pb.barWidth {
		filled = pb.barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", pb.barWidth-filled)
	return fmt.Sprintf("%s[ai-team]%s %s | %s%s (%d/%d) %s",
		ColorBold, ColorReset,
		Colorize(pb.feature, ColorCyan),
		Colorize(agent, ColorYellow), ColorReset,
		pb.current, pb.total,
		bar,
	)
}

// StatusWriter перерисовывает статус-бар после каждой записи вывода.
type StatusWriter struct {
	mu      sync.Mutex
	barText string
}

func NewStatusWriter() *StatusWriter {
	return &StatusWriter{}
}

func (w *StatusWriter) SetBar(text string) {
	w.mu.Lock()
	w.barText = text
	w.mu.Unlock()
}

func (w *StatusWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := fmt.Print(string(p))
	if err != nil {
		return n, err
	}
	if IsTerminal() && w.barText != "" {
		fmt.Print("\033[s\033[K" + w.barText + "\033[u")
	}
	return len(p), nil
}
