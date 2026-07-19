package notifier

import (
	"context"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/runtime"
)

const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusBlocked = "blocked"
)

type StageResult struct {
	Name         string
	Status       string
	Err          error
	Blocker      string
	Duration     time.Duration
	StageIndex   int
	TotalStages  int
	Inputs       []runtime.Artifact
	Outputs      []runtime.Artifact
}

type Notifier interface {
	Notify(ctx context.Context, stage StageResult) error
}
