package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/logging"
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
	requirements, configured, err := rs.templateDeliveryRequirements()
	if err != nil {
		return err
	}
	if configured {
		if len(requirements.Delivery.RequireChecks) == 0 {
			return fmt.Errorf("delivery запрещён: в delivery.require_checks не задано ни одной обязательной проверки")
		}
		workspaceDigest, digestErr := checks.WorkspaceDigest(rs.sourceDir())
		if digestErr != nil {
			return fmt.Errorf("delivery workspace digest: %w", digestErr)
		}
		for _, checkName := range requirements.Delivery.RequireChecks {
			if _, _, checkErr := rs.requiredControllerCheck(*requirements, checkName, workspaceDigest); checkErr != nil {
				return checkErr
			}
		}
		if _, evidenceErr := rs.requiredDeliveryVerdictEvidence(*requirements); evidenceErr != nil {
			return evidenceErr
		}
	}
	_, err = rs.currentDeliveryVerification()
	return err
}

func (rs *runState) currentDeliveryVerification() (delivery.Verification, error) {
	workspaceDigest, err := checks.WorkspaceDigest(rs.sourceDir())
	if err != nil {
		return delivery.Verification{}, fmt.Errorf("delivery workspace digest: %w", err)
	}
	requirements, configured, err := rs.templateDeliveryRequirements()
	if err != nil {
		return delivery.Verification{}, err
	}
	if configured {
		if len(requirements.Delivery.RequireChecks) == 0 {
			return delivery.Verification{}, fmt.Errorf("delivery запрещён: в delivery.require_checks не задано ни одной обязательной проверки")
		}
		checkName := requirements.Delivery.RequireChecks[0]
		_, check, checkErr := rs.requiredControllerCheck(*requirements, checkName, workspaceDigest)
		if checkErr != nil {
			return delivery.Verification{}, checkErr
		}
		return delivery.Verification{
			SourceRunID: rs.runID, WorkspaceDigest: workspaceDigest, CheckEvidenceDigest: check.EvidenceDigest,
		}, nil
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
				if _, manifestErr := rs.verifiedCheckManifest(result, check); manifestErr != nil {
					continue
				}
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

func (rs *runState) templateDeliveryRequirements() (*config.TemplateStage, bool, error) {
	if rs == nil || rs.p == nil || rs.p.cfg == nil || rs.p.cfg.Template == "" {
		return nil, false, nil
	}
	var selected *config.TemplateStage
	for i := range rs.p.cfg.Stages {
		stage := &rs.p.cfg.Stages[i]
		if stage.Delivery == nil {
			continue
		}
		if selected != nil {
			return nil, false, fmt.Errorf("delivery запрещён: несколько stages задают delivery.require_checks")
		}
		selected = stage
	}
	return selected, selected != nil, nil
}

func (rs *runState) stageResult(stage config.TemplateStage) (notifier.StageResult, bool) {
	for i := len(rs.results) - 1; i >= 0; i-- {
		result := rs.results[i]
		if !result.Superseded && result.Name == stage.ID {
			return result, true
		}
	}
	if stage.Agent != "" && stage.Agent != stage.ID {
		for i := len(rs.results) - 1; i >= 0; i-- {
			result := rs.results[i]
			if !result.Superseded && result.Name == stage.Agent {
				return result, true
			}
		}
	}
	return notifier.StageResult{}, false
}

func (rs *runState) verifiedAttemptManifest(result notifier.StageResult) (evidence.AttemptManifest, error) {
	if result.AttemptID == "" || result.RunID != "" && result.RunID != rs.runID {
		return evidence.AttemptManifest{}, fmt.Errorf("delivery evidence: attempt identity отсутствует или относится к другому run")
	}
	_, manifest, err := evidence.ReadAttemptManifest(rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, result.AttemptID)
	if err != nil {
		return evidence.AttemptManifest{}, fmt.Errorf("delivery evidence: attempt %s manifest: %w", result.AttemptID, err)
	}
	if manifest.RunID != rs.runID || manifest.AttemptID != result.AttemptID || manifest.Stage != result.Name ||
		manifest.Status != result.Status || manifest.Verdict != string(result.Verdict) {
		return evidence.AttemptManifest{}, fmt.Errorf("delivery evidence: attempt %s result does not match its immutable manifest", result.AttemptID)
	}
	return manifest, nil
}

func (rs *runState) verifiedCheckManifest(result notifier.StageResult, check checks.Result) (checks.Result, error) {
	if !checks.VerifyResultDigest(check) {
		return checks.Result{}, fmt.Errorf("delivery запрещён: controller check %s имеет невалидный evidence digest", check.Name)
	}
	manifest, err := rs.verifiedAttemptManifest(result)
	if err != nil {
		return checks.Result{}, err
	}
	for _, saved := range manifest.Checks {
		if saved.Name == check.Name && saved.EvidenceDigest == check.EvidenceDigest && checks.VerifyResultDigest(saved) {
			return saved, nil
		}
	}
	return checks.Result{}, fmt.Errorf("delivery запрещён: controller check %s отсутствует в immutable attempt manifest", check.Name)
}

func (rs *runState) requiredControllerCheck(stage config.TemplateStage, checkName, workspaceDigest string) (notifier.StageResult, checks.Result, error) {
	result, exists := rs.stageResult(stage)
	if !exists {
		return notifier.StageResult{}, checks.Result{}, fmt.Errorf("delivery запрещён: требуемая проверка %s не запускалась на этапе %s", checkName, stage.ID)
	}
	if result.Status != notifier.StatusPassed || result.Err != nil {
		return result, checks.Result{}, fmt.Errorf("delivery запрещён: этап %s с required check %s не завершился успешно", stage.ID, checkName)
	}
	manifest, err := rs.verifiedAttemptManifest(result)
	if err != nil {
		return result, checks.Result{}, err
	}
	for _, check := range manifest.Checks {
		if check.Name != checkName {
			continue
		}
		if check.Policy != checks.PolicyRequired || check.Status != checks.StatusPassed {
			return result, checks.Result{}, fmt.Errorf("delivery запрещён: required check %s должен иметь controller status passed и policy required", checkName)
		}
		if check.WorkspaceDigestBefore != workspaceDigest || check.WorkspaceDigestAfter != workspaceDigest {
			return result, checks.Result{}, fmt.Errorf("delivery запрещён: required check %s проверил другой workspace digest", checkName)
		}
		saved, checkErr := rs.verifiedCheckManifest(result, check)
		if checkErr != nil {
			return result, checks.Result{}, checkErr
		}
		return result, saved, nil
	}
	return result, checks.Result{}, fmt.Errorf("delivery запрещён: controller evidence required check %s отсутствует в immutable manifest этапа %s", checkName, stage.ID)
}

func (rs *runState) requiredDeliveryVerdictEvidence(deliveryStage config.TemplateStage) (map[string]delivery.PreconditionEvidence, error) {
	if deliveryStage.Delivery == nil {
		return nil, fmt.Errorf("delivery verdict evidence: template stage %s has no delivery configuration", deliveryStage.ID)
	}
	evidenceSet := make(map[string]delivery.PreconditionEvidence, len(deliveryStage.Delivery.RequireVerdicts))
	for _, requiredStageID := range deliveryStage.Delivery.RequireVerdicts {
		var requiredStage *config.TemplateStage
		for i := range rs.p.cfg.Stages {
			if rs.p.cfg.Stages[i].ID == requiredStageID {
				requiredStage = &rs.p.cfg.Stages[i]
				break
			}
		}
		if requiredStage == nil {
			return nil, fmt.Errorf("delivery запрещён: required verdict stage %s не найден", requiredStageID)
		}
		result, exists := rs.stageResult(*requiredStage)
		if !exists || result.Status != notifier.StatusPassed || result.Err != nil {
			return nil, fmt.Errorf("delivery запрещён: required verdict stage %s отсутствует или не прошёл", requiredStageID)
		}
		definitionName := requiredStage.Agent
		if definitionName == "" {
			definitionName = requiredStage.ID
		}
		definition, loadErr := rs.p.reg.Load(definitionName)
		if loadErr != nil {
			return nil, fmt.Errorf("delivery required verdict %s: definition: %w", requiredStageID, loadErr)
		}
		if definition.Verdict == nil || !definition.Verdict.Required {
			return nil, fmt.Errorf("delivery запрещён: required verdict stage %s не имеет required verdict contract", requiredStageID)
		}
		manifest, manifestErr := rs.verifiedAttemptManifest(result)
		if manifestErr != nil {
			return nil, manifestErr
		}
		var outputPaths []string
		var outputRecords []evidence.ArtifactRecord
		for _, output := range manifest.Outputs {
			if output.Type != "file" {
				continue
			}
			if output.ProducerRunID != rs.runID || output.ProducerAttemptID != result.AttemptID || output.ProducerStage != result.Name ||
				output.EvidencePath == "" || output.Size <= 0 || output.SHA256 == "" {
				return nil, fmt.Errorf("delivery запрещён: required verdict stage %s output identity не подтверждена", requiredStageID)
			}
			outputPath := filepath.Join(rs.evidence.RunDir(), filepath.FromSlash(output.EvidencePath))
			artifactType, size, digest, digestErr := evidence.ArtifactDigest(outputPath)
			if digestErr != nil || artifactType != output.Type || size != output.Size || digest != output.SHA256 {
				return nil, fmt.Errorf("delivery запрещён: immutable output %s этапа %s не совпадает с manifest", output.Name, requiredStageID)
			}
			outputPaths = append(outputPaths, outputPath)
			outputRecords = append(outputRecords, output)
		}
		if len(outputPaths) == 0 {
			return nil, fmt.Errorf("delivery запрещён: immutable output required verdict stage %s отсутствует", requiredStageID)
		}
		actual, verdictErr := verdict.FromOutputsContract(outputPaths, definition.Verdict)
		if verdictErr != nil || actual != verdict.Verdict(manifest.Verdict) || actual != result.Verdict {
			return nil, fmt.Errorf("delivery запрещён: required verdict stage %s не совпадает с immutable output contract", requiredStageID)
		}
		if actual != verdict.Approved && actual != verdict.Pass {
			return nil, fmt.Errorf("delivery запрещён: required verdict stage %s имеет отрицательный verdict %s", requiredStageID, actual)
		}
		for _, output := range outputRecords {
			evidenceSet["verdict:"+requiredStageID+":"+output.Name] = delivery.PreconditionEvidence{
				Type: output.Type, Size: output.Size, SHA256: output.SHA256, Verdict: string(actual),
			}
		}
	}
	return evidenceSet, nil
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
	candidateSHA := ""
	if rs.candidate != nil {
		identity, identityErr := rs.candidate.Identity()
		if identityErr != nil {
			return identityErr
		}
		candidateSHA = identity.WorkspaceSHA256
	}
	showPipelineSummary(rs.results)
	logging.Printf("\n%s\n%s\nPlan SHA-256: %s\n", ui.Colorize("Canonical delivery plan:", ui.ColorBold), canonical, planHash)
	recordApproval := func(mode string, resolved *approval.PendingApproval) error {
		if mode == "hash_flag" {
			value, err := rs.persistHashFlagDeliveryApproval(name, result.AttemptID, planHash, candidateSHA, canonical)
			if err != nil {
				return err
			}
			resolved = &value
		}
		if resolved == nil {
			return errors.New("delivery approval decision is missing")
		}
		if err := requireResolvedDeliveryApproval(rs.approvalStore, rs.runID, name, resolved.AttemptID, planHash, canonical, candidateSHA); err != nil {
			return err
		}
		if resolved.ID != approval.NewID(rs.runID, resolved.AttemptID, name, name, "delivery_plan", planHash) {
			return errors.New("delivery approval does not identify the saved run, attempt, stage, and plan")
		}
		rs.approvedPlanHash = planHash
		eventMode := mode
		eventData := map[string]any{
			"plan_hash": planHash, "mode": eventMode, "approver": "local-user",
			"approval_attempt_id": resolved.AttemptID, "operation_attempt_id": result.AttemptID,
		}
		if mode == "resolved_approval_reused" {
			// Replay accepts the canonical authority mode; retain reuse as a
			// separate fact instead of making it an unsupported mode value.
			eventData["mode"] = "resolved_approval"
			eventData["reused"] = true
		}
		return rs.evidence.Append(evidence.Event{Type: "delivery_plan_approved", AttemptID: result.AttemptID, Timestamp: time.Now().UTC(), Data: eventData})
	}
	if rs.approvedPlanHash != "" {
		if rs.approvedPlanHash != planHash {
			return fmt.Errorf("delivery approval hash mismatch: approved=%s actual=%s", rs.approvedPlanHash, planHash)
		}
		if rs.resumedApproval != nil &&
			rs.resumedApproval.FromStage == name && rs.resumedApproval.ToStage == name &&
			rs.resumedApproval.Trigger == "delivery_plan" &&
			rs.resumedApproval.Status == approval.StatusResolved &&
			rs.resumedApproval.ResolvedAction == "approve" && rs.resumedApproval.SubjectHash == planHash {
			// A human approval may outlive a retry of the delivery stage only when
			// the exact canonical plan and candidate still match its saved subject.
			if err := requireResolvedDeliveryApproval(rs.approvalStore, rs.runID, name,
				rs.resumedApproval.AttemptID, planHash, canonical, candidateSHA); err != nil {
				return fmt.Errorf("saved delivery approval no longer matches the planned operation: %w", err)
			}
			if err := approvalAttemptMatchesStage(rs.p.attemptManifestSource, rs.evidence.RunDir(),
				rs.runID, rs.resumedApproval.AttemptID, name); err != nil {
				return fmt.Errorf("saved delivery approval attempt cannot be verified: %w", err)
			}
			if err := rs.ratifyDeferredGates("local-user", "approve"); err != nil {
				return err
			}
			return recordApproval("resolved_approval_reused", rs.resumedApproval)
		}
		if rs.approvePlanExplicit {
			// An explicit --approve-plan reasserts the exact subject for this new
			// attempt, so persist a fresh decision under its deterministic ID.
			if err := rs.ratifyDeferredGates("local-user", "approve"); err != nil {
				return err
			}
			return recordApproval("hash_flag", nil)
		}
		// A previously resolved approval belongs to an earlier attempt. Do not
		// transfer it automatically: fall through and request a new decision for
		// this exact attempt and plan.
	}

	// Delivery plan — обычный persisted approval с subject = exact plan hash.
	// Решение можно записать интерактивно, через web decision endpoint или
	// через `--resume --approve-plan <sha256>` (см. resume в RunWithResult).
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
		rs.logSQLiteApprovalRoute(value)
		if _, sqliteStore := rs.approvalStore.(*approval.SQLiteStore); !sqliteStore {
			logging.Printf("Решение: ai-team decision --run %s --approval %s --actor <id> --role %s --action approve|reject --subject %s\n",
				rs.runID, value.ID, deliveryApprovalRole, planHash)
		}
		logging.Printf("Для продолжения с явным подтверждением плана: ai-team run --resume %s --approve-plan %s\n",
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
	return recordApproval("resolved_approval", &value)
}

func (rs *runState) persistHashFlagDeliveryApproval(stageName, attemptID, planHash, candidateSHA string, canonical []byte) (approval.PendingApproval, error) {
	value, err := rs.approvalStore.Create(approval.PendingApproval{
		RunID: rs.runID, AttemptID: attemptID,
		FromStage: stageName, ToStage: stageName, Trigger: "delivery_plan",
		SubjectHash: planHash, CandidateSHA256: candidateSHA,
		RequiredRoles: []string{deliveryApprovalRole}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": stageName, "reject": stageName},
		Payload: json.RawMessage(canonical),
	})
	if err != nil {
		return approval.PendingApproval{}, fmt.Errorf("persist hash-flag delivery approval: %w", err)
	}
	if value.Status == approval.StatusPending {
		value, err = rs.approvalStore.Decide(rs.runID, value.ID, approval.Decision{
			ActorID: "local-user", ActorRole: deliveryApprovalRole,
			Action: "approve", SubjectHash: planHash,
		})
		if err != nil {
			return approval.PendingApproval{}, fmt.Errorf("resolve hash-flag delivery approval: %w", err)
		}
	}
	if err := requireResolvedDeliveryApproval(rs.approvalStore, rs.runID, stageName, attemptID, planHash, canonical, candidateSHA); err != nil {
		return approval.PendingApproval{}, fmt.Errorf("hash-flag delivery approval is not a resolved exact-plan approval: %w", err)
	}
	return value, nil
}

func requireResolvedDeliveryApproval(store ApprovalStore, runID, stage, attemptID, planHash string, canonical []byte, candidateSHA string) error {
	if store == nil {
		return errors.New("deferred delivery requires controller approval storage")
	}
	if runID == "" || stage == "" || attemptID == "" || planHash == "" || len(canonical) == 0 {
		return errors.New("deferred delivery approval identity or canonical plan is incomplete")
	}
	payloadHash := sha256.Sum256(canonical)
	if hex.EncodeToString(payloadHash[:]) != planHash {
		return errors.New("deferred delivery canonical plan does not match the expected plan hash")
	}
	approvalID := approval.NewID(runID, attemptID, stage, stage, "delivery_plan", planHash)
	value, err := store.Load(runID, approvalID)
	if err != nil {
		return fmt.Errorf("load controller delivery approval %s: %w", approvalID, err)
	}
	return validateResolvedDeliveryApproval(value, runID, stage, attemptID, planHash, canonical, candidateSHA)
}

func validateResolvedDeliveryApproval(value approval.PendingApproval, runID, stage, attemptID, planHash string, canonical []byte, candidateSHA string) error {
	approvalID := approval.NewID(runID, attemptID, stage, stage, "delivery_plan", planHash)
	approvedPlan, err := delivery.Parse(value.Payload)
	if err != nil {
		return fmt.Errorf("controller delivery approval payload is not a canonical delivery plan: %w", err)
	}
	approvedCanonical, err := approvedPlan.CanonicalJSON()
	if err != nil {
		return fmt.Errorf("canonicalize controller delivery approval payload: %w", err)
	}
	var approvedCompact, expectedCompact bytes.Buffer
	if err := json.Compact(&approvedCompact, value.Payload); err != nil {
		return fmt.Errorf("compact controller delivery approval payload: %w", err)
	}
	if err := json.Compact(&expectedCompact, canonical); err != nil {
		return fmt.Errorf("compact expected canonical delivery plan: %w", err)
	}
	if value.ID != approvalID || value.RunID != runID || value.AttemptID != attemptID ||
		value.FromStage != stage || value.ToStage != stage || value.Trigger != "delivery_plan" ||
		value.SubjectHash != planHash || value.CandidateSHA256 != candidateSHA ||
		value.Targets["approve"] != stage || !containsString(value.Actions, "approve") ||
		!bytes.Equal(approvedCanonical, canonical) ||
		!bytes.Equal(approvedCompact.Bytes(), expectedCompact.Bytes()) {
		return errors.New("controller delivery approval does not match the canonical plan, candidate, and saved run, attempt, stage, and trigger")
	}
	if value.Status != approval.StatusResolved || value.ResolvedAction != "approve" ||
		!containsString(value.RequiredRoles, deliveryApprovalRole) {
		return errors.New("deferred delivery requires a resolved release-manager approval for the exact plan")
	}
	for _, decision := range value.Decisions {
		if decision.ApprovalID == value.ID && decision.ActorID != "" && decision.ActorRole == deliveryApprovalRole &&
			decision.Action == "approve" && decision.SubjectHash == planHash {
			return nil
		}
	}
	return errors.New("deferred delivery approval has no matching release-manager decision")
}

// requireResolvedDeliveryOperationApproval reuses a saved human decision only
// for the same canonical plan and candidate. The approval's originating stage
// attempt must still have a matching manifest from the selected source, which
// is controller-owned for cloud runs.
func requireResolvedDeliveryOperationApproval(store ApprovalStore, source evidence.AttemptManifestSource,
	runDir, runID, stage, planHash string, canonical []byte, candidateSHA, explicitPlanHash string) error {
	if store == nil {
		return errors.New("deferred delivery requires controller approval storage")
	}
	values, err := store.List(runID)
	if err != nil {
		return fmt.Errorf("list saved controller delivery approvals: %w", err)
	}
	var lastErr error
	for _, value := range values {
		if value.AttemptID == "" {
			continue
		}
		if err := validateResolvedDeliveryApproval(value, runID, stage, value.AttemptID, planHash, canonical, candidateSHA); err != nil {
			continue
		}
		explicitMatch := explicitPlanHash != "" && strings.ToLower(strings.TrimSpace(explicitPlanHash)) == planHash
		trustedStore, trusted := store.(approval.TrustedDecisionAuthority)
		if !explicitMatch && (!trusted || !trustedStore.HasAuthenticatedControllerDecision(value)) {
			lastErr = errors.New("deferred delivery requires an authenticated controller decision or the exact current --approve-plan hash")
			continue
		}
		if err := approvalAttemptMatchesStage(source, runDir, runID, value.AttemptID, stage); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("no saved approval belongs to a controller-recorded delivery attempt: %w", lastErr)
	}
	return errors.New("no resolved release-manager approval matches this delivery plan and candidate")
}

func approvalAttemptMatchesStage(source evidence.AttemptManifestSource, runDir, runID, attemptID, stage string) error {
	_, attempt, err := evidence.ReadAttemptManifest(source, runDir, runID, attemptID)
	if err != nil {
		return fmt.Errorf("read saved delivery approval attempt %s: %w", attemptID, err)
	}
	if attempt.RunID != runID || attempt.AttemptID != attemptID || attempt.Stage != stage {
		return fmt.Errorf("saved delivery approval attempt identity mismatch: run=%q attempt=%q stage=%q", attempt.RunID, attempt.AttemptID, attempt.Stage)
	}
	return nil
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
	requirements, configured, requirementErr := rs.templateDeliveryRequirements()
	if requirementErr != nil {
		return requirementErr
	}
	if configured {
		verdictEvidence, evidenceErr := rs.requiredDeliveryVerdictEvidence(*requirements)
		if evidenceErr != nil {
			return evidenceErr
		}
		if preconditions == nil {
			preconditions = make(map[string]delivery.PreconditionEvidence, len(verdictEvidence))
		} else {
			copy := make(map[string]delivery.PreconditionEvidence, len(preconditions)+len(verdictEvidence))
			for name, value := range preconditions {
				copy[name] = value
			}
			preconditions = copy
		}
		for name, value := range verdictEvidence {
			preconditions[name] = value
		}
	}
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
