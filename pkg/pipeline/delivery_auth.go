package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
)

// delivery_auth.go — предусловия, canonical plan и authorization delivery.

func deliveryPlanFromOutputs(outputs []runtime.Artifact) (delivery.Plan, error) {
	for _, output := range outputs {
		if output.Name != "plan" {
			continue
		}
		data, err := os.ReadFile(output.Path)
		if err != nil {
			return delivery.Plan{}, err
		}
		return delivery.Parse(data)
	}
	return delivery.Plan{}, fmt.Errorf("delivery output plan отсутствует")
}

func toEvidenceArtifacts(artifacts []runtime.Artifact) []evidence.Artifact {
	result := make([]evidence.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		result = append(result, evidence.Artifact{Name: artifact.Name, Path: artifact.Path, SourcePath: artifact.Source})
	}
	return result
}

func toRuntimeArtifacts(artifacts []evidence.Artifact) []runtime.Artifact {
	result := make([]runtime.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		result = append(result, runtime.Artifact{Name: artifact.Name, Path: artifact.Path, Source: artifact.SourcePath})
	}
	return result
}

func (rs *runState) validateDeliveryChecks() error {
	_, err := rs.currentDeliveryVerification()
	return err
}

func (rs *runState) currentDeliveryVerification() (delivery.Verification, error) {
	workspaceDigest, err := checks.WorkspaceDigest(rs.sourceDir())
	if err != nil {
		return delivery.Verification{}, fmt.Errorf("delivery workspace digest: %w", err)
	}
	for resultIndex := len(rs.results) - 1; resultIndex >= 0; resultIndex-- {
		result := rs.results[resultIndex]
		if result.Superseded || result.Err != nil {
			continue
		}
		for checkIndex := len(result.Checks) - 1; checkIndex >= 0; checkIndex-- {
			check := result.Checks[checkIndex]
			if checks.IsTestEvidence(check) &&
				check.WorkspaceDigestBefore == workspaceDigest && check.WorkspaceDigestAfter == workspaceDigest && check.EvidenceDigest != "" {
				return delivery.Verification{
					SourceRunID: rs.runID, WorkspaceDigest: workspaceDigest, CheckEvidenceDigest: check.EvidenceDigest,
				}, nil
			}
		}
	}
	if plan, prepared, loadErr := delivery.LoadPreparedPlan(rs.sourceDir(), rs.runCfg.Feature); loadErr != nil {
		return delivery.Verification{}, fmt.Errorf("проверка prepared delivery plan: %w", loadErr)
	} else if prepared {
		if plan.VerifiedWorkspaceDigest != workspaceDigest {
			return delivery.Verification{}, fmt.Errorf("prepared delivery plan проверял workspace %s, текущее состояние %s", plan.VerifiedWorkspaceDigest, workspaceDigest)
		}
		if verifyErr := evidence.VerifyCheckEvidence(filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs"), plan.SourceRunID, plan.CheckEvidenceDigest, workspaceDigest); verifyErr != nil {
			return delivery.Verification{}, fmt.Errorf("prepared delivery provenance: %w", verifyErr)
		}
		return delivery.Verification{
			SourceRunID: plan.SourceRunID, WorkspaceDigest: workspaceDigest, CheckEvidenceDigest: plan.CheckEvidenceDigest,
		}, nil
	}
	return delivery.Verification{}, fmt.Errorf("delivery запрещён: нет успешно выполненного required check класса unit/integration/e2e для точного текущего workspace digest %s", workspaceDigest)
}

// deliveryApprovalRole — роль, санкционирующая delivery plan. В cloud-режиме
// совпадает с cloudidentity.RoleReleaseManager; локальный CLI решает под
// actor "local-user".
const deliveryApprovalRole = "release_manager"

func (rs *runState) authorizeDelivery(name string, result notifier.StageResult, plan delivery.Plan) error {
	planHash, err := plan.Hash()
	if err != nil {
		return err
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return err
	}
	showPipelineSummary(rs.results)
	fmt.Printf("\n%s\n%s\nPlan SHA-256: %s\n", ui.Colorize("Canonical delivery plan:", ui.ColorBold), canonical, planHash)
	recordApproval := func(mode string) error {
		rs.approvedPlanHash = planHash
		return rs.evidence.Append(evidence.Event{Type: "delivery_plan_approved", AttemptID: result.AttemptID, Timestamp: time.Now().UTC(), Data: map[string]any{
			"plan_hash": planHash, "mode": mode, "approver": "local-user",
		}})
	}
	if rs.approvedPlanHash != "" {
		if rs.approvedPlanHash != planHash {
			return fmt.Errorf("delivery approval hash mismatch: approved=%s actual=%s", rs.approvedPlanHash, planHash)
		}
		// Явное подтверждение точного canonical plan (--approve-plan) расширяется
		// на отложенные гейты run'а (APF-1).
		if err := rs.ratifyDeferredGates("local-user", "approve"); err != nil {
			return err
		}
		return recordApproval("hash_flag")
	}

	// Delivery plan — обычный persisted approval с subject = exact plan hash.
	// Решение можно записать интерактивно, через web decision endpoint или
	// через `--resume --approve-plan <sha256>` (см. resume в RunWithResult).
	candidateSHA := ""
	if rs.candidate != nil {
		identity, identityErr := rs.candidate.Identity()
		if identityErr != nil {
			return identityErr
		}
		candidateSHA = identity.WorkspaceSHA256
	}
	value, err := rs.approvalStore.Create(approval.PendingApproval{
		RunID: rs.runID, AttemptID: result.AttemptID,
		FromStage: name, ToStage: name, Trigger: "delivery_plan",
		SubjectHash: planHash, CandidateSHA256: candidateSHA,
		RequiredRoles: []string{deliveryApprovalRole}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": name, "reject": name},
		Payload: json.RawMessage(canonical),
	})
	if err != nil {
		return err
	}
	requestedAt := time.Now().UTC()
	if err := rs.evidence.Append(evidence.Event{
		Type: "approval_requested", AttemptID: result.AttemptID,
		Timestamp: requestedAt, Data: approvalEventData(value),
	}); err != nil {
		return err
	}
	if rs.p.recorder != nil {
		rs.p.recorder.ApprovalRequested(rs.runID, value.ID, result.AttemptID, requestedAt, approvalEventData(value))
	}

	action := ""
	if rs.p.prompter.Interactive() {
		prompt := fmt.Sprintf("%s %s может выполнить commit/push/PR. Продолжить? [y/N]",
			ui.Colorize("Delivery:", ui.ColorBold), ui.Colorize(name, ui.ColorYellow))
		if deferredCount := rs.pendingDeferredCount(); deferredCount > 0 {
			prompt = fmt.Sprintf("%s %s консолидирует %d отложенных approval-гейта. Продолжить? [y/N]",
				ui.Colorize("Delivery:", ui.ColorBold), ui.Colorize(name, ui.ColorYellow), deferredCount)
		}
		ans := rs.p.prompter.Ask(prompt)
		if ans == "y" {
			action = "approve"
		} else {
			action = "reject"
		}
	}
	if action == "" {
		if err := rs.saveWaiting(name, value.ID); err != nil {
			return err
		}
		fmt.Printf("Решение: ai-team decision --run %s --approval %s --actor <id> --role %s --action approve|reject --subject %s\n",
			rs.runID, value.ID, deliveryApprovalRole, planHash)
		fmt.Printf("Для продолжения с явным подтверждением плана: ai-team run --resume %s --approve-plan %s\n",
			rs.runID, planHash)
		return &ApprovalRequiredError{
			Checkpoint: "delivery перед " + name, RunID: rs.runID,
			ApprovalID: value.ID, SubjectHash: value.SubjectHash,
		}
	}
	value, err = rs.approvalStore.Decide(value.RunID, value.ID, approval.Decision{
		ActorID: "local-user", ActorRole: deliveryApprovalRole,
		Action: action, SubjectHash: value.SubjectHash,
	})
	if err != nil {
		return err
	}
	decidedAt := time.Now().UTC()
	if err := rs.evidence.Append(evidence.Event{
		Type: "approval_decided", AttemptID: result.AttemptID,
		Timestamp: decidedAt, Data: approvalEventData(value),
	}); err != nil {
		return err
	}
	if rs.p.recorder != nil {
		rs.p.recorder.ApprovalDecided(rs.runID, value.ID, result.AttemptID, decidedAt, approvalEventData(value))
	}
	if action == "reject" {
		if err := rs.ratifyDeferredGates("local-user", "reject"); err != nil {
			return err
		}
		return fmt.Errorf("%w: delivery перед %s отклонён человеком", ErrUserStopped, name)
	}
	if err := rs.ratifyDeferredGates("local-user", "approve"); err != nil {
		return err
	}
	return recordApproval("resolved_approval")
}

// pendingDeferredCount — число ждущих consolidated-подтверждения deferred-гейтов
// текущего run (для осмысленного решения человека в delivery-промпте, APF-1).
func (rs *runState) pendingDeferredCount() int {
	list, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return 0
	}
	count := 0
	for _, value := range list {
		if value.Status == approval.StatusPending && value.Deferred {
			count++
		}
	}
	return count
}

// ratifyDeferredGates разрешает все pending deferred-гейты run'а одним
// consolidated delivery-решением (APF-1). Действие человека (approve/reject)
// распространяется на каждый отложенный гейт; точный subject каждого approval
// проверяется в approval store. Уже разрешённые гейты не трогаются.
func (rs *runState) ratifyDeferredGates(actorID, action string) error {
	list, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return err
	}
	ratified := make([]map[string]string, 0)
	for _, value := range list {
		if value.Status != approval.StatusPending || !value.Deferred {
			continue
		}
		decidedAction := action
		if !containsString(value.Actions, decidedAction) {
			if containsString(value.Actions, "reject") {
				decidedAction = "reject"
			} else {
				decidedAction = value.Actions[0]
			}
		}
		resolved, err := rs.approvalStore.ResolveDeferred(rs.runID, value.ID, approval.Decision{
			ActorID: actorID, ActorRole: deliveryApprovalRole,
			Action: decidedAction, SubjectHash: value.SubjectHash,
			Comment: "consolidated delivery decision",
		})
		if err != nil {
			return fmt.Errorf("deferred approval %s: %w", value.ID, err)
		}
		decidedAt := time.Now().UTC()
		if err := rs.evidence.Append(evidence.Event{
			Type: "approval_decided", AttemptID: resolved.AttemptID,
			Timestamp: decidedAt, Data: approvalEventData(resolved),
		}); err != nil {
			return err
		}
		if rs.p.recorder != nil {
			rs.p.recorder.ApprovalDecided(rs.runID, resolved.ID, resolved.AttemptID, decidedAt, approvalEventData(resolved))
		}
		ratified = append(ratified, map[string]string{
			"approval_id": resolved.ID, "subject_hash": resolved.SubjectHash, "action": resolved.ResolvedAction,
		})
	}
	if len(ratified) > 0 {
		ratifiedAt := time.Now().UTC()
		if err := rs.evidence.Append(evidence.Event{
			Type: "deferred_gates_ratified", Timestamp: ratifiedAt,
			Data: map[string]any{"action": action, "approver": actorID, "gates": ratified},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (rs *runState) writeDeliveryPlan(ctx context.Context, a *agent.Agent, preconditions map[string]delivery.PreconditionEvidence) error {
	files := rs.attributedDeliveryFiles()
	var plan delivery.Plan
	var err error
	if len(files) == 0 {
		var exists bool
		plan, exists, err = delivery.LoadPreparedPlan(rs.sourceDir(), rs.runCfg.Feature)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("delivery planner: в текущем run нет атрибутированных изменений и prepared plan отсутствует")
		}
		workspaceDigest, digestErr := checks.WorkspaceDigest(rs.sourceDir())
		if digestErr != nil {
			return digestErr
		}
		if verifyErr := delivery.VerifyPreparedWorkspace(rs.sourceDir(), plan, workspaceDigest); verifyErr != nil {
			return verifyErr
		}
		if verifyErr := evidence.VerifyCheckEvidence(filepath.Join(rs.runCfg.TargetDir, ".ai-team", "runs"), plan.SourceRunID, plan.CheckEvidenceDigest, workspaceDigest); verifyErr != nil {
			return fmt.Errorf("prepared delivery provenance: %w", verifyErr)
		}
		if verifyErr := delivery.VerifyPreconditions(plan, preconditions); verifyErr != nil {
			return verifyErr
		}
	} else {
		verification, verificationErr := rs.currentDeliveryVerification()
		if verificationErr != nil {
			return verificationErr
		}
		verification.Preconditions = preconditions
		plan, err = delivery.BuildPlan(ctx, rs.sourceDir(), rs.runCfg.Feature, rs.runCfg.TaskDesc, files, verification)
		if err != nil {
			return err
		}
	}
	planPath, exists := a.Outputs["plan"]
	if !exists {
		return fmt.Errorf("delivery definition не содержит output plan")
	}
	fullPath, err := confinedArtifactPath(rs.task.ArtifactRoot, runtime.ReplaceVars(planPath, rs.runCfg.Feature))
	if err != nil {
		return err
	}
	return delivery.WritePlan(fullPath, plan)
}

func (rs *runState) attributedDeliveryFiles() []string {
	seen := make(map[string]bool)
	var files []string
	for _, result := range rs.results {
		if result.Err != nil {
			continue
		}
		for _, changedPath := range result.Mutations {
			changedPath = filepath.ToSlash(changedPath)
			if changedPath == ".ai-team" || strings.HasPrefix(changedPath, ".ai-team/") || seen[changedPath] {
				continue
			}
			seen[changedPath] = true
			files = append(files, changedPath)
		}
	}
	sort.Strings(files)
	return files
}

func validateSnapshotPreconditions(name string, a *agent.Agent, inputs []runtime.Artifact) (map[string]delivery.PreconditionEvidence, error) {
	result := make(map[string]delivery.PreconditionEvidence, len(a.Preconditions))
	byName := make(map[string]runtime.Artifact, len(inputs))
	for _, input := range inputs {
		byName[input.Name] = input
	}
	inputNames := make([]string, 0, len(a.Preconditions))
	for inputName := range a.Preconditions {
		inputNames = append(inputNames, inputName)
	}
	sort.Strings(inputNames)
	for _, inputName := range inputNames {
		artifact, exists := byName[inputName]
		if !exists {
			return nil, fmt.Errorf("агент %s: immutable precondition input %s отсутствует", name, inputName)
		}
		actual, err := verdict.FromOutputsContract([]string{artifact.Path}, a.Preconditions[inputName])
		if err != nil {
			return nil, fmt.Errorf("агент %s: precondition %s не выполнен на immutable snapshot: %w", name, inputName, err)
		}
		if actual.IsNegative() {
			return nil, fmt.Errorf("агент %s: precondition %s отклонён verdict %s", name, inputName, actual)
		}
		artifactType, size, digest, err := evidence.ArtifactDigest(artifact.Path)
		if err != nil {
			return nil, fmt.Errorf("агент %s: precondition %s digest: %w", name, inputName, err)
		}
		result[inputName] = delivery.PreconditionEvidence{Type: artifactType, Size: size, SHA256: digest, Verdict: string(actual)}
	}
	return result, nil
}

// enforce обрабатывает негативный вердикт: loopback всегда требует
// сохранённого решения человека и не зависит от наличия TTY.
// Возвращает индекс цели loopback (-1, если loopback не выполняется).
// isBackwardTransition определяет loopback семантически — по позиции цели
// относительно источника в скомпилированном графе, а не по имени action.
// Delivery approval не является переходом и никогда не считается loopback.
