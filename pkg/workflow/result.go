package workflow

import (
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

const (
	StatusPassed      = "passed"
	StatusFailed      = "failed"
	StatusBlocked     = "blocked"
	StatusRejected    = "rejected"
	StatusCanceled    = "canceled"
	StatusWarning     = "warning"
	StatusSkipped     = "skipped"
	StatusInvalidated = "invalidated"
)

// Artifact is a controller-observed artifact, independent from any runtime.
type Artifact struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
	Source  string
}

// StageResult is the domain record of one immutable stage attempt.
type StageResult struct {
	RunID            string
	AttemptID        string
	Name             string
	Status           string
	Err              error
	Blocker          string
	Verdict          verdict.Verdict
	Duration         time.Duration
	StartedAt        time.Time
	FinishedAt       time.Time
	Superseded       bool
	ValidationFailed bool
	ControlStopped   bool
	State            AttemptState
	Checks           []checks.Result
	Mutations        []string
	Delivery         *delivery.Result
	StageIndex       int
	TotalStages      int
	Inputs           []Artifact
	Outputs          []Artifact
	Summary          string
}
