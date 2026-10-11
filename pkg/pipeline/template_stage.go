package pipeline

import (
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
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

// stageExecutorForRun applies only an override bound to the approval visit
// that made this stage ready. A new approval/loop visit cannot inherit a
// previous executor choice for the same stage ID.
func (rs *runState) stageExecutorForRun(stageID string) string {
	activeApproval := rs.activeResolvedApproval(stageID)
	if activeApproval != nil && activeApproval.Kind == approval.KindInput && activeApproval.FromStage == stageID &&
		(activeApproval.ResolvedAction == "run_agent" || activeApproval.ResolvedAction == "refine_agent") {
		return "agent"
	}
	override, ok := rs.lifecycleState.ExecutorOverrides[stageID]
	if !ok {
		return rs.p.stageExecutor(stageID)
	}
	approvalID := rs.lifecycleState.ActiveApprovalID
	if approvalID == "" {
		approvalID = rs.lifecycleState.PendingApprovalID
	}
	if approvalID == "" || override.ApprovalID != approvalID {
		return rs.p.stageExecutor(stageID)
	}
	return override.Executor
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
