package pipeline

import (
	"context"

	"github.com/arturpanteleev/ai-team/pkg/prchecks"
)

// PRStageChecks keeps the network and git boundary injectable while exposing
// only checks bound to the PR identity supplied by the stage result.
type PRStageChecks interface {
	CheckIdentity(ctx context.Context, targetDir, reference, rule string, expected *prchecks.PullRequest) (status, reason string, identity prchecks.PullRequest, err error)
	Diff(ctx context.Context, targetDir, reference string) ([]byte, prchecks.PullRequest, error)
	DiffIdentity(ctx context.Context, targetDir, reference string, expected *prchecks.PullRequest) ([]byte, prchecks.PullRequest, error)
}
