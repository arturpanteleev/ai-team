package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

// OverrideStageCheck разрешает ожидающее решение kind=check_decision
// действием override: проваленная проверка этапа обходится явным решением
// ответственного за этап, после чего run возобновляется и этап переходит
// дальше как «пройденный с обходом». Причина обязательна.
//
// Право на обход принадлежит только системному администратору
// (cloudidentity.PermissionGateOverride): проверку делает вызывающий сервер до
// вызова, контроллер же записывает в решение authority=system_admin, и именно
// оно попадает в неизменяемую запись check_overridden.
func (c *Controller) OverrideStageCheck(_ context.Context, runID, stageID, reason, actorID, actorRole string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("обход проверки требует причину")
	}
	if strings.TrimSpace(actorID) == "" {
		return errors.New("actor id is required to override a stage check")
	}
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		return errors.New("обход проверки требует ожидающего решения по проверке этапа")
	}
	if state.NextStage != stageID {
		return fmt.Errorf("stage %q is not current", stageID)
	}
	value, err := c.approvals.Load(runID, state.PendingApprovalID)
	if err != nil {
		return err
	}
	if value.Kind != approval.KindCheckDecision || value.Trigger != approval.StageCheckTrigger ||
		value.FromStage != stageID || value.Status != approval.StatusPending {
		return errors.New("текущее ожидающее решение не является решением по проверке этапа")
	}
	if !containsAction(value.Actions, "override") || value.Targets["override"] != stageID {
		return errors.New("решение не предлагает обход проверки для этого этапа")
	}
	if _, err := c.approvals.Decide(runID, value.ID, approval.Decision{
		ActorID: actorID, ActorRole: actorRole, Action: "override", Comment: reason,
		SubjectHash: value.SubjectHash, ControllerAuthenticated: true,
		Authority: approval.AuthoritySystemAdmin,
	}); err != nil {
		return err
	}
	return c.Resume(runID)
}

// MaxGateRetryRounds bounds the retries of technically failed gates for one
// stage attempt (one result version): a gate that keeps failing technically
// must end in a new version, a return or an administrator decision, not loop.
const MaxGateRetryRounds = 10

// RetryStageGates resolves the pending check_decision with retry_gate: the
// named gates, whose latest attempt is a technical failure (error/timeout),
// run again on the SAME result version; gates that already succeeded are not
// repeated and the earlier attempts stay. The decision is idempotent per
// actor: a redelivered request does not create a second one.
func (c *Controller) RetryStageGates(_ context.Context, runID, stageID string, gates []string, reason, actorID, actorRole string) error {
	if strings.TrimSpace(actorID) == "" {
		return errors.New("actor id is required to retry a gate")
	}
	names := make([]string, 0, len(gates))
	seen := map[string]bool{}
	for _, gate := range gates {
		gate = strings.TrimSpace(gate)
		if gate != "" && !seen[gate] {
			seen[gate] = true
			names = append(names, gate)
		}
	}
	if len(names) == 0 {
		return errors.New("retry of a gate requires a gate name")
	}
	state, err := c.loadLifecycle(runID)
	if err != nil {
		return err
	}
	if state.Phase != lifecycle.PhaseWaiting || state.PendingApprovalID == "" {
		return errors.New("повтор гейта требует ожидающего решения по проверке этапа")
	}
	if state.NextStage != stageID {
		return fmt.Errorf("stage %q is not current", stageID)
	}
	value, err := c.approvals.Load(runID, state.PendingApprovalID)
	if err != nil {
		return err
	}
	if value.Kind != approval.KindCheckDecision || value.Trigger != approval.StageCheckTrigger ||
		value.FromStage != stageID || value.Status != approval.StatusPending {
		return errors.New("текущее ожидающее решение не является решением по проверке этапа")
	}
	if !containsAction(value.Actions, approval.ActionRetryGate) || value.Targets[approval.ActionRetryGate] != stageID {
		return errors.New("повтор гейта недоступен: нет гейтов, завершившихся технической ошибкой (error/timeout)")
	}
	all, err := c.approvals.List(runID)
	if err != nil {
		return err
	}
	rounds := 0
	for _, other := range all {
		if other.Kind == approval.KindCheckDecision && other.FromStage == stageID && other.AttemptID == value.AttemptID &&
			other.Status == approval.StatusResolved && other.ResolvedAction == approval.ActionRetryGate {
			rounds++
		}
	}
	if rounds >= MaxGateRetryRounds {
		return fmt.Errorf("лимит повторов гейтов исчерпан: %d раундов на этап %s; остаются новая версия результата, возврат или обход администратором", MaxGateRetryRounds, stageID)
	}
	if _, err := c.approvals.Decide(runID, value.ID, approval.Decision{
		ActorID: actorID, ActorRole: actorRole, Action: approval.ActionRetryGate, Comment: reason,
		Gates: names, SubjectHash: value.SubjectHash, ControllerAuthenticated: true,
	}); err != nil {
		return err
	}
	return c.Resume(runID)
}
