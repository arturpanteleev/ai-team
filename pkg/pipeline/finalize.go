package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// finalize.go — итоговый статус run, отчёт и summary.

func (rs *runState) finalize(runErr error) (workflow.RunOutcome, error) {
	endTime := time.Now()
	status := runStatusFor(runErr, rs.results)
	var approvalRequired *ApprovalRequiredError
	if errors.As(runErr, &approvalRequired) {
		if err := report.GenerateFinalReport(
			rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime,
			rs.task.ArtifactRoot, string(workflow.RunStopped),
		); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("waiting report: %w", err))
		}
		if err := rs.evidence.Append(evidence.Event{
			Type: "run_paused", Timestamp: endTime.UTC(),
			Data: map[string]any{
				"status":      "waiting_for_approval",
				"next_stage":  rs.lifecycleState.NextStage,
				"approval_id": rs.lifecycleState.PendingApprovalID,
			},
		}); err != nil {
			runErr = errors.Join(runErr, err)
		}
		rs.ps.Finalize()
		if rs.p.recorder != nil {
			rs.p.recorder.RunPaused(rs.runID, "waiting_for_approval", endTime.UTC())
		}
		return workflow.RunStopped, &RunError{Outcome: workflow.RunStopped, Err: runErr}
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		if err := report.GenerateFinalReport(
			rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime,
			rs.task.ArtifactRoot, status,
		); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("paused report: %w", err))
		}
		if err := rs.evidence.Append(evidence.Event{
			Type: "run_paused", Timestamp: endTime.UTC(),
			Data: map[string]any{"status": status, "next_stage": rs.lifecycleState.NextStage},
		}); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if err := rs.saveLifecycle(lifecycle.PhaseResumable, rs.lifecycleState.NextStage); err != nil {
			runErr = errors.Join(runErr, err)
		}
		rs.ps.Finalize()
		if rs.p.recorder != nil {
			rs.p.recorder.RunPaused(rs.runID, status, endTime.UTC())
		}
		return workflow.RunStopped, &RunError{Outcome: workflow.RunStopped, Err: runErr}
	}
	var finalizeErr error
	if err := report.GenerateFinalReport(rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime, rs.task.ArtifactRoot, status); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("final report: %w", err))
	} else if err := rs.evidence.PublishReportTree(rs.runCfg.Feature, filepath.Join(rs.reportsDir, rs.runCfg.Feature)); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("publish immutable report: %w", err))
	}
	if finalizeErr != nil {
		status = string(workflow.RunFailed)
		if err := report.GenerateFinalReport(rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime, rs.task.ArtifactRoot, status); err != nil {
			finalizeErr = errors.Join(finalizeErr, fmt.Errorf("failed-status report: %w", err))
		}
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "run_finished", Timestamp: endTime.UTC(),
		Data: map[string]any{"status": status, "stage_attempts": rs.attemptOrdinal},
	}); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("запись run_finished: %w", err))
		status = string(workflow.RunFailed)
		_ = report.GenerateFinalReport(rs.reportsDir, rs.runCfg.Feature, rs.results, rs.startTime, endTime, rs.task.ArtifactRoot, status)
	}
	if err := rs.writeUsageEnvelope(endTime, status); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("usage envelope: %w", err))
	}
	if err := rs.writeAttestation(endTime, status); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("attestation: %w", err))
	}
	rs.ps.Finalize()
	rs.printSummary()
	if rs.p.recorder != nil {
		rs.p.recorder.RunFinished(rs.runID, status, endTime.UTC())
	}
	terminal := rs.lifecycleState
	terminal.Phase, terminal.NextStage, terminal.PendingApprovalID, terminal.AttemptOrdinal =
		lifecycle.PhaseTerminal, "", "", rs.attemptOrdinal
	if err := rs.lifecycleStore.Save(rs.lifecycleState, terminal); err != nil {
		finalizeErr = errors.Join(finalizeErr, fmt.Errorf("terminal lifecycle state: %w", err))
	} else {
		rs.lifecycleState = terminal
	}
	combinedErr := errors.Join(runErr, finalizeErr)
	outcome := workflow.RunOutcome(status)
	if combinedErr != nil {
		return outcome, &RunError{Outcome: outcome, Err: combinedErr}
	}
	return outcome, nil
}

// writeUsageEnvelope публикует attempt-independent usage-сводку run в
// {RunDir}/usage.json — рядом с run.json, вне attempts/.
func (rs *runState) writeUsageEnvelope(finishedAt time.Time, status string) error {
	envelope := metrics.Build(rs.runID, rs.runCfg.Feature, rs.startTime, finishedAt,
		rs.results, rs.loopbackCycles, status)
	return writeControllerJSON(filepath.Join(rs.evidence.RunDir(), "usage.json"), envelope)
}

// writeAttestation публикует in-toto compatible attestation statement v1
// (V0-3) в {RunDir}/attestation.json после terminal-завершения run'а.
// subject = candidate workspace identity; approvals берутся из approval store.
func (rs *runState) writeAttestation(finishedAt time.Time, status string) error {
	approvals, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return fmt.Errorf("attestation approvals: %w", err)
	}
	var subjects []attest.Subject
	if rs.candidate != nil {
		identity, identityErr := rs.candidate.Identity()
		if identityErr == nil && identity.WorkspaceSHA256 != "" {
			subjects = append(subjects, attest.Subject{
				Name:   "candidate",
				Digest: map[string]string{"sha256": identity.WorkspaceSHA256},
			})
		}
		// Вне Git или при нерезолвимой identity subject честно пустой: не
		// утверждаем то, что не можем вычислить deterministically.
	}
	statement, err := attest.Build(attest.Options{
		RunDir: rs.evidence.RunDir(), RunID: rs.runID,
		FinishedAt: finishedAt, Outcome: status,
		CandidateSubject: subjects, Approvals: approvals,
	})
	if err != nil {
		return err
	}
	return writeControllerJSON(filepath.Join(rs.evidence.RunDir(), "attestation.json"), statement)
}

// runStatus — финальный статус запуска для store.
func runStatusFor(err error, results []notifier.StageResult) string {
	states := make([]workflow.AttemptState, 0, len(results))
	for _, result := range results {
		state := result.State
		if state.Execution == "" {
			execution := workflow.ExecutionSucceeded
			if result.Err != nil {
				execution = workflow.ExecutionInfraFailed
			}
			state, _ = workflow.DeriveAttempt(workflow.AttemptFacts{
				Execution: execution, Verdict: result.Verdict,
				Blocked: result.Status == notifier.StatusBlocked, Superseded: result.Superseded,
			})
		}
		states = append(states, state)
	}
	return string(workflow.DeriveRun(runSignal(err), states))
}

func runSignal(err error) workflow.RunSignal {
	if err == nil {
		return workflow.SignalCompleted
	}
	var runErr *RunError
	if errors.As(err, &runErr) {
		return workflow.RunSignal(runErr.Outcome)
	}
	switch {
	case isBlockedErr(err):
		return workflow.SignalBlocked
	case isUserStopped(err):
		return workflow.SignalStopped
	case errors.Is(err, context.Canceled):
		return workflow.SignalCanceled
	default:
		return workflow.SignalFailed
	}
}

func isBlockedErr(err error) bool {
	var be *BlockedError
	return errors.As(err, &be)
}

func isUserStopped(err error) bool {
	return errors.Is(err, ErrUserStopped)
}

func (rs *runState) printSummary() {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	title := fmt.Sprintf("=== ИТОГ ПАЙПЛАЙНА: %s ===", rs.runCfg.Feature)
	fmt.Fprintf(w, "%s\n", ui.Colorize(title, ui.ColorBold))

	fmt.Fprintf(w, "%s\t%s\t\t%s\n",
		ui.Colorize("Этап", ui.ColorCyan),
		ui.Colorize("Статус", ui.ColorCyan),
		ui.Colorize("Результат", ui.ColorCyan),
	)
	fmt.Fprintf(w, "───\t───\t\t───\n")

	for _, r := range rs.results {
		var status string
		switch r.Status {
		case notifier.StatusBlocked:
			status = ui.ColoredStatusBlocked()
		case notifier.StatusRejected, notifier.StatusFailed, notifier.StatusCanceled:
			status = ui.ColoredStatus(false)
		case notifier.StatusWarning:
			status = ui.Colorize("!", ui.ColorYellow)
		case notifier.StatusSkipped:
			status = ui.Colorize("⏸", ui.ColorCyan)
		case notifier.StatusInvalidated:
			status = ui.Colorize("↺", ui.ColorCyan)
		default:
			status = ui.ColoredStatus(r.Status == notifier.StatusPassed && r.Err == nil)
		}

		var resultStr string
		switch {
		case r.Err != nil:
			resultStr = ui.Colorize(shortenError(r.Err), ui.ColorRed)
		case r.Status == notifier.StatusBlocked:
			resultStr = ui.Colorize("BLOCKED: "+ui.Truncate(r.Blocker, 60), ui.ColorYellow)
		default:
			var labels []string
			for _, out := range r.Outputs {
				labels = append(labels, ui.Colorize(out.Name, ui.ColorGreen))
			}
			if len(labels) == 0 {
				labels = append(labels, "—")
			}
			resultStr = strings.Join(labels, ", ")
			if r.Verdict != "" {
				resultStr += " (" + string(r.Verdict) + ")"
			}
		}

		fmt.Fprintf(w, "%s\t%s\t\t%s\n",
			ui.Colorize(r.Name, ui.ColorYellow),
			status,
			resultStr,
		)
	}

	fmt.Fprintf(w, "\n%s  %s\n",
		ui.Colorize("📄", ui.ColorBold),
		ui.Colorize("Report: "+filepath.Join(rs.reportsDir, rs.runCfg.Feature, "index.html"), ui.ColorCyan),
	)

	w.Flush()
	fmt.Println()
}

func shortenError(err error) string {
	return ui.Truncate(err.Error(), 80)
}
