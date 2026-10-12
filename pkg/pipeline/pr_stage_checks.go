package pipeline

import (
	"context"
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/prchecks"
)

// PRStageChecks keeps the network and git boundary injectable while exposing
// only checks bound to the PR identity supplied by the stage result.
// Every PR is bound to the task repository captured at run start.
type PRStageChecks interface {
	TaskRepository(ctx context.Context, targetDir string) (string, error)
	CheckIdentity(ctx context.Context, targetDir, repository, reference, rule string, expected *prchecks.PullRequest) (status, reason string, identity prchecks.PullRequest, err error)
	Diff(ctx context.Context, targetDir, repository, reference string) ([]byte, prchecks.PullRequest, error)
	DiffIdentity(ctx context.Context, targetDir, repository, reference string, expected *prchecks.PullRequest) ([]byte, prchecks.PullRequest, error)
}

// prTaskRepository returns the captured repository or why it is unavailable.
func (rs *runState) prTaskRepository() (string, error) {
	if rs.taskRepository == "" {
		return "", fmt.Errorf("PR checks require the task's GitHub origin captured at run start: %s", rs.taskRepositoryErr)
	}
	return rs.taskRepository, nil
}
