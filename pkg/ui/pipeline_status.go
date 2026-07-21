package ui

import "fmt"

type PipelineStatus struct {
	project string
	feature string
	bar     *ProgressBar
	writer  *StatusWriter
}

func NewPipelineStatus(project, feature string, total int) *PipelineStatus {
	bar := NewProgressBar(feature, total)
	writer := NewStatusWriter()
	return &PipelineStatus{
		project: project,
		feature: feature,
		bar:     bar,
		writer:  writer,
	}
}

// StatusWriter — writer для вывода агентов: перерисовывает статус-бар
// после каждой записи (persistent status bar).
func (ps *PipelineStatus) StatusWriter() *StatusWriter {
	return ps.writer
}

// StartAgent отмечает начало этапа index (1-based): выполнено index-1 из total.
func (ps *PipelineStatus) StartAgent(index int, agent string) {
	ps.bar.AdvanceTo(index-1, agent)
	text := ps.bar.BarText(agent)
	ps.writer.SetBar(text)
	if IsTerminal() {
		fmt.Print("\033[s\033[K" + text + "\033[u")
	}
}

// DoneAgent отмечает завершение этапа.
func (ps *PipelineStatus) DoneAgent(agent string) {
	ps.bar.Next(agent)
	ps.writer.SetBar(ps.bar.BarText(agent))
}

func (ps *PipelineStatus) Finalize() {
	ps.writer.SetBar("")
	ps.bar.Done()
}
