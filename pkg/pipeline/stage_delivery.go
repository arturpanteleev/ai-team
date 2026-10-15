package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func (rs *runState) stageDeliveryRequest(result notifier.StageResult) (delivery.StageDeliveryRequest, string, error) {
	if result.AttemptID == "" || result.Delivery == nil || result.Delivery.PlanHash == "" {
		return delivery.StageDeliveryRequest{}, "", errors.New("stage delivery requires a completed attempt and prepared plan")
	}
	runDir := rs.evidence.RunDir()
	manifestSHA, _, err := evidence.AttemptManifestDigest(rs.p.attemptManifestSource, runDir, rs.runID, result.AttemptID)
	if err != nil {
		return delivery.StageDeliveryRequest{}, "", fmt.Errorf("stage delivery attempt manifest digest: %w", err)
	}
	runtimeIdentity, err := runtimeIdentityOfRun(runDir)
	if err != nil {
		return delivery.StageDeliveryRequest{}, "", fmt.Errorf("stage delivery runtime identity: %w", err)
	}
	request := delivery.StageDeliveryRequest{
		RunID: rs.runID, AttemptID: result.AttemptID,
		AttemptManifestSHA256: manifestSHA, PlanHash: result.Delivery.PlanHash,
		RuntimeIdentity: runtimeIdentity,
	}
	approvalIdentity, err := stageDeliveryApprovalIdentity(rs, result)
	if err != nil {
		return delivery.StageDeliveryRequest{}, "", err
	}
	return request, approvalIdentity, nil
}

func stageDeliveryApprovalIdentity(rs *runState, result notifier.StageResult) (string, error) {
	events, found, err := rs.readEvidenceEvents()
	if err != nil {
		return "", fmt.Errorf("read stage delivery approval evidence: %w", err)
	}
	if !found {
		return "", errors.New("stage delivery approval evidence is unavailable")
	}
	approved, deferred := false, false
	mode, approvalAttemptID := "", ""
	for _, event := range events {
		if event.AttemptID != result.AttemptID {
			continue
		}
		switch event.Type {
		case "delivery_plan_approved":
			if event.Data["plan_hash"] != result.Delivery.PlanHash || event.Data["operation_attempt_id"] != result.AttemptID {
				continue
			}
			mode, _ = event.Data["mode"].(string)
			approvalAttemptID, _ = event.Data["approval_attempt_id"].(string)
			approved = true
		case "delivery_deferred":
			deferredHash, _ := event.Data["plan_hash"].(string)
			deferred = deferredHash == result.Delivery.PlanHash
		}
	}
	if !approved || !deferred || approvalAttemptID == "" || (mode != "hash_flag" && mode != "resolved_approval") {
		return "", errors.New("stage delivery requires matching approved-plan and deferred-delivery events")
	}
	return approval.NewID(rs.runID, approvalAttemptID, result.Name, result.Name, "delivery_plan", result.Delivery.PlanHash), nil
}

func stageDeliveryTrailers(runID, runtimeIdentity string) []string {
	return []string{
		delivery.TrailerRunID + ": " + runID,
		delivery.TrailerRuntime + ": " + runtimeIdentity,
	}
}

func (rs *runState) executeStageDelivery(ctx context.Context, stageID string, result *notifier.StageResult) error {
	stage, ok := rs.p.templateStage(stageID)
	if !ok || !stageRequiresControllerPRDelivery(stage) {
		return nil
	}
	if result == nil || result.Status != notifier.StatusPassed || result.Err != nil {
		return nil
	}
	request, approvalIdentity, err := rs.stageDeliveryRequest(*result)
	if err != nil {
		return err
	}
	var receipt delivery.StageDeliveryReceipt
	if rs.p.stageDeliveryExecutor != nil {
		timeout := rs.p.cfg.EffectiveDeliveryTimeout()
		stageCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		receipt, err = rs.p.stageDeliveryExecutor.ExecuteStageDelivery(stageCtx, request)
		if err != nil {
			return fmt.Errorf("controller stage delivery: %w", annotateDeliveryFailure(stageCtx, ctx, timeout, err))
		}
	} else {
		// The in-process controller applies the same approval authority as the
		// worker-facing controller path before it reuses a receipt or performs
		// delivery: the exact canonical plan, candidate digest, approving
		// attempt and human/--approve-plan decision are re-validated.
		plan, err := rs.validateLocalStageDeliveryApproval(stageID, request, approvalIdentity)
		if err != nil {
			return err
		}
		if existing, found, readErr := delivery.ReadControllerStageDeliveryReceipt(rs.runCfg.TargetDir, request); readErr != nil {
			return readErr
		} else if found {
			receipt = *existing
		} else {
			result, executeErr := rs.p.delivery.Execute(ctx, delivery.Request{
				TargetDir: rs.sourceDir(), Feature: rs.runCfg.Feature, Plan: plan,
				Trailers: stageDeliveryTrailers(rs.runID, request.RuntimeIdentity),
			})
			if executeErr != nil {
				return fmt.Errorf("controller stage delivery execute: %w", executeErr)
			}
			receipt = delivery.StageDeliveryReceipt{
				RunID: rs.runID, AttemptID: request.AttemptID,
				AttemptManifestSHA256: request.AttemptManifestSHA256,
				PlanHash:              request.PlanHash, RuntimeIdentity: request.RuntimeIdentity,
				ApprovalIdentity: approvalIdentity, Feature: rs.runCfg.Feature,
				CommitSHA: result.CommitSHA, PRURL: result.PRURL, PerformedAt: time.Now().UTC(),
			}
			if err := delivery.WriteControllerStageDeliveryReceipt(rs.runCfg.TargetDir, receipt); err != nil {
				return fmt.Errorf("persist controller stage delivery receipt: %w", err)
			}
			receipt, _, err = readSealedStageReceipt(rs.runCfg.TargetDir, request)
			if err != nil {
				return err
			}
		}
		if _, err := ProjectControllerPRResultPair(rs.runCfg.TargetDir, rs.sourceDir(), rs.p.cfg, rs.runCfg.Feature, stageID, request.PlanHash, receipt.PRURL); err != nil {
			return fmt.Errorf("project controller PR result: %w", err)
		}
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("controller stage delivery receipt: %w", err)
	}
	if receipt.RunID != request.RunID || receipt.AttemptID != request.AttemptID ||
		receipt.AttemptManifestSHA256 != request.AttemptManifestSHA256 || receipt.PlanHash != request.PlanHash ||
		receipt.RuntimeIdentity != request.RuntimeIdentity || receipt.ApprovalIdentity != approvalIdentity ||
		receipt.Feature != rs.runCfg.Feature || strings.TrimSpace(receipt.PRURL) == "" {
		return errors.New("controller stage delivery receipt does not match the current approved attempt")
	}
	path, err := ControllerPRResultPath(rs.sourceDir(), rs.p.cfg, rs.runCfg.Feature, stageID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("controller stage PR result projection is unavailable: %w", err)
	}
	data, err := safeio.ReadRegularFile(path, 8<<20)
	if err != nil || strings.TrimSpace(string(data)) != receipt.PRURL {
		return fmt.Errorf("controller stage PR result projection does not match its receipt: %w", err)
	}
	result.Delivery.PRURL = receipt.PRURL
	result.Outputs = append(result.Outputs, runtime.Artifact{Name: "link", Path: path, Size: info.Size(), ModTime: info.ModTime()})
	rs.stageDeliveryReceipt = &receipt
	return nil
}

// validateLocalStageDeliveryApproval loads the prepared plan for the local
// (in-process) controller stage delivery and validates it with
// ValidateResolvedDeliveryOperationApproval, the authority used by the
// worker-facing controller path. The approval it resolves must be the one the
// run's evidence names for this attempt.
func (rs *runState) validateLocalStageDeliveryApproval(stageID string, request delivery.StageDeliveryRequest, approvalIdentity string) (delivery.Plan, error) {
	plan, found, err := delivery.LoadPreparedPlan(rs.sourceDir(), rs.runCfg.Feature)
	if err != nil || !found {
		return delivery.Plan{}, fmt.Errorf("load prepared stage delivery plan: found=%t: %w", found, err)
	}
	planHash, err := plan.Hash()
	if err != nil || planHash != request.PlanHash || plan.SourceRunID != rs.runID {
		return delivery.Plan{}, errors.Join(errors.New("prepared stage delivery plan identity mismatch"), err)
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return delivery.Plan{}, fmt.Errorf("canonical prepared stage delivery plan: %w", err)
	}
	candidateSHA := plan.VerifiedWorkspaceDigest
	if current, digestErr := checks.WorkspaceDigest(rs.sourceDir()); digestErr != nil || current != candidateSHA {
		return delivery.Plan{}, errors.Join(errors.New("stage delivery workspace differs from the approved candidate digest"), digestErr)
	}
	// The local file store has no authenticated-decision authority, so the
	// validator's "explicit hash" gate is not what authorizes this delivery:
	// with no operator hash the fallback is request.PlanHash, which always
	// matches. The authority is the resolved release-manager approval in the
	// store (exact canonical plan, candidate, attempt and decision, checked by
	// the validator) together with the hash-chained delivery_plan_approved
	// event that stageDeliveryApprovalIdentity required. An operator-supplied
	// hash (--approve-plan) or this process's own authorization still has to
	// equal the plan, so a foreign hash is rejected.
	explicit := rs.p.deliveryApprovalHash
	if explicit == "" {
		explicit = rs.approvedPlanHash
	}
	if explicit == "" {
		explicit = request.PlanHash
	}
	if rs.approvalStore == nil {
		return delivery.Plan{}, errors.New("stage delivery requires controller approval storage")
	}
	// Validate exactly the approval named by the run's evidence. A continuation
	// attempt may hold several valid approvals for the same plan (the earlier
	// awaiting-plan attempt and the continuation), and the validator returns
	// the first one it accepts.
	approvalID, err := ValidateResolvedDeliveryOperationApproval(singleApprovalReader{store: rs.approvalStore, id: approvalIdentity},
		rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, stageID, planHash, canonical, candidateSHA, explicit)
	if err != nil {
		return delivery.Plan{}, fmt.Errorf("stage delivery approval: %w", err)
	}
	if approvalID != approvalIdentity {
		return delivery.Plan{}, errors.New("stage delivery evidence approval identity does not match controller approval")
	}
	return plan, nil
}

// singleApprovalReader exposes one approval ID from a store to
// ValidateResolvedDeliveryOperationApproval.
type singleApprovalReader struct {
	store ApprovalStore
	id    string
}

func (r singleApprovalReader) Load(runID, approvalID string) (approval.PendingApproval, error) {
	if approvalID != r.id {
		return approval.PendingApproval{}, fmt.Errorf("approval %s is not the evidence-named stage delivery approval", approvalID)
	}
	return r.store.Load(runID, approvalID)
}

func (r singleApprovalReader) List(runID string) ([]approval.PendingApproval, error) {
	values, err := r.store.List(runID)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if value.ID == r.id {
			return []approval.PendingApproval{value}, nil
		}
	}
	return nil, nil
}

// stageRequiresControllerPRDelivery opts a stage into pre-review delivery only
// when its hard result contract requires a controller-created PR. Other
// delivery stages retain the terminal-delivery behavior and do not publish a
// PR before their graph reaches the existing delivery point.
func stageRequiresControllerPRDelivery(stage config.TemplateStage) bool {
	if stage.Delivery == nil || stage.Result != "link" || stage.LinkKind != "pr" || stage.Executor != "agent" {
		return false
	}
	for _, rule := range stage.CodeRules() {
		if rule == "pr_exists" {
			return true
		}
	}
	return false
}

// prepareControllerPRInput replays the most recent active PR stage receipt and
// exposes its link to later graph stages as an ordinary controller-owned
// input. This also restores the input when a run resumes directly at review or
// QA after the implementation attempt was already published.
func (rs *runState) prepareControllerPRInput(ctx context.Context, targetStage string) error {
	targetIndex := rs.graph.Index(targetStage)
	if targetIndex < 0 {
		return nil
	}
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.Superseded || result.Delivery == nil || result.Delivery.PlanHash == "" {
			continue
		}
		stage, ok := rs.p.templateStage(result.Name)
		if !ok || !stageRequiresControllerPRDelivery(stage) {
			continue
		}
		if rs.graph.Index(result.Name) >= targetIndex {
			continue
		}
		if err := rs.executeStageDelivery(ctx, result.Name, &result); err != nil {
			return fmt.Errorf("restore controller PR result from %s for %s: %w", result.Name, targetStage, err)
		}
		if result.Delivery == nil || strings.TrimSpace(result.Delivery.PRURL) == "" {
			return errors.New("controller PR stage receipt did not provide a URL")
		}
		path, err := ControllerPRResultPath(rs.sourceDir(), rs.p.cfg, rs.runCfg.Feature, result.Name)
		if err != nil {
			return err
		}
		if err := validateExistingArtifactPath(rs.task.ArtifactRoot, path); err != nil {
			return fmt.Errorf("controller PR result input is unsafe: %w", err)
		}
		input := runtime.Artifact{Name: "pull-request", Path: path, Size: 0}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("controller PR result input is unavailable: %w", err)
		}
		input.Size, input.ModTime = info.Size(), info.ModTime()
		for _, existing := range rs.extraInputs[targetStage] {
			if existing.Name == input.Name && filepath.Clean(existing.Path) == filepath.Clean(input.Path) {
				rs.results[index] = result
				return nil
			}
		}
		rs.extraInputs[targetStage] = append(rs.extraInputs[targetStage], input)
		rs.results[index] = result
		return nil
	}
	return nil
}

func readSealedStageReceipt(target string, request delivery.StageDeliveryRequest) (delivery.StageDeliveryReceipt, bool, error) {
	receipt, found, err := delivery.ReadControllerStageDeliveryReceipt(target, request)
	if err != nil || !found {
		return delivery.StageDeliveryReceipt{}, found, err
	}
	return *receipt, true, nil
}

func ControllerPRResultPath(targetDir string, cfg *config.Config, feature, stageID string) (string, error) {
	if cfg == nil || !workflow.ValidFeature(feature) {
		return "", errors.New("controller PR result config is unavailable")
	}
	for _, stage := range cfg.Stages {
		if stage.ID == stageID && stage.Delivery != nil && stage.Result == "link" && stage.LinkKind == "pr" {
			return filepath.Join(targetDir, ".ai-team", "artifacts", feature, "stages", stageID, "result.link"), nil
		}
	}
	return "", fmt.Errorf("stage %s is not a configured PR result stage", stageID)
}

// deliveryApprovalContinuation reports whether this attempt resumes an agent
// delivery stage that previously stopped only to wait for approval of its exact
// delivery plan. Such an attempt must not run the model again: the approved
// subject is the prepared plan over the candidate bytes the earlier attempt
// produced. The continuation re-runs controller checks, rebuilds the plan from
// that prepared state and asks authorizeDelivery for the exact hash, so a
// mismatched or missing approval still stops without side effects.
func (rs *runState) deliveryApprovalContinuation(name string, a *agent.Agent) (bool, error) {
	stage, ok := rs.p.templateStage(name)
	if !ok || stage.Delivery == nil || a == nil || a.Kind == "delivery" || rs.approvalStore == nil {
		return false, nil
	}
	previousAttempt := ""
	for index := len(rs.results) - 1; index >= 0; index-- {
		if rs.results[index].Name == name {
			// A replayed attempt carries the stop as its skipped status.
			stopped := rs.results[index].ControlStopped || rs.results[index].Status == notifier.StatusSkipped
			if !stopped || rs.results[index].Superseded {
				return false, nil
			}
			previousAttempt = rs.results[index].AttemptID
			break
		}
	}
	if previousAttempt == "" {
		return false, nil
	}
	plan, found, err := delivery.LoadPreparedPlan(rs.sourceDir(), rs.runCfg.Feature)
	if err != nil || !found {
		return false, err
	}
	planHash, err := plan.Hash()
	if err != nil {
		return false, err
	}
	if plan.SourceRunID != rs.runID {
		return false, nil
	}
	values, err := rs.approvalStore.List(rs.runID)
	if err != nil {
		return false, fmt.Errorf("list delivery approvals for continuation: %w", err)
	}
	requested := false
	for _, value := range values {
		if value.AttemptID != previousAttempt || value.FromStage != name || value.ToStage != name ||
			value.Trigger != "delivery_plan" || value.SubjectHash != planHash {
			continue
		}
		if value.Status == approval.StatusResolved && value.ResolvedAction != "approve" {
			return false, nil
		}
		requested = true
	}
	if !requested {
		return false, nil
	}
	workspaceDigest, err := checks.WorkspaceDigest(rs.sourceDir())
	if err != nil {
		return false, err
	}
	if workspaceDigest != plan.VerifiedWorkspaceDigest {
		return false, nil
	}
	return true, nil
}
