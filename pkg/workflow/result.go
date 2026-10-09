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

// Режимы изменения файла относительно baseline этапа (V0-1).
const (
	MutationAdded    = "added"
	MutationModified = "modified"
	MutationRemoved  = "removed"
)

// MutationChange — контроллер-атрибутированная мутация одного
// repository-relative пути: режим изменения и класс пути.
type MutationChange struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Class string `json:"class"`
}

// AttemptUsage contains attested model token and cost totals for one stage
// invocation. A nil value means the runtime did not report usage; an attested
// zero is represented by a non-nil value with zero counters.
type AttemptUsage struct {
	Attested     bool    `json:"attested"`
	TokensInput  int64   `json:"tokens_input,omitempty"`
	TokensOutput int64   `json:"tokens_output,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
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
	MutationChanges  []MutationChange
	Delivery         *delivery.Result
	Usage            *AttemptUsage
	StageIndex       int
	TotalStages      int
	Inputs           []Artifact
	Outputs          []Artifact
	Summary          string
}
