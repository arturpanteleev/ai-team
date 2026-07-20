package notifier

import (
	"context"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const (
	StatusPassed      = workflow.StatusPassed
	StatusFailed      = workflow.StatusFailed
	StatusBlocked     = workflow.StatusBlocked
	StatusRejected    = workflow.StatusRejected
	StatusCanceled    = workflow.StatusCanceled
	StatusWarning     = workflow.StatusWarning
	StatusSkipped     = workflow.StatusSkipped
	StatusInvalidated = workflow.StatusInvalidated
)

// StageResult remains an alias for source compatibility; ownership is workflow.
type StageResult = workflow.StageResult

type Notifier interface {
	Notify(ctx context.Context, stage StageResult) error
}
