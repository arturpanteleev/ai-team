// Package metrics собирает per-run usage envelope: агрегаты по этапам,
// loopback-циклы и итоговый outcome прогона.
package metrics

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const SchemaVersion = 1

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
	// TokensUnknown — true, пока харнесс/адаптер не отдаёт attested usage.
	TokensUnknown bool `json:"tokens_unknown"`
	// UsageReported — true, когда хотя бы один адаптер аттестовал usage.
	UsageReported bool `json:"usage_reported,omitempty"`
	// TokensInput/TokensOutput/CostUSD — суммарные attested usage одного run.
	TokensInput  int64   `json:"tokens_input,omitempty"`
	TokensOutput int64   `json:"tokens_output,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Outcome      string  `json:"outcome"`
}

// Build агрегирует фиксированные результаты этапов в usage envelope.
// Superseded попытки исключаются; этапы упорядочены по первому появлению.
// usage — суммарный attested usage (P1-7); если Attested=false — usage
// остаётся unknown.
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
	envelope := UsageEnvelope{
		SchemaVersion:   SchemaVersion,
		RunID:           runID,
		Feature:         feature,
		StartedAt:       startedAt.UTC(),
		FinishedAt:      finishedAt.UTC(),
		TotalDurationMS: total,
		Stages:          stages,
		LoopbackCycles:  loopbackCycles,
		TokensUnknown:   !usage.Attested,
		UsageReported:   usage.Attested,
		TokensInput:     usage.TokensInput,
		TokensOutput:    usage.TokensOutput,
		CostUSD:         usage.CostUSD,
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

// Format печатает envelope как читаемую таблицу (этап, попытки, время).
func (e UsageEnvelope) Format(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Этап\tПопытки\tВремя\n")
	for _, stage := range e.Stages {
		fmt.Fprintf(tw, "%s\t%d\t%dms\n", stage.Stage, stage.Attempts, stage.DurationMS)
	}
	fmt.Fprintf(tw, "ИТОГО\t%d\t%dms\n", e.TotalAttempts(), e.TotalDurationMS)
	return tw.Flush()
}
