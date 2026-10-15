package pipeline

import (
	"context"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/llmgate"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
)

// gate_llm.go — the LLM gate (agent_review) in the common gate chain.
//
// The contract lives in pkg/llmgate: a reviewer agent is called with fixed
// inputs (its declared inputs plus the outputs of the checked stage) and must
// answer by the configured verdict schema. An answer outside the schema or a
// refusal of the runtime is a technical error (execution=error), retried up to
// max_attempts and never turned into pass or fail. mode grill is another way of
// running the gate and does not pass through here.

// legacyNoVerdictNote is the note of a report without a marker verdict, kept
// byte-for-byte for the schema every pre-S05 template used.
const legacyNoVerdictNote = "в отчёте агента-проверяющего не найден вердикт PASSED|FAILED"

// runAgentGate runs a plain (non-grill) LLM gate and returns its report in the
// form the stage-check flow consumes.
func (rs *runState) runAgentGate(ctx context.Context, stage config.TemplateStage, result notifier.StageResult, check config.TemplateCheck) stagecheck.Report {
	schema := llmgate.Normalize(llmgate.Schema(check.VerdictSchema))
	res := llmgate.Run(ctx, llmgate.Spec{Schema: schema, MaxAttempts: check.MaxAttempts},
		func(ctx context.Context, attempt int) (string, error) {
			// The attempt number separates the runtime attempt IDs of the
			// repeated calls of one gate attempt.
			return rs.runCheckerAgentWithInputs(ctx, stage, result, result.Outputs, attempt-1)
		})
	return agentGateReport(stage.ID, check.Agent, schema, res)
}

// agentGateReport maps the contract result to the stage-check report.
func agentGateReport(stageID, source string, schema llmgate.Schema, res llmgate.Result) stagecheck.Report {
	report := stagecheck.Report{Stage: stageID, Kind: stagecheck.KindAgent}
	switch res.Status {
	case llmgate.StatusPass:
		report.Status = stagecheck.StatusPassed
		report.Verdict = llmgate.MarkerStatusOf(true)
		report.Notes = res.Reason
	case llmgate.StatusFail:
		report.Status = stagecheck.StatusFailed
		report.Verdict = llmgate.MarkerStatusOf(false)
		if source != "" {
			report.Notes = "агент-проверяющий " + source + " вынес вердикт " + report.Verdict
		}
		if res.Reason != "" {
			report.Notes = strings.TrimSpace(report.Notes + ": " + res.Reason)
		}
	default:
		// error and canceled alike: no verdict was obtained.
		report.Status = stagecheck.StatusError
		report.Notes = agentGateErrorNotes(schema, res)
	}
	return report
}

func agentGateErrorNotes(schema llmgate.Schema, res llmgate.Result) string {
	if len(res.Trace) == 1 {
		item := res.Trace[0]
		switch {
		case item.Class == llmgate.ClassRuntime:
			return item.Detail
		case item.Class == llmgate.ClassSchema && schema == llmgate.SchemaMarker:
			return legacyNoVerdictNote
		}
	}
	return res.Reason
}
