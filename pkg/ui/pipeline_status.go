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

func (ps *PipelineStatus) StatusWriter() *StatusWriter {
	return ps.writer
}

func (ps *PipelineStatus) StartAgent(index int, agent string) {
	text := ps.bar.BarText(agent)
	ps.writer.SetBar(text)
	if IsTerminal() {
		fmt.Print("\033[s\033[K" + text + "\033[u")
	}
}

func (ps *PipelineStatus) DoneAgent(agent string) {
	text := ps.bar.BarText(agent)
	ps.writer.SetBar(text)
}

func (ps *PipelineStatus) Advance(index int, agent string) {
	ps.bar.AdvanceTo(index, agent)
	text := ps.bar.BarText(agent)
	ps.writer.SetBar(text)
}

func (ps *PipelineStatus) Finalize() {
	ps.writer.SetBar("")
	ps.bar.Done()
}
