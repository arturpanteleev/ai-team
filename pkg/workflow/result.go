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
//
// Model, CacheReadTokens, CacheWriteTokens and CacheReadIncludedInInput feed
// the per-model tariff cost (B-66). Model is the configured model of the
// attempt (empty when the harness default was used and is therefore unknown).
// Cache reads are part of TokensInput only when CacheReadIncludedInInput is
// set; cache writes are never part of TokensInput. Manifests written before
// B-66 have no Model and are priced as unknown.
type AttemptUsage struct {
	Attested                 bool    `json:"attested"`
	TokensInput              int64   `json:"tokens_input,omitempty"`
	TokensOutput             int64   `json:"tokens_output,omitempty"`
	CostUSD                  float64 `json:"cost_usd,omitempty"`
	Model                    string  `json:"model,omitempty"`
	CacheReadTokens          int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         int64   `json:"cache_write_tokens,omitempty"`
	CacheReadIncludedInInput bool    `json:"cache_read_included_in_input,omitempty"`
}

// StageResult is the domain record of one immutable stage attempt.
// CheckOverridden marks an attempt whose failed stage check (B-40) was
// bypassed by an explicit, reasoned human decision (B-41); delivery and
// subsequent stages read it as "passed with override".
type StageResult struct {
	RunID                     string
	AttemptID                 string
	Name                      string
	Executor                  string
	ActorID                   string
	ActorRole                 string
	HumanInputApprovalID      string
	HumanEditOfAgentAttemptID string
	Status                    string
	Err                       error
	Blocker                   string
	Verdict                   verdict.Verdict
	Duration                  time.Duration
	StartedAt                 time.Time
	FinishedAt                time.Time
	Superseded                bool
	ValidationFailed          bool
	ControlStopped            bool
	CheckOverridden           bool
	State                     AttemptState
	Checks                    []checks.Result
	Mutations                 []string
	MutationChanges           []MutationChange
	Delivery                  *delivery.Result
	Usage                     *AttemptUsage
	StageIndex                int
	TotalStages               int
	Inputs                    []Artifact
	Outputs                   []Artifact
	Summary                   string
}
