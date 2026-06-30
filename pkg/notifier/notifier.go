package notifier

import (
	"context"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

type StageResult struct {
	Name         string
	Status       string
	Err          error
	Duration     time.Duration
	StageIndex   int
	TotalStages  int
	Inputs       []runtime.Artifact
	Outputs      []runtime.Artifact
}

type Notifier interface {
	Notify(ctx context.Context, stage StageResult) error
}
