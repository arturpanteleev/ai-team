package ui

import "fmt"

type PipelineStatus struct {
	project string
	feature string
	bar     *ProgressBar
}

func NewPipelineStatus(project, feature string, total int) *PipelineStatus {
	return &PipelineStatus{
		project: project,
		feature: feature,
		bar:     NewProgressBar(feature, total),
	}
}

func (ps *PipelineStatus) StartAgent(index int, agent string) {
	ps.bar.Clear()
	ps.bar.AdvanceTo(index, agent)
	if IsTerminal() {
		fmt.Println()
	}
}

func (ps *PipelineStatus) DoneAgent(agent string) {
	ps.bar.Next(agent)
}

func (ps *PipelineStatus) Finalize() {
	ps.bar.Done()
}
