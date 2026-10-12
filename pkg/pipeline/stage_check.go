package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/stagecheck"
	"github.com/arturpanteleev/ai-team/pkg/ui"
)

// stage_check.go — проверки результата этапа (B-40, §7). Вызываются из
// executeGraph после завершения попытки и до подтверждения перехода.

// stageCheckResetMarker скрывает agent-грилл (B-42) от B-40-механики: пока
// любой request со mode grill обрабатывается гриллом, а не обычной проверкой.
const grillMode = "grill"

// runStageChecks выполняет декларативную проверку результата только что
// завершённой попытки. Возвращает ApprovalRequiredError, если проверка не
// пройдена или не смогла выполниться: переход блокируется до решения человека.
func (rs *runState) runStageChecks(ctx context.Context, stageID string, result notifier.StageResult) error {
	stage, ok := rs.p.templateStage(stageID)
	if !ok || stage.Check == nil || stage.Check.Kind == "" {
		return nil
	}
	check := stage.Check
	if check.Kind == "agent" && check.Mode == grillMode {
		return nil // grill обрабатывается отдельной механикой (B-42).
	}

	// Replay-safe: a crash can leave an attempt durably finished before its
	// check ran, or after a failed check but before the human decision was
	// recorded. Decide from the durable check event (not the attempt replay
	// flag): no completed check means the check must run again, a passed check
	// is already satisfied, and a failed/erroring check must still block the
	// transition instead of silently advancing.
	if applied, found, err := rs.replayedStageCheck(stageID, result.AttemptID); err != nil {
		return err
	} else if found {
		if applied.Status == stagecheck.StatusPassed {
			return nil
		}
		return rs.blockOnDurableCheckFailure(stage, result, applied)
	}

	startedAt := time.Now().UTC()
	if err := rs.evidence.Append(evidence.Event{
		Type: "check_started", Stage: stageID, AttemptID: result.AttemptID, Timestamp: startedAt,
		Data: map[string]any{"check_kind": check.Kind},
	}); err != nil {
		return fmt.Errorf("запись check_started для %s: %w", stageID, err)
	}

	report := rs.evaluateStageCheck(ctx, stage, result)
	if report.Stage == "" {
		report.Stage = stageID
	}
	if report.Kind == "" {
		report.Kind = check.Kind
	}
	reportPath, reportSHA, err := rs.writeCheckReport(stageID, report)
	if err != nil {
		return fmt.Errorf("сохранение отчёта проверки %s: %w", stageID, err)
	}

	eventType := "check_passed"
	isFailure := report.Status != stagecheck.StatusPassed
	if isFailure {
		eventType = "check_failed"
	}
	if report.Status == stagecheck.StatusError {
		eventType = "check_error"
	}
	eventData := map[string]any{
		"check_kind":    report.Kind,
		"report_path":   reportPath,
		"report_sha256": reportSHA,
		"status":        report.Status,
	}
	if report.Status == stagecheck.StatusError {
		eventData["error"] = strings.TrimSpace(report.Notes)
	}
	checkEvent := evidence.Event{
		Type: eventType, Stage: stageID, AttemptID: result.AttemptID, Timestamp: time.Now().UTC(), Data: eventData,
	}
	if err := rs.evidence.Append(checkEvent); err != nil {
		return fmt.Errorf("запись %s для %s: %w", eventType, stageID, err)
	}
	if eventType == "check_error" {
		rs.recordTypedEvent(checkEvent)
	}
	logging.Printf("%s проверка %s: %s\n",
		ui.Colorize(stageStatusGlyph(report.Status), stageStatusColor(report.Status)),
		ui.Colorize(stageID, ui.ColorBold),
		report.Status)

	if !isFailure {
		return nil
	}
	return rs.requestCheckDecision(stage, result, report, reportPath, reportSHA)
}

// durableStageCheck is the replay view of a completed stage check, rebuilt
// from the run's hash-chained evidence so a crash cannot lose the outcome.
type durableStageCheck struct {
	Kind       string
	Status     string
	ReportPath string
	ReportSHA  string
}

// replayedStageCheck returns the completed check already recorded for the
// attempt, if any. "Completed" means a check_passed/check_failed/check_error
// event exists; a lone check_started (crash mid-check) is deliberately treated
// as not-run so the check is retried. A check_overridden event takes
// precedence over the failed check it bypasses: the human decision satisfies
// the check requirement.
func (rs *runState) replayedStageCheck(stageID, attemptID string) (durableStageCheck, bool, error) {
	if attemptID == "" {
		return durableStageCheck{}, false, nil
	}
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return durableStageCheck{}, false, fmt.Errorf("чтение событий проверки %s: %w", stageID, err)
	}
	if !found {
		return durableStageCheck{}, false, nil
	}
	overridden := false
	for _, event := range events {
		if event.AttemptID != attemptID || event.Stage != stageID {
			continue
		}
		if event.Type == "check_overridden" {
			overridden = true
			continue
		}
		if overridden {
			continue
		}
		switch event.Type {
		case "check_passed", "check_failed", "check_error":
			return durableStageCheck{
				Kind:       eventDataString(event, "check_kind"),
				Status:     eventDataString(event, "status"),
				ReportPath: eventDataString(event, "report_path"),
				ReportSHA:  eventDataString(event, "report_sha256"),
			}, true, nil
		}
	}
	if overridden {
		return durableStageCheck{Status: stagecheck.StatusPassed}, true, nil
	}
	return durableStageCheck{}, false, nil
}

// blockOnDurableCheckFailure re-establishes the human decision that a prior,
// durably failed check requires. It reuses a still-pending check_decision
// approval, and otherwise raises a fresh one from the recorded report binding.
// A resolved override for the same attempt satisfies the check requirement;
// any other resolved action is not treated to advance: the failed attempt must
// not transition.
func (rs *runState) blockOnDurableCheckFailure(stage config.TemplateStage, result notifier.StageResult, applied durableStageCheck) error {
	existing, err := rs.checkDecisionForAttempt(stage.ID, result.AttemptID)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Status == approval.StatusPending {
			if err := rs.saveWaiting(stage.ID, existing.ID); err != nil {
				return err
			}
			return &ApprovalRequiredError{
				Checkpoint: fmt.Sprintf("проверка этапа %s (%s)", stage.ID, applied.Status),
				RunID:      rs.runID, ApprovalID: existing.ID, SubjectHash: existing.SubjectHash,
			}
		}
		if existing.ResolvedAction == "override" {
			return nil
		}
		return fmt.Errorf("проверка этапа %s (%s) уже разрешена действием %q; требуется повторный прогон этапа",
			stage.ID, applied.Status, existing.ResolvedAction)
	}
	report := stagecheck.Report{Stage: stage.ID, Kind: applied.Kind, Status: applied.Status}
	if report.Kind == "" {
		report.Kind = stage.Check.Kind
	}
	return rs.requestCheckDecision(stage, result, report, applied.ReportPath, applied.ReportSHA)
}

// applyCheckOverride фиксирует обход проверки этапа решением человека и
// возвращает результат попытки с флагом CheckOverridden. Этап не
// перезапускается: переход продолжается как «пройденный с обходом».
func (rs *runState) applyCheckOverride(stageID string) (notifier.StageResult, error) {
	if rs.resumedApproval == nil {
		return notifier.StageResult{}, errors.New("обход проверки: отсутствует решённый check_decision")
	}
	decision := lastApprovalDecision(rs.resumedApproval)
	reason := strings.TrimSpace(decision.Comment)
	if reason == "" {
		return notifier.StageResult{}, errors.New("обход проверки требует причину")
	}
	var result *notifier.StageResult
	for index := len(rs.results) - 1; index >= 0; index-- {
		if rs.results[index].Name == stageID && rs.results[index].AttemptID == rs.resumedApproval.AttemptID {
			result = &rs.results[index]
			break
		}
	}
	if result == nil {
		return notifier.StageResult{}, fmt.Errorf("обход проверки: попытка %s этапа %s не найдена",
			rs.resumedApproval.AttemptID, stageID)
	}
	applied, _, err := rs.replayedStageCheck(stageID, result.AttemptID)
	if err != nil {
		return notifier.StageResult{}, err
	}
	checkKind := applied.Kind
	if checkKind == "" {
		stage, ok := rs.p.templateStage(stageID)
		if !ok || stage.Check == nil {
			return notifier.StageResult{}, fmt.Errorf("обход проверки: этап %s не имеет проверки", stageID)
		}
		checkKind = stage.Check.Kind
	}
	eventData := map[string]any{
		"severity":    "warning",
		"warning":     true,
		"reason":      reason,
		"actor_id":    decision.ActorID,
		"actor_role":  decision.ActorRole,
		"approval_id": rs.resumedApproval.ID,
		"check_kind":  checkKind,
	}
	if applied.Status != "" {
		eventData["check_status"] = applied.Status
	}
	if applied.ReportPath != "" {
		eventData["report_path"] = applied.ReportPath
	}
	if applied.ReportSHA != "" {
		eventData["report_sha256"] = applied.ReportSHA
	}
	overriddenEvent := evidence.Event{
		Type: "check_overridden", Stage: stageID, AttemptID: result.AttemptID,
		Timestamp: time.Now().UTC(), Data: eventData,
	}
	if err := rs.evidence.Append(overriddenEvent); err != nil {
		return notifier.StageResult{}, fmt.Errorf("запись check_overridden для %s: %w", stageID, err)
	}
	rs.recordTypedEvent(overriddenEvent)
	result.CheckOverridden = true
	return *result, nil
}

func (rs *runState) checkDecisionForAttempt(stageID, attemptID string) (*approval.PendingApproval, error) {
	values, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return nil, fmt.Errorf("list check decisions для %s: %w", stageID, err)
	}
	for index := range values {
		value := values[index]
		if value.Kind == approval.KindCheckDecision && value.Trigger == approval.StageCheckTrigger &&
			value.FromStage == stageID && value.AttemptID == attemptID {
			return &value, nil
		}
	}
	return nil, nil
}

func eventDataString(event evidence.Event, key string) string {
	value, _ := event.Data[key].(string)
	return value
}

func stageStatusGlyph(status string) string {
	switch status {
	case stagecheck.StatusFailed:
		return "✗"
	case stagecheck.StatusError:
		return "⚠"
	default:
		return "✓"
	}
}

func stageStatusColor(status string) string {
	switch status {
	case stagecheck.StatusFailed:
		return ui.ColorRed
	case stagecheck.StatusError:
		return ui.ColorYellow
	default:
		return ui.ColorGreen
	}
}

// evaluateStageCheck строит отчёт проверки. Ошибки исполнения не возвращаются
// наружу: они фиксируются как StatusError внутри отчёта и превращаются в
// check_error событие.
func (rs *runState) evaluateStageCheck(ctx context.Context, stage config.TemplateStage, result notifier.StageResult) stagecheck.Report {
	switch stage.Check.Kind {
	case "hard":
		return rs.runHardChecks(ctx, stage, result)
	case "agent":
		return rs.runAgentCheck(ctx, stage, result)
	default:
		return stagecheck.Report{Kind: stage.Check.Kind, Status: "error", Notes: "вид проверки не реализован: " + stage.Check.Kind}
	}
}

func (rs *runState) runHardChecks(ctx context.Context, stage config.TemplateStage, result notifier.StageResult) stagecheck.Report {
	content, reference := rs.stageCheckInputs(stage, result)
	input := stagecheck.HardInput{
		Stage: stage.ID, ResultType: stage.Result,
		Content: content, Reference: reference,
		RequiredSections: stage.RequiredSections,
		Rules:            stage.Check.Rules,
	}
	for _, rule := range stage.Check.Rules {
		if rule != "pr_exists" && rule != "pr_open" && rule != "pr_base_branch" {
			continue
		}
		base, baseErr := delivery.DetectBaseBranch(ctx, rs.runCfg.TargetDir)
		input.ExpectedBaseBranch = base
		input.PRCheck = rs.prCheckClosure(baseErr, base)
		break
	}
	return stagecheck.RunHard(ctx, input)
}

func (rs *runState) prCheckClosure(baseErr error, base string) stagecheck.PRCheckFunc {
	return func(ctx context.Context, reference, _ string) ([]stagecheck.RuleResult, error) {
		if baseErr != nil {
			return nil, fmt.Errorf("pr_base_branch: %w", baseErr)
		}
		report, err := delivery.CheckPullRequest(ctx, delivery.ExecRunner{}, rs.runCfg.TargetDir, reference, base)
		if err != nil {
			return nil, err
		}
		results := make([]stagecheck.RuleResult, 0, len(report.Rules))
		for _, rule := range report.Rules {
			results = append(results, stagecheck.RuleResult{Rule: rule.Rule, Status: rule.Status, Reason: rule.Reason})
		}
		return results, nil
	}
}

// stageCheckInputs извлекает из выходов попытки содержимое результата: md-файлы
// конкатенируются в content, а первая текстовая ссылка становится reference.
func (rs *runState) stageCheckInputs(stage config.TemplateStage, result notifier.StageResult) (content, reference string) {
	var builder strings.Builder
	for _, output := range result.Outputs {
		if output.Path == "" {
			continue
		}
		data, err := safeio.ReadRegularFile(output.Path, maxArtifactFileBytes)
		if err != nil {
			continue
		}
		lowerName := strings.ToLower(output.Name)
		lowerPath := strings.ToLower(filepath.ToSlash(output.Path))
		if strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerName, ".md") || strings.HasSuffix(lowerName, ".markdown") {
			builder.Write(data)
			builder.WriteString("\n\n")
			continue
		}
		if reference == "" && strings.TrimSpace(string(data)) != "" && !looksBinary(data) {
			reference = strings.TrimSpace(string(data))
		}
	}
	return builder.String(), reference
}

func looksBinary(data []byte) bool {
	const sample = 1024
	limit := len(data)
	if limit > sample {
		limit = sample
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

func (rs *runState) runAgentCheck(ctx context.Context, stage config.TemplateStage, result notifier.StageResult) stagecheck.Report {
	agentName := stage.Check.Agent
	output, err := rs.runCheckerAgent(ctx, stage, result)
	if err != nil {
		return stagecheck.Report{Kind: "agent", Status: "error", Notes: err.Error()}
	}
	return stagecheck.RunAgent(stage.ID, agentName, output)
}

// runCheckerAgent запускает агента-проверяющего на результате этапа и
// возвращает его md-отчёт. Проверяющий получает свои декларированные входы
// плюс выходы проверяемого этапа; отчёт читается из декларированных выходов.
func (rs *runState) runCheckerAgent(ctx context.Context, stage config.TemplateStage, result notifier.StageResult) (string, error) {
	check := stage.Check
	definition, err := rs.p.reg.Load(check.Agent)
	if err != nil {
		return "", fmt.Errorf("агент проверки %s: %w", check.Agent, err)
	}
	previous := rs.extraInputs[stage.ID]
	rs.extraInputs[stage.ID] = result.Outputs
	_, inputs, inputsErr := rs.collectInputs(definition, stage.ID)
	rs.extraInputs[stage.ID] = previous
	if inputsErr != nil {
		return "", fmt.Errorf("входы агента проверки %s: %w", check.Agent, inputsErr)
	}
	checkerRuntime, err := rs.p.newRuntime(definition.RuntimeType)
	if err != nil {
		return "", fmt.Errorf("runtime агента проверки %s: %w", check.Agent, err)
	}
	agentCfg := rs.p.cfg.AgentConfig(check.Agent)
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Name: check.Agent}
	}
	runtimeAgent := &runtime.Agent{
		Name:         definition.Name,
		AttemptID:    rs.evidence.NewAttemptID(check.Agent, rs.attemptOrdinal),
		RuntimeType:  definition.RuntimeType,
		CLI:          definition.CLI,
		Prompt:       definition.Prompt,
		Inputs:       definition.Inputs,
		Outputs:      definition.Outputs,
		Verdict:      definition.Verdict,
		Kind:         definition.Kind,
		Mutation:     definition.Mutation,
		AllowedPaths: append([]string(nil), definition.AllowedPaths...),
		ReadScope:    definition.ReadScope,
	}
	if agentCfg.CLI != "" {
		runtimeAgent.CLI = agentCfg.CLI
	}
	runtimeAgent.Model = agentCfg.Model
	runtimeAgent.Effort = agentCfg.Effort
	runtimeAgent.MCPServers = append([]runtime.MCPServerConfig(nil), agentCfg.MCPServers...)
	task := *rs.task
	if err := checkerRuntime.Execute(ctx, runtimeAgent, &task, inputs); err != nil {
		return "", fmt.Errorf("агент проверки %s: %w", check.Agent, err)
	}
	outputs, err := rs.collectOutputs(definition, check.Agent)
	if err != nil {
		return "", fmt.Errorf("агент проверки %s: отчёт не создан: %w", check.Agent, err)
	}
	var builder strings.Builder
	for _, output := range outputs {
		if output.Path == "" || (output.Size > maxArtifactFileBytes) {
			continue
		}
		data, readErr := safeio.ReadRegularFile(output.Path, maxArtifactFileBytes)
		if readErr != nil {
			return "", fmt.Errorf("агент проверки %s: чтение отчёта: %w", check.Agent, readErr)
		}
		builder.Write(data)
		builder.WriteString("\n\n")
	}
	if strings.TrimSpace(builder.String()) == "" {
		return "", fmt.Errorf("агент проверки %s: отчёт пуст", check.Agent)
	}
	return builder.String(), nil
}

func (rs *runState) writeCheckReport(stageID string, report stagecheck.Report) (string, string, error) {
	data := report.Markdown()
	reportPath := filepath.Join(rs.runCfg.Feature, ".stage-check", stageID+".md")
	artifactTarget := filepath.Join(rs.task.ArtifactRoot, reportPath)
	if err := safeio.EnsureDirPath(filepath.Dir(artifactTarget)); err != nil {
		return "", "", err
	}
	if err := safeio.WriteRegularFileNoFollow(artifactTarget, data, 0644); err != nil {
		return "", "", err
	}
	runTargetDir := rs.evidence.RunDir()
	if _, err := safeio.EnsureDir(runTargetDir, "checks"); err != nil {
		return "", "", err
	}
	runTarget := filepath.Join(runTargetDir, "checks", stageID+".md")
	if _, err := os.Lstat(runTarget); err == nil {
		runTarget = filepath.Join(runTargetDir, "checks", fmt.Sprintf("%s-attempt-%d.md", stageID, rs.attemptOrdinal))
	}
	if err := safeio.WriteRegularFileNoFollow(runTarget, data, 0644); err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(data)
	return filepath.ToSlash(reportPath), hex.EncodeToString(digest[:]), nil
}

// requestCheckDecision поднимает человеческий запрос kind=check_decision:
// проверка не пройдена (или сорвалась), переход блокируется до решения
// ответственного за этап. На B-40 все действия ведут обратно на тот же этап
// (новая версия = повторный прогон и повторная проверка); обход и возврат
// наполняются B-41.
func (rs *runState) requestCheckDecision(stage config.TemplateStage, result notifier.StageResult, report stagecheck.Report, reportPath, reportSHA string) error {
	failedRules := make([]string, 0, len(report.Rules))
	for _, rule := range report.Rules {
		if rule.Status == "failed" || rule.Status == "error" {
			failedRules = append(failedRules, rule.Rule)
		}
	}
	payload, err := json.Marshal(approval.CheckDecisionPayload{
		Kind: string(approval.KindCheckDecision), StageID: stage.ID,
		CheckKind: report.Kind, Status: report.Status,
		ReportPath: reportPath, ReportSHA256: reportSHA, FailedRules: failedRules,
	})
	if err != nil {
		return err
	}
	actions := []string{"new_version", "override", "return"}
	targets := make(map[string]string, len(actions))
	for _, action := range actions {
		targets[action] = stage.ID
	}
	value, err := rs.approvalStore.Create(approval.PendingApproval{
		Kind: approval.KindCheckDecision, RunID: rs.runID, AttemptID: result.AttemptID,
		FromStage: stage.ID, ToStage: stage.ID, Trigger: approval.StageCheckTrigger,
		SubjectHash: reportSHA, RequiredRoles: []string{stage.Function},
		Quorum: approval.QuorumAny, Actions: actions, Targets: targets, Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("create check_decision approval для %s: %w", stage.ID, err)
	}
	if value.Status == approval.StatusResolved {
		return nil
	}
	requestedAt := time.Now().UTC()
	if err := rs.evidence.Append(evidence.Event{
		Type: "approval_requested", Stage: stage.ID, AttemptID: value.AttemptID,
		Timestamp: requestedAt, Data: approvalEventData(value),
	}); err != nil {
		return fmt.Errorf("запись check_decision запроса: %w", err)
	}
	if rs.p.recorder != nil {
		rs.p.recorder.ApprovalRequested(rs.runID, value.ID, value.AttemptID, requestedAt, approvalEventData(value))
	}
	if err := rs.saveWaiting(stage.ID, value.ID); err != nil {
		return err
	}
	return &ApprovalRequiredError{
		Checkpoint: fmt.Sprintf("проверка этапа %s (%s)", stage.ID, report.Status),
		RunID:      rs.runID, ApprovalID: value.ID, SubjectHash: value.SubjectHash,
	}
}
