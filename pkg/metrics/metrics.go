// Package metrics собирает per-run usage envelope: агрегаты по этапам,
// loopback-циклы и итоговый outcome прогона.
package metrics

import (
	"fmt"
	"io"
	"math"
	"text/tabwriter"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const (
	LegacySchemaVersion = 1
	SchemaVersion       = 2
)

// StageMetrics — агрегат по одному этапу за весь run (без superseded попыток).
type StageMetrics struct {
	Stage      string `json:"stage"`
	Attempts   int    `json:"attempts"`
	DurationMS int64  `json:"duration_ms"`
}

// Usage — aggregated attested usage из adapters-layer (P1-7). Поля принимаются
// ТОЛЬКО от адаптера с capability usage-reported (runtime.UsageSource);
// Attested=true фиксирует этот источник аттестации.
type Usage struct {
	Attested     bool    `json:"attested"`
	Unknown      bool    `json:"unknown,omitempty"`
	TokensInput  int64   `json:"tokens_input,omitempty"`
	TokensOutput int64   `json:"tokens_output,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

// UsageEnvelope — attempt-independent usage-сводка одного run.
type UsageEnvelope struct {
	SchemaVersion   int            `json:"schema_version"`
	RunID           string         `json:"run_id"`
	Feature         string         `json:"feature"`
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      time.Time      `json:"finished_at"`
	TotalDurationMS int64          `json:"total_duration_ms"`
	Stages          []StageMetrics `json:"stages"`
	LoopbackCycles  int            `json:"loopback_cycles"`
	// TokensUnknown — true, если отсутствует хотя бы одна обязательная аттестация.
	TokensUnknown bool `json:"tokens_unknown"`
	// UsageReported — true, когда хотя бы один адаптер аттестовал usage.
	UsageReported bool `json:"usage_reported,omitempty"`
	// TokensInput/TokensOutput/CostUSD — суммарные attested usage одного run.
	TokensInput  int64   `json:"tokens_input,omitempty"`
	TokensOutput int64   `json:"tokens_output,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Outcome      string  `json:"outcome"`
}

// HasCompleteTokenUsage reports whether this envelope uses the accounting
// semantics that prove all model attempts were represented. Schema v1 only
// recorded whether any adapter reported usage, so a non-unknown token total
// from that format may still be partial.
func (e UsageEnvelope) HasCompleteTokenUsage() bool {
	return e.SchemaVersion == SchemaVersion && e.UsageReported && !e.TokensUnknown
}

// Build агрегирует фиксированные результаты этапов в usage envelope.
// Superseded попытки исключаются; этапы упорядочены по первому появлению.
// usage — per-run attested usage (P1-7); if any invoked model attempt is
// missing complete usage, the token totals remain unknown.
func Build(runID, feature string, startedAt, finishedAt time.Time, results []workflow.StageResult, loopbackCycles int, outcome string, usage Usage) UsageEnvelope {
	index := make(map[string]int)
	stages := make([]StageMetrics, 0)
	for _, result := range results {
		if result.Superseded {
			continue
		}
		position, exists := index[result.Name]
		if !exists {
			position = len(stages)
			index[result.Name] = position
			stages = append(stages, StageMetrics{Stage: result.Name})
		}
		stages[position].Attempts++
		stages[position].DurationMS += result.Duration.Milliseconds()
	}
	var total int64
	if !startedAt.IsZero() && !finishedAt.IsZero() && !finishedAt.Before(startedAt) {
		total = finishedAt.Sub(startedAt).Milliseconds()
	}
	tokensInput, tokensOutput, costUSD := usage.TokensInput, usage.TokensOutput, usage.CostUSD
	if !usage.Attested || usage.Unknown {
		// An incomplete aggregate is not a zero-token total. Omit its counters
		// so downstream readers cannot mistake a partial sum for the full run.
		tokensInput, tokensOutput, costUSD = 0, 0, 0
	}
	envelope := UsageEnvelope{
		SchemaVersion:   SchemaVersion,
		RunID:           runID,
		Feature:         feature,
		StartedAt:       startedAt.UTC(),
		FinishedAt:      finishedAt.UTC(),
		TotalDurationMS: total,
		Stages:          stages,
		LoopbackCycles:  loopbackCycles,
		TokensUnknown:   !usage.Attested || usage.Unknown,
		UsageReported:   usage.Attested,
		TokensInput:     tokensInput,
		TokensOutput:    tokensOutput,
		CostUSD:         costUSD,
		Outcome:         outcome,
	}
	return envelope
}

// TotalAttempts возвращает суммарное число не-superseded попыток.
func (e UsageEnvelope) TotalAttempts() int {
	var total int
	for _, stage := range e.Stages {
		total += stage.Attempts
	}
	return total
}

// EstimateSubscriptionShare distributes an explicitly configured monthly
// subscription amount across recorded runs in the same UTC month, in
// proportion to their attested input+output tokens. It returns unavailable
// when any required usage input is unknown or the recorded token denominator
// is empty. The result is an estimate, never an API price.
func EstimateSubscriptionShare(monthlyAmount float64, run UsageEnvelope, recorded []UsageEnvelope) (float64, bool) {
	if monthlyAmount <= 0 || math.IsNaN(monthlyAmount) || math.IsInf(monthlyAmount, 0) || !run.HasCompleteTokenUsage() {
		return 0, false
	}
	month := run.FinishedAt.UTC().Format("2006-01")
	var totalTokens int64
	var selectedTokens int64
	foundRun := false
	for _, envelope := range recorded {
		if envelope.FinishedAt.IsZero() || envelope.FinishedAt.UTC().Format("2006-01") != month {
			continue
		}
		if !envelope.HasCompleteTokenUsage() {
			return 0, false
		}
		if envelope.TokensInput > math.MaxInt64-envelope.TokensOutput {
			return 0, false
		}
		tokens := envelope.TokensInput + envelope.TokensOutput
		if tokens < 0 || totalTokens > math.MaxInt64-tokens {
			return 0, false
		}
		totalTokens += tokens
		if envelope.RunID == run.RunID {
			selectedTokens = tokens
			foundRun = true
		}
	}
	if !foundRun || totalTokens == 0 {
		return 0, false
	}
	return monthlyAmount * float64(selectedTokens) / float64(totalTokens), true
}

// Format печатает envelope как читаемую таблицу (этап, попытки, время).
func (e UsageEnvelope) Format(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// вывод в консоль: сообщать об ошибке записи некуда — это и есть канал сообщений.
	_, _ = fmt.Fprintf(tw, "Этап\tПопытки\tВремя\n")
	for _, stage := range e.Stages {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%dms\n", stage.Stage, stage.Attempts, stage.DurationMS)
	}
	_, _ = fmt.Fprintf(tw, "ИТОГО\t%d\t%dms\n", e.TotalAttempts(), e.TotalDurationMS)
	return tw.Flush()
}
