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
	}); err != nil {
		return err
	}
	return c.Resume(runID)
}
