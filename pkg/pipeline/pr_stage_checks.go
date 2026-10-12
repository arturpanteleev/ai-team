package pipeline

import (
	"context"

	"github.com/arturpanteleev/ai-team/pkg/prchecks"
	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
)

// PRStageChecks keeps the network and git boundary injectable while exposing
// only checks bound to the PR identity supplied by the stage result.
type PRStageChecks interface {
	Check(ctx context.Context, targetDir, reference, rule string) (status, reason string, err error)
	Diff(ctx context.Context, targetDir, reference string) ([]byte, prchecks.PullRequest, error)
}

func (rs *runState) projectRuleCheck(ctx context.Context, rule, reference string) (stagecheck.RuleResult, error) {
	status, reason, err := rs.p.prStageChecks.Check(ctx, rs.runCfg.TargetDir, reference, rule)
	if err != nil {
		return stagecheck.RuleResult{}, err
	}
	return stagecheck.RuleResult{Rule: rule, Status: status, Reason: reason}, nil
}
