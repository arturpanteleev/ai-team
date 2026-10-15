package pipeline

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gate_retry.go — technical retries of gates and the evidence of overrides (S06).
//
// A gate that ended in error or timeout is a technical failure, not an opinion
// of the checker. It can be run again on the SAME result version:
//
//   - the person (or the REST/MCP caller) resolves the check decision with the
//     retry_gate action naming the gates;
//   - the controller records gate_retry_requested bound to the hash of the
//     result version and runs the chain again with Wave.Retry set; the gates
//     of the chain that already succeeded are not run again and the earlier
//     attempts stay in the history;
//   - a recorded retry request that no later check event consumed survives a
//     crash: the next resume runs the same retry.

// waveAttempts returns the recorded attempts of the wave of the result version.
func (rs *runState) waveAttempts(stage config.TemplateStage, result notifier.StageResult, sha string) ([]workflow.GateAttempt, error) {
	return rs.gateJournal().Load(rs.waveKey(stage, result, sha))
}

// latestAttempts folds the attempts of a version into the effective (latest)
// attempt of every gate, in order of first appearance.
func latestAttempts(attempts []workflow.GateAttempt) []workflow.GateAttempt {
	var order []string
	latest := map[string]workflow.GateAttempt{}
	for _, attempt := range attempts {
		if _, seen := latest[attempt.GateName]; !seen {
			order = append(order, attempt.GateName)
		}
		latest[attempt.GateName] = attempt
	}
	out := make([]workflow.GateAttempt, 0, len(order))
	for _, name := range order {
		out = append(out, latest[name])
	}
	return out
}

// retryableGates lists the blocking gates whose effective attempt is a
// technical failure (error or timeout) on this version.
func (rs *runState) retryableGates(stage config.TemplateStage, result notifier.StageResult, sha string) []string {
	attempts, err := rs.waveAttempts(stage, result, sha)
	if err != nil {
		return nil
	}
	var names []string
	for _, attempt := range latestAttempts(attempts) {
		if attempt.Blocking() && attempt.Execution.Technical() {
			names = append(names, attempt.GateName)
		}
	}
	sort.Strings(names)
	return names
}

// gateRetryRounds counts the retry requests of a stage attempt; each round
// raises its own check decision.
func (rs *runState) gateRetryRounds(stageID, attemptID string) int {
	events, found, err := rs.readEvidenceEvents()
	if err != nil || !found {
		return 0
	}
	rounds := 0
	for _, event := range events {
		if event.Type == gatesched.EventGateRetryRequested && event.Stage == stageID && event.AttemptID == attemptID {
			rounds++
		}
	}
	return rounds
}

// pendingGateRetry returns the gates of the latest retry request of this stage
// attempt and result version that no later check event consumed, or nil.
func (rs *runState) pendingGateRetry(stageID, attemptID, sha string) (map[string]bool, error) {
	if attemptID == "" {
		return nil, nil
	}
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return nil, fmt.Errorf("чтение запросов повтора гейтов %s: %w", stageID, err)
	}
	if !found {
		return nil, nil
	}
	var pending map[string]bool
	for _, event := range events {
		if event.Stage != stageID || event.AttemptID != attemptID {
			continue
		}
		recordedSHA := eventDataString(event, "artifact_sha256")
		if recordedSHA != "" && sha != "" && recordedSHA != sha {
			continue
		}
		switch event.Type {
		case gatesched.EventGateRetryRequested:
			pending = map[string]bool{}
			for _, name := range eventDataStrings(event, "gates") {
				pending[name] = true
			}
		case "check_passed", "check_failed", "check_error":
			pending = nil
		}
	}
	return pending, nil
}

func eventDataStrings(event evidence.Event, key string) []string {
	switch values := event.Data[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

// prepareGateRetry turns a resolved retry_gate check decision into the replay
// of the checks of the already finished stage attempt: it finds the attempt,
// requires its result to be exactly the version the failed check belongs to,
// and records the request. The record is idempotent per approval.
func (rs *runState) prepareGateRetry(stageID string) (notifier.StageResult, error) {
	value := rs.resumedApproval
	if value == nil || value.Kind != approval.KindCheckDecision || value.ResolvedAction != approval.ActionRetryGate {
		return notifier.StageResult{}, errors.New("повтор гейтов: отсутствует решённый check_decision с действием retry_gate")
	}
	decision := lastApprovalDecision(value)
	if len(decision.Gates) == 0 {
		return notifier.StageResult{}, errors.New("повтор гейтов: решение не называет гейты")
	}
	stage, ok := rs.p.templateStage(stageID)
	if !ok {
		return notifier.StageResult{}, fmt.Errorf("повтор гейтов: этап %s не найден", stageID)
	}
	var result *notifier.StageResult
	for index := len(rs.results) - 1; index >= 0; index-- {
		if rs.results[index].Name == stageID && rs.results[index].AttemptID == value.AttemptID {
			result = &rs.results[index]
			break
		}
	}
	if result == nil {
		return notifier.StageResult{}, fmt.Errorf("повтор гейтов: попытка %s этапа %s не найдена", value.AttemptID, stageID)
	}
	sha, err := rs.resultVersion(*result)
	if err != nil {
		return notifier.StageResult{}, fmt.Errorf("версия результата этапа %s: %w", stageID, err)
	}
	applied, _, err := rs.replayedStageCheckVersion(stageID, result.AttemptID, "")
	if err != nil {
		return notifier.StageResult{}, err
	}
	if applied.ArtifactSHA != "" && applied.ArtifactSHA != sha {
		return notifier.StageResult{}, fmt.Errorf("повтор гейтов: результат этапа %s изменился после проверки (было %s, стало %s); повтор возможен только на той же версии, запустите проверку новой версии",
			stageID, short(applied.ArtifactSHA), short(sha))
	}
	retryable := map[string]bool{}
	for _, name := range rs.retryableGates(stage, *result, sha) {
		retryable[name] = true
	}
	for _, name := range decision.Gates {
		if !retryable[name] {
			return notifier.StageResult{}, fmt.Errorf("повтор гейтов: гейт %q не завершился технической ошибкой на этой версии", name)
		}
	}
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return notifier.StageResult{}, fmt.Errorf("чтение событий повтора гейтов: %w", err)
	}
	if found {
		for _, event := range events {
			if event.Type == gatesched.EventGateRetryRequested && event.Stage == stageID && eventDataString(event, "approval_id") == value.ID {
				return *result, nil // recorded before a crash: replay it
			}
		}
	}
	data := map[string]any{
		"approval_id": value.ID, "gates": decision.Gates, "artifact_sha256": sha,
		"actor_id": decision.ActorID, "actor_role": decision.ActorRole, "visit": rs.waveKey(stage, *result, sha).Visit,
		"round": rs.gateRetryRounds(stageID, result.AttemptID) + 1, "severity": "info",
	}
	if reason := strings.TrimSpace(decision.Comment); reason != "" {
		data["reason"] = reason
	}
	event := evidence.Event{
		Type: gatesched.EventGateRetryRequested, Stage: stageID, AttemptID: result.AttemptID,
		Timestamp: time.Now().UTC(), Data: data,
	}
	if err := rs.evidence.Append(event); err != nil {
		return notifier.StageResult{}, fmt.Errorf("запись %s для %s: %w", event.Type, stageID, err)
	}
	rs.recordTypedEvent(event)
	return *result, nil
}

// overrideEvidence builds the immutable part of the check_overridden event
// that binds the override to the result version, the stage visit and the
// exact blocking attempts it lets pass. The attempts stay as recorded: the
// override changes the decision of the controller, never the outcome.
func (rs *runState) overrideEvidence(stage config.TemplateStage, result notifier.StageResult, sha string) (map[string]any, error) {
	attempts, err := rs.waveAttempts(stage, result, sha)
	if err != nil {
		return nil, fmt.Errorf("чтение попыток гейтов для обхода %s: %w", stage.ID, err)
	}
	refs := automaticGateGroups(stage).Refs()
	statuses, resolution := gatesched.Resolve(refs, nil, sha, attempts, nil)
	var blocking []workflow.GateAttempt
	for _, status := range statuses {
		if status.Blocking && status.Attempt != nil {
			blocking = append(blocking, *status.Attempt)
		}
	}
	data := map[string]any{
		"artifact_sha256": sha, "visit": rs.waveKey(stage, result, sha).Visit,
		"gates":   gatesched.OverriddenGates(blocking),
		"outcome": overriddenOutcome(blocking),
	}
	if len(resolution.Warnings) > 0 {
		data["advisory_warnings"] = resolution.Warnings
	}
	return data, nil
}

// overriddenOutcome names what was overridden: a required fail is the main
// case; a pure technical failure is overridden only by an administrator too.
func overriddenOutcome(blocking []workflow.GateAttempt) string {
	for _, attempt := range blocking {
		if attempt.Execution == workflow.GateExecutionSucceeded {
			return string(workflow.GateVerdictFail)
		}
	}
	return "error"
}
