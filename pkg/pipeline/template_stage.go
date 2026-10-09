package pipeline

import (
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/config"
)

func (p *Pipeline) templateStage(stageID string) (config.TemplateStage, bool) {
	for _, stage := range p.cfg.Stages {
		if stage.ID == stageID {
			return stage, true
		}
	}
	return config.TemplateStage{}, false
}

func (p *Pipeline) stageExecutor(stageID string) string {
	if stage, ok := p.templateStage(stageID); ok {
		return stage.Executor
	}
	return "agent"
}

func (p *Pipeline) registryAgentName(stageID string) string {
	if stage, ok := p.templateStage(stageID); ok && stage.Agent != "" {
		return stage.Agent
	}
	return stageID
}

// loadStageDefinition resolves a stable template stage ID to its optional
// registry contract. Human stages may intentionally have no agent definition.
func (p *Pipeline) loadStageDefinition(stageID string) (*agent.Agent, error) {
	if stage, ok := p.templateStage(stageID); ok && stage.Agent == "" {
		if stage.Executor == "human" {
			return nil, nil
		}
		return nil, fmt.Errorf("stage %s requires a registry agent", stageID)
	}
	return p.reg.Load(p.registryAgentName(stageID))
}
