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
	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

var errDeferredMarkerNotFound = errors.New("delivery_deferred event не найден")

// ControllerStageDeliveryInput is the controller-only authority bundle for
// producing the PR result before later review stages run. The request carries
// identity only; every identity and the delivery effect are re-derived from
// controller-owned events, attempt manifests, run metadata and approvals.
type ControllerStageDeliveryInput struct {
	TargetDir        string
	RunDir           string
	Request          delivery.StageDeliveryRequest
	EventLog         evidence.EventLog
	AttemptManifests evidence.AttemptManifestSource
	Approvals        DeliveryApprovalReader
	ApprovedPlanHash string
	Service          delivery.Service
}

// ExecuteControllerStageDelivery performs an idempotent, controller-owned
// delivery for one completed agent attempt and projects its PR URL as a stage
// result. The attempt manifest remains immutable; terminal attestation and its
// run-level record are produced later by finalize.
func ExecuteControllerStageDelivery(ctx context.Context, input ControllerStageDeliveryInput) (delivery.StageDeliveryReceipt, error) {
	request := input.Request
	if input.Service == nil || input.Approvals == nil || input.EventLog == nil || input.AttemptManifests == nil {
		return delivery.StageDeliveryReceipt{}, errors.New("controller stage delivery authority is incomplete")
	}
	if err := evidence.ValidateRunID(request.RunID); err != nil {
		return delivery.StageDeliveryReceipt{}, err
	}
	target, err := candidate.CanonicalTargetDir(input.TargetDir)
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("stage delivery target: %w", err)
	}
	runDir := filepath.Join(target, ".ai-team", "runs", request.RunID)
	if filepath.Clean(input.RunDir) != runDir {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery run directory differs from controller target and run identity")
	}
	manifestDigest, _, err := evidence.AttemptManifestDigest(input.AttemptManifests, runDir, request.RunID, request.AttemptID)
	if err != nil || manifestDigest != request.AttemptManifestSHA256 {
		return delivery.StageDeliveryReceipt{}, errors.Join(errors.New("stage delivery attempt manifest digest mismatch"), err)
	}
	_, attempt, err := evidence.ReadAttemptManifest(input.AttemptManifests, runDir, request.RunID, request.AttemptID)
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("read controller stage delivery attempt: %w", err)
	}
	if attempt.RunID != request.RunID || attempt.AttemptID != request.AttemptID || attempt.Executor != "agent" ||
		attempt.Status != string(workflow.OutcomePassed) || attempt.Outcome != string(workflow.OutcomePassed) ||
		attempt.Delivery == nil || attempt.Delivery.PlanHash != request.PlanHash {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery attempt is not a passed agent attempt for the approved plan")
	}
	runtimeIdentity, err := runtimeIdentityOfRun(runDir)
	if err != nil || runtimeIdentity != request.RuntimeIdentity {
		return delivery.StageDeliveryReceipt{}, errors.Join(errors.New("stage delivery runtime identity mismatch"), err)
	}
	replayed, err := evidence.ReplayEventLogWithEventSourcesAndTarget(filepath.Join(runDir, "events.jsonl"), request.RunID,
		input.EventLog, input.AttemptManifests, "")
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("replay controller stage delivery evidence: %w", err)
	}
	var replayedAttempt *evidence.ReplayedAttempt
	for index := range replayed.Attempts {
		if replayed.Attempts[index].AttemptID == request.AttemptID {
			replayedAttempt = &replayed.Attempts[index]
			break
		}
	}
	if replayedAttempt == nil || replayedAttempt.Superseded || replayedAttempt.Stage != attempt.Stage ||
		replayedAttempt.ManifestSHA256 != request.AttemptManifestSHA256 || replayedAttempt.State.Outcome != workflow.OutcomePassed {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery attempt is not the active, replay-verified passed attempt")
	}
	runManifest, err := readRunManifest(runDir)
	if err != nil || runManifest.RunID != request.RunID || runManifest.Feature == "" {
		return delivery.StageDeliveryReceipt{}, errors.Join(errors.New("stage delivery run manifest identity mismatch"), err)
	}
	cfg, err := pinnedDeliveryConfig(runDir, runManifest)
	if err != nil || cfg == nil {
		return delivery.StageDeliveryReceipt{}, errors.Join(errors.New("stage delivery pinned config is unavailable or invalid"), err)
	}
	var stage *config.TemplateStage
	for index := range cfg.Stages {
		if cfg.Stages[index].ID == attempt.Stage {
			stage = &cfg.Stages[index]
			break
		}
	}
	if stage == nil || !stageRequiresControllerPRDelivery(*stage) {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery attempt is not a configured agent PR result stage")
	}
	if attempt.Delivery.StatePath == "" {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery attempt does not name its prepared state")
	}
	marker, approvalAttemptID, err := stageDeliveryEvidenceForAttempt(runDir, request, attempt.Stage, runManifest.Feature, input.EventLog)
	if err != nil {
		return delivery.StageDeliveryReceipt{}, err
	}
	workspace, plan, canonical, planHash, err := preparedDeferredPlan(target, runManifest.Feature, marker)
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("stage delivery prepared plan: %w", err)
	}
	if planHash != request.PlanHash || plan.SourceRunID != request.RunID ||
		filepath.Clean(filepath.FromSlash(marker.StatePath)) != filepath.Clean(attempt.Delivery.StatePath) {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery prepared plan differs from the immutable attempt")
	}
	candidateSHA := plan.VerifiedWorkspaceDigest
	if currentWorkspaceSHA, digestErr := checks.WorkspaceDigest(workspace); digestErr != nil || currentWorkspaceSHA != candidateSHA {
		return delivery.StageDeliveryReceipt{}, errors.Join(errors.New("stage delivery workspace differs from the approved candidate digest"), digestErr)
	}
	approvalID, err := ValidateResolvedDeliveryOperationApproval(input.Approvals, input.AttemptManifests,
		runDir, request.RunID, attempt.Stage, planHash, canonical, candidateSHA, input.ApprovedPlanHash)
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("stage delivery approval: %w", err)
	}
	if approvalID != approval.NewID(request.RunID, approvalAttemptID, attempt.Stage, attempt.Stage, "delivery_plan", planHash) {
		return delivery.StageDeliveryReceipt{}, errors.New("stage delivery evidence approval identity does not match controller approval")
	}
	lock, err := evidence.AcquireWorkspaceLock(workspace)
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("stage delivery workspace lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	validateReceipt := func(receipt delivery.StageDeliveryReceipt) error {
		if err := receipt.Validate(); err != nil {
			return err
		}
		if receipt.RunID != request.RunID || receipt.AttemptID != request.AttemptID ||
			receipt.AttemptManifestSHA256 != request.AttemptManifestSHA256 || receipt.PlanHash != request.PlanHash ||
			receipt.RuntimeIdentity != request.RuntimeIdentity || receipt.ApprovalIdentity != approvalID || receipt.Feature != runManifest.Feature {
			return errors.New("controller stage delivery receipt identity mismatch")
		}
		return nil
	}
	if existing, found, readErr := delivery.ReadControllerStageDeliveryReceipt(target, request); readErr != nil {
		return delivery.StageDeliveryReceipt{}, readErr
	} else if found {
		if err := validateReceipt(*existing); err != nil {
			return delivery.StageDeliveryReceipt{}, err
		}
		if _, err := ProjectControllerPRResultPair(target, workspace, cfg, runManifest.Feature, attempt.Stage, request.PlanHash, existing.PRURL); err != nil {
			return delivery.StageDeliveryReceipt{}, fmt.Errorf("repair controller stage PR projection: %w", err)
		}
		return *existing, nil
	}
	result, err := input.Service.Execute(ctx, delivery.Request{
		TargetDir: workspace, Feature: runManifest.Feature, Plan: plan,
		Trailers: stageDeliveryTrailers(request.RunID, request.RuntimeIdentity),
	})
	if err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("execute controller stage delivery: %w", err)
	}
	if result.PlanHash != request.PlanHash || strings.TrimSpace(result.PRURL) == "" {
		return delivery.StageDeliveryReceipt{}, errors.New("delivery service returned an unbound plan or empty PR URL")
	}
	receipt, err := delivery.SealStageDeliveryReceipt(delivery.StageDeliveryReceipt{
		RunID: request.RunID, AttemptID: request.AttemptID,
		AttemptManifestSHA256: request.AttemptManifestSHA256, PlanHash: request.PlanHash,
		RuntimeIdentity: request.RuntimeIdentity, ApprovalIdentity: approvalID,
		Feature: runManifest.Feature, CommitSHA: result.CommitSHA, PRURL: result.PRURL, PerformedAt: time.Now().UTC(),
	})
	if err != nil {
		return delivery.StageDeliveryReceipt{}, err
	}
	if err := delivery.WriteControllerStageDeliveryReceipt(target, receipt); err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("persist controller stage delivery receipt: %w", err)
	}
	if _, err := ProjectControllerPRResultPair(target, workspace, cfg, runManifest.Feature, attempt.Stage, request.PlanHash, receipt.PRURL); err != nil {
		return delivery.StageDeliveryReceipt{}, fmt.Errorf("project controller stage PR result: %w", err)
	}
	return receipt, nil
}

func stageDeliveryEvidenceForAttempt(runDir string, request delivery.StageDeliveryRequest, stage, feature string, source evidence.EventLog) (deferredMarkerEvent, string, error) {
	events, err := evidence.VerifyEventLogWithSource(filepath.Join(runDir, "events.jsonl"), request.RunID, source)
	if err != nil {
		return deferredMarkerEvent{}, "", fmt.Errorf("read stage delivery event chain: %w", err)
	}
	var approvalAttemptID string
	var approved, deferred bool
	var marker deferredMarkerEvent
	for _, event := range events {
		if event.AttemptID != request.AttemptID {
			continue
		}
		switch event.Type {
		case "delivery_plan_approved":
			planHash, _ := event.Data["plan_hash"].(string)
			operationAttemptID, _ := event.Data["operation_attempt_id"].(string)
			mode, _ := event.Data["mode"].(string)
			if planHash != request.PlanHash || operationAttemptID != request.AttemptID ||
				(event.Stage != "" && event.Stage != stage) || (mode != "hash_flag" && mode != "resolved_approval") {
				continue
			}
			approvalAttemptID, _ = event.Data["approval_attempt_id"].(string)
			approved = approvalAttemptID != ""
		case "delivery_deferred":
			planHash, _ := event.Data["plan_hash"].(string)
			if planHash != request.PlanHash || (event.Stage != "" && event.Stage != stage) {
				continue
			}
			data, marshalErr := json.Marshal(event.Data)
			if marshalErr != nil {
				return deferredMarkerEvent{}, "", marshalErr
			}
			if err := json.Unmarshal(data, &marker); err != nil {
				return deferredMarkerEvent{}, "", err
			}
			marker.Stage, marker.AttemptID = stage, request.AttemptID
			deferred = marker.Feature == "" || marker.Feature == feature
		}
	}
	if !approved || !deferred || marker.PlanHash != request.PlanHash || marker.StatePath == "" {
		return deferredMarkerEvent{}, "", errors.New("stage delivery requires matching approved-plan and deferred-delivery events for the attempt")
	}
	return marker, approvalAttemptID, nil
}

// delivery_deferred.go (V0-9/B-35): terminal run-level delivery record.
// Для configured PR stage сам commit/push/PR может произойти до review, чтобы
// существующие hard PR checks могли проверить результат. Этот hook сохраняет
// отдельную terminal attestation и ссылается на immutable controller receipt;
// для прочих delivery он исполняет side effect после finalize.

// executeDeferredDelivery — вызывается строго после финального finalize при
// outcome Completed. Работает от реального контроллера (rs.p.delivery) и
// workspace lock'а (мы всё ещё внутри RunWithResult). plan перегружается из
// подготовленного delivery state и сверяется с утверждённым (approvedPlanHash)
// и с deferred-маркером стадии.
// QS-06: parent — контекст процесса (signal.NotifyContext в cmd/ai-team), а НЕ
// budgetCtx run'а. Доставка уже одобрена человеком и исполняется после
// терминального finalize, поэтому исчерпанный max_execution_time не должен
// её обрывать; но Ctrl-C обязан, и собственный delivery_timeout — тоже.
func (rs *runState) executeDeferredDelivery(parent context.Context) error {
	if rs.deferredDelivery == nil {
		return nil
	}
	timeout := rs.p.cfg.EffectiveDeliveryTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	plan, found, planErr := delivery.LoadPreparedPlan(rs.sourceDir(), rs.runCfg.Feature)
	if planErr != nil {
		return fmt.Errorf("deferred delivery: загрузка prepared plan: %w", planErr)
	}
	if !found {
		return errors.New("deferred delivery: prepared plan отсутствует — delivery state не найден")
	}
	planHash, hashErr := plan.Hash()
	if hashErr != nil {
		return fmt.Errorf("deferred delivery: plan hash: %w", hashErr)
	}
	if planHash != rs.deferredDelivery.PlanHash {
		return fmt.Errorf("deferred delivery: prepared plan не совпадает с маркером стадии (%s != %s)",
			planHash, rs.deferredDelivery.PlanHash)
	}
	if rs.approvedPlanHash != "" && rs.approvedPlanHash != planHash {
		return fmt.Errorf("deferred delivery: утверждённый plan %s не совпадает с approved %s",
			planHash, rs.approvedPlanHash)
	}

	attestationDigest, err := rs.deferredAttestationDigest()
	if err != nil {
		return err
	}
	runtimeIdentity, err := runtimeIdentityOfRun(rs.evidence.RunDir())
	if err != nil {
		return err
	}
	var result delivery.Result
	trailers := []string{
		delivery.TrailerRunID + ": " + rs.runID,
		delivery.TrailerRuntime + ": " + runtimeIdentity,
		delivery.TrailerAttestation + ": " + attestationDigest,
	}
	var stageReceipt *delivery.StageDeliveryReceipt
	if rs.deferredDelivery.StageID != "" {
		stageReceipt, err = rs.ensureStageDeliveryReceipt(ctx)
		if err != nil {
			return fmt.Errorf("deferred delivery: recover stage receipt: %w", err)
		}
	}
	if stageReceipt != nil {
		if err := stageReceipt.Validate(); err != nil {
			return fmt.Errorf("deferred delivery: stage receipt: %w", err)
		}
		if stageReceipt.RunID != rs.runID || stageReceipt.AttemptID != rs.deferredDelivery.AttemptID ||
			stageReceipt.PlanHash != planHash || stageReceipt.RuntimeIdentity != runtimeIdentity || stageReceipt.Feature != rs.runCfg.Feature {
			return errors.New("deferred delivery: stage receipt does not match terminal run identity")
		}
		result = delivery.Result{PlanHash: planHash, StatePath: stageReceiptStatePath(rs.sourceDir(), rs.runCfg.Feature), CommitSHA: stageReceipt.CommitSHA, PRURL: stageReceipt.PRURL}
		trailers = stageDeliveryTrailers(rs.runID, runtimeIdentity)
	} else {
		result, err = rs.p.delivery.Execute(ctx, delivery.Request{
			TargetDir: rs.sourceDir(), Feature: rs.runCfg.Feature, Plan: plan, Trailers: trailers,
		})
		if err != nil {
			return fmt.Errorf("deferred delivery: controller.Execute: %w", annotateDeliveryFailure(ctx, parent, timeout, err))
		}
	}

	record := delivery.TerminalRecord{
		SchemaVersion:     delivery.TerminalRecordSchemaVersion,
		RunID:             rs.runID,
		Feature:           rs.runCfg.Feature,
		PlanHash:          planHash,
		CommitSHA:         result.CommitSHA,
		PRURL:             result.PRURL,
		Trailers:          trailers,
		AttestationSHA256: attestationDigest,
		RuntimeIdentity:   runtimeIdentity,
		PerformedAt:       time.Now().UTC(),
	}
	if stageReceipt != nil {
		record.StageAttemptID = stageReceipt.AttemptID
		record.StageAttemptManifestSHA256 = stageReceipt.AttemptManifestSHA256
		record.StageDeliveryReceiptSHA256 = stageReceipt.ReceiptSHA256
		if rs.p.terminalRecordWriter == nil {
			if err := ValidateStageDeliveryTerminalRecord(rs.runCfg.TargetDir, rs.evidence.RunDir(), record, rs.p.eventLogSource); err != nil {
				return fmt.Errorf("deferred delivery: validate stage terminal record: %w", err)
			}
		}
	}
	var writeErr error
	if rs.p.terminalRecordWriter != nil {
		writeErr = rs.p.terminalRecordWriter.WriteTerminalRecord(record)
	} else {
		// A direct pipeline invocation has observed Execute in this controller
		// process, so it can issue the retry proof. Sandboxed workers only submit
		// a TerminalRecord through their scoped API and do not get this authority.
		if receiptErr := delivery.WriteControllerDeliveryReceipt(rs.runCfg.TargetDir, record); receiptErr != nil {
			return fmt.Errorf("deferred delivery: controller receipt: %w", receiptErr)
		}
		writeErr = delivery.WriteTerminalRecord(rs.evidence.RunDir(), record)
	}
	if writeErr != nil {
		return fmt.Errorf("deferred delivery: запись terminal record: %w", writeErr)
	}
	if err := delivery.WriteTerminalRecord(rs.evidence.RunDir(), record); err != nil {
		return fmt.Errorf("deferred delivery: run-local terminal record: %w", err)
	}
	if err := rs.evidence.SealTerminalEvidence(); err != nil {
		return fmt.Errorf("deferred delivery: reseal terminal evidence: %w", err)
	}
	if err := rs.publishDeferredDeliveryResult(record); err != nil {
		return fmt.Errorf("deferred delivery: publish stage PR result: %w", err)
	}
	logging.Printf("\n%s delivery по run %s: commit=%s pr=%s\n",
		ui.Colorize("✓", ui.ColorGreen), rs.runID, result.CommitSHA, result.PRURL)
	return nil
}

func (rs *runState) ensureStageDeliveryReceipt(ctx context.Context) (*delivery.StageDeliveryReceipt, error) {
	if rs.stageDeliveryReceipt != nil {
		return rs.stageDeliveryReceipt, nil
	}
	deferred := rs.deferredDelivery
	if deferred == nil || deferred.StageID == "" || deferred.AttemptID == "" {
		return nil, nil
	}
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.AttemptID != deferred.AttemptID || result.Name != deferred.StageID || result.Delivery == nil {
			continue
		}
		if result.Delivery.PlanHash != deferred.PlanHash {
			return nil, errors.New("stage result plan hash differs from deferred-delivery marker")
		}
		if err := rs.executeStageDelivery(ctx, deferred.StageID, &result); err != nil {
			return nil, err
		}
		if rs.stageDeliveryReceipt != nil {
			return rs.stageDeliveryReceipt, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("stage delivery attempt %s is missing from run results", deferred.AttemptID)
}

func stageReceiptStatePath(workspace, feature string) string {
	return filepath.Join(workspace, ".ai-team", "delivery", feature+".json")
}

// publishDeferredDeliveryResult refreshes the configured stage-result path
// from the validated terminal record. Stage delivery may have projected the
// same value before review; this repair remains outside the attempt manifest.
func (rs *runState) publishDeferredDeliveryResult(record delivery.TerminalRecord) error {
	deferred := rs.deferredDelivery
	if deferred == nil || deferred.StageID == "" {
		return nil
	}
	stage, ok := rs.p.templateStage(deferred.StageID)
	if !ok || stage.Delivery == nil || stage.Result != "link" || stage.LinkKind != "pr" {
		return fmt.Errorf("delivery result stage %s is not a configured PR delivery stage", deferred.StageID)
	}
	if record.RunID != rs.runID || record.Feature != rs.runCfg.Feature ||
		record.PlanHash != deferred.PlanHash || strings.TrimSpace(record.PRURL) == "" {
		return errors.New("terminal delivery record does not match the prepared stage result")
	}
	var resultIndex = -1
	for index := len(rs.results) - 1; index >= 0; index-- {
		if rs.results[index].AttemptID == deferred.AttemptID && rs.results[index].Name == deferred.StageID {
			resultIndex = index
			break
		}
	}
	if resultIndex < 0 {
		return fmt.Errorf("delivery result attempt %s for stage %s is missing", deferred.AttemptID, deferred.StageID)
	}
	result := rs.results[resultIndex]
	if result.Delivery == nil || result.Delivery.PlanHash != record.PlanHash {
		return errors.New("stage result does not reference the terminal delivery plan")
	}
	path, err := writeDeferredPRResultLink(rs.runCfg.TargetDir, rs.p.cfg, rs.runCfg.Feature, stage.ID, record)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	result.Delivery.PRURL = record.PRURL
	result.Outputs = append(result.Outputs, runtime.Artifact{
		Name: "link", Path: path, Size: info.Size(), ModTime: info.ModTime(),
	})
	rs.results[resultIndex] = result
	// The attempt report was sealed during terminal finalize; this stage-result link is a separate read model.
	return nil
}

// writeDeferredPRResultLink publishes the terminal PR URL as a controller-owned
// task result. It is an idempotent read model outside the attempt manifest: a
// retry may atomically repair a missing or partial file from the validated
// terminal record without changing the attested attempt.
func writeDeferredPRResultLink(targetDir string, cfg *config.Config, feature, stageID string, record delivery.TerminalRecord) (string, error) {
	if cfg == nil {
		return "", errors.New("deferred delivery result: pinned config is unavailable")
	}
	var stage *config.TemplateStage
	for index := range cfg.Stages {
		if cfg.Stages[index].ID == stageID {
			stage = &cfg.Stages[index]
			break
		}
	}
	if stage == nil || stage.Delivery == nil || stage.Result != "link" || stage.LinkKind != "pr" {
		return "", fmt.Errorf("delivery result stage %s is not a configured PR delivery stage", stageID)
	}
	if !workflow.ValidFeature(feature) || strings.TrimSpace(record.PRURL) == "" {
		return "", errors.New("deferred delivery result: feature or PR URL is invalid")
	}
	artifactRoot := filepath.Join(targetDir, ".ai-team", "artifacts")
	relative := filepath.Join(feature, "stages", stage.ID, "result.link")
	path, err := confinedArtifactPath(artifactRoot, relative)
	if err != nil {
		return "", err
	}
	content := []byte(strings.TrimSpace(record.PRURL) + "\n")
	if err := safeio.ReplaceRegularFileNoFollow(path, content, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// ProjectControllerPRResult writes the controller-derived PR link for a
// configured stage. It is a separate task result projection and never changes
// the immutable attempt manifest or attested workspace.
func ProjectControllerPRResult(targetDir string, cfg *config.Config, feature, stageID, planHash, prURL string) (string, error) {
	return writeDeferredPRResultLink(targetDir, cfg, feature, stageID, delivery.TerminalRecord{
		Feature: feature, PlanHash: planHash, PRURL: prURL,
	})
}

// ProjectControllerPRResultPair keeps the project read model and the isolated
// candidate input in sync. The target copy is for UI/report consumers; the
// workspace copy is an explicit runtime input for review and QA stages.
func ProjectControllerPRResultPair(targetDir, workspaceDir string, cfg *config.Config, feature, stageID, planHash, prURL string) (string, error) {
	targetPath, err := ProjectControllerPRResult(targetDir, cfg, feature, stageID, planHash, prURL)
	if err != nil {
		return "", err
	}
	if filepath.Clean(targetDir) == filepath.Clean(workspaceDir) {
		return targetPath, nil
	}
	if _, err := ProjectControllerPRResult(workspaceDir, cfg, feature, stageID, planHash, prURL); err != nil {
		return "", err
	}
	return targetPath, nil
}

// DeliverDeferred — standalone-повтор отложенной доставки (retry/CLI) по
// завершённому evidence. Используется когда post-terminal хук упал (можно
// перезапустить `ai-team deliver --run <id> --target <repo>`); enforcement
// детерминированный: результат привязан к plan, зафиксированному в
// delivery_deferred event, и к terminal статусу run.
//
// AUD-05: рабочий каталог (workspace) разрешается из проверенной run metadata —
// state_path из delivery_deferred event указывает на candidate worktree
// Git-run'а, а не на control target, который передаёт CLI.
// targetDir остаётся только контрольным корнем для sanity-проверки.
// Дополнительно сверяется identity workspace с attested candidate digest из
// attestation.json, а сам retry берёт workspace lock, чтобы конкурентные
// повторы не выполнили доставку дважды.
//
// QS-06: parent — контекст процесса (сигналы), поверх него накладывается
// собственный delivery_timeout. Contract тот же, что у post-terminal хука:
// зависший git/gh обрывается дедлайном, Ctrl-C — сигналом.
func (p *Pipeline) DeliverDeferred(parent context.Context, runDir, feature, targetDir string) (delivery.TerminalRecord, error) {
	if _, err := safeio.ExistingDir(runDir); err != nil {
		return delivery.TerminalRecord{}, err
	}
	runID := filepath.Base(runDir)
	if runID == "." || runID == string(filepath.Separator) {
		return delivery.TerminalRecord{}, errors.New("deliver: недопустимый run dir")
	}
	if err := p.validateControllerBackedLegacyDelivery(targetDir, runDir, runID); err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: validate controller delivery authority: %w", err)
	}

	// evidence.Resume не применим: он открывает только НЕ-терминальные run
	// (терминальный отклоняется «already terminal»), а DeliverDeferred работает
	// именно с терминальными. Читаем манифест терминального run напрямую из
	// run.json (паттерн export.readRunManifest), сверяя identity/schema.
	manifest, err := readRunManifest(runDir)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: replayed evidence: %w", err)
	}
	terminalStatus, err := terminalStatusOfRun(runDir, runID, p.eventLogSource)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	switch terminalStatus {
	case string(workflow.RunCompleted), string(workflow.RunCompletedWithWarnings):
	default:
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: run %s завершился как %q, доставка не разрешена", runID, terminalStatus)
	}
	if feature == "" {
		feature = manifest.Feature
	}
	if feature == "" {
		return delivery.TerminalRecord{}, errors.New("deliver: feature не определён")
	}

	marker, err := firstDeferredMarker(runDir, p.eventLogSource)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	marker, err = deferredMarkerWithAttemptStage(p.attemptManifestSource, runDir, runID, marker)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	if marker.PlanHash == "" || (marker.Feature != "" && marker.Feature != feature) {
		return delivery.TerminalRecord{}, errors.New("deliver: delivery_deferred маркер не согласован с run")
	}
	workspace, plan, _, planHash, err := preparedDeferredPlan(targetDir, feature, marker)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}

	lock, err := evidence.AcquireWorkspaceLock(workspace)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace lock: %w", err)
	}
	defer func() { _ = lock.Close() }() // снятие файловой блокировки: значимый результат уже посчитан.

	// AUD-05: retry обязан исполнять доставку в тот же candidate, который был
	// attestated в terminal finalize; проверив digest workspace — fail-closed
	// при реконфигурации ворктри между finalize и повторной доставкой.
	candidateSHA, candidateFound, digestErr := candidateWorkspaceDigestOfRun(runDir)
	if digestErr != nil {
		return delivery.TerminalRecord{}, digestErr
	} else if candidateFound {
		currentDigest, wsErr := checks.WorkspaceDigest(workspace)
		if wsErr != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace digest: %w", wsErr)
		}
		if currentDigest != candidateSHA {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace %s не совпадает с attested candidate %s", currentDigest, candidateSHA)
		}
	}

	attestationDigest, err := attestationDigestOfRun(runDir)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	runtimeIdentity, err := runtimeIdentityOfRun(runDir)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}

	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: canonical prepared plan: %w", err)
	}
	approvalStore := p.approvals
	if approvalStore == nil {
		approvalStore, err = approval.NewStore(targetDir)
		if err != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: controller approval store: %w", err)
		}
	}
	approvalAttempts := evidence.ReservedAttemptManifestSource{TargetDir: targetDir}
	approvalID, err := ValidateResolvedDeliveryOperationApproval(approvalStore, approvalAttempts, runDir,
		runID, marker.Stage, planHash, canonical, candidateSHA, p.deliveryApprovalHash)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: %w", err)
	}
	// A successful retry is idempotent. Do this after the exact current plan,
	// candidate, terminal run, and workspace lock have been validated, so a
	// changed plan cannot borrow the previous run's approval or PR record.
	if existing, found, readErr := delivery.ReadControllerDeliveryReceipt(targetDir, runID); readErr != nil {
		return delivery.TerminalRecord{}, readErr
	} else if found {
		if err := validateRecoveredTerminalRecord(runDir, runID, feature, marker, *existing, p.eventLogSource); err != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: existing controller receipt mismatch: %w", err)
		}
		if err := publishDeferredPRResultFromRun(targetDir, runDir, manifest, marker, *existing); err != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: restore stage PR result: %w", err)
		}
		return *existing, nil
	}
	manifestDigest, _, err := evidence.AttemptManifestDigest(approvalAttempts, runDir, runID, marker.AttemptID)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: stage receipt attempt manifest digest: %w", err)
	}
	stageRequest := delivery.StageDeliveryRequest{
		RunID: runID, AttemptID: marker.AttemptID, AttemptManifestSHA256: manifestDigest,
		PlanHash: planHash, RuntimeIdentity: runtimeIdentity,
	}
	if stageReceipt, found, readErr := delivery.ReadControllerStageDeliveryReceipt(targetDir, stageRequest); readErr != nil {
		return delivery.TerminalRecord{}, readErr
	} else if found {
		if stageReceipt.ApprovalIdentity != approvalID || stageReceipt.Feature != feature {
			return delivery.TerminalRecord{}, errors.New("deliver: stage receipt approval or feature identity mismatch")
		}
		record := delivery.TerminalRecord{
			SchemaVersion: delivery.TerminalRecordSchemaVersion, RunID: runID, Feature: feature,
			PlanHash: planHash, CommitSHA: stageReceipt.CommitSHA, PRURL: stageReceipt.PRURL,
			Trailers:          stageDeliveryTrailers(runID, runtimeIdentity),
			AttestationSHA256: attestationDigest, RuntimeIdentity: runtimeIdentity,
			StageAttemptID: stageReceipt.AttemptID, StageAttemptManifestSHA256: stageReceipt.AttemptManifestSHA256,
			StageDeliveryReceiptSHA256: stageReceipt.ReceiptSHA256, PerformedAt: time.Now().UTC(),
		}
		if err := ValidateStageDeliveryTerminalRecord(targetDir, runDir, record, p.eventLogSource); err != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: validate stage receipt for terminal record: %w", err)
		}
		if err := delivery.WriteControllerDeliveryReceipt(targetDir, record); err != nil {
			return delivery.TerminalRecord{}, err
		}
		stored, found, readErr := delivery.ReadControllerDeliveryReceipt(targetDir, runID)
		if readErr != nil || !found {
			return delivery.TerminalRecord{}, errors.Join(errors.New("deliver: controller terminal receipt did not persist"), readErr)
		}
		record = *stored
		if err := delivery.WriteControllerTerminalRecord(targetDir, runID, record); err != nil {
			logging.Printf("⚠ delivery compatibility controller record for run %s: %v", runID, err)
		}
		if err := delivery.WriteTerminalRecord(runDir, record); err != nil {
			logging.Printf("⚠ delivery compatibility mirror for run %s: %v", runID, err)
		}
		if err := evidence.ResealTerminalEvidence(runDir, p.eventLogSource); err != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: reseal terminal evidence: %w", err)
		}
		if err := publishDeferredPRResultFromRun(targetDir, runDir, manifest, marker, record); err != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: publish stage PR result: %w", err)
		}
		return record, nil
	}

	trailers := []string{
		delivery.TrailerRunID + ": " + runID,
		delivery.TrailerRuntime + ": " + runtimeIdentity,
		delivery.TrailerAttestation + ": " + attestationDigest,
	}
	timeout := p.cfg.EffectiveDeliveryTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, err := p.delivery.Execute(ctx, delivery.Request{
		TargetDir: workspace, Feature: feature, Plan: plan, Trailers: trailers,
	})
	if err != nil {
		return delivery.TerminalRecord{}, annotateDeliveryFailure(ctx, parent, timeout, err)
	}
	record := delivery.TerminalRecord{
		SchemaVersion:     delivery.TerminalRecordSchemaVersion,
		RunID:             runID,
		Feature:           feature,
		PlanHash:          planHash,
		CommitSHA:         result.CommitSHA,
		PRURL:             result.PRURL,
		Trailers:          trailers,
		AttestationSHA256: attestationDigest,
		RuntimeIdentity:   runtimeIdentity,
		PerformedAt:       time.Now().UTC(),
	}
	// Persist the controller-only receipt first. Worker-submitted terminal
	// records use a separate API and cannot satisfy the idempotent shortcut.
	if err := delivery.WriteControllerDeliveryReceipt(targetDir, record); err != nil {
		return delivery.TerminalRecord{}, err
	}
	stored, found, err := delivery.ReadControllerDeliveryReceipt(targetDir, runID)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	if !found {
		return delivery.TerminalRecord{}, errors.New("deliver: controller delivery receipt disappeared after write")
	}
	record = *stored
	// Keep the worker-API compatibility record and historical run-local copy for
	// existing readers. Neither mirror is accepted as proof for retry
	// idempotency; a conflicting worker-submitted record cannot replace the
	// controller receipt or undo the successful delivery.
	if err := delivery.WriteControllerTerminalRecord(targetDir, runID, record); err != nil {
		logging.Printf("⚠ delivery compatibility controller record for run %s: %v", runID, err)
	}
	if err := delivery.WriteTerminalRecord(runDir, record); err != nil {
		logging.Printf("⚠ delivery compatibility mirror for run %s: %v", runID, err)
	}
	if err := evidence.ResealTerminalEvidence(runDir, p.eventLogSource); err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: reseal terminal evidence: %w", err)
	}
	if err := publishDeferredPRResultFromRun(targetDir, runDir, manifest, marker, record); err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: publish stage PR result: %w", err)
	}
	return record, nil
}

// ControllerBackedLegacyDeliveryRequiresApproval identifies pre-canonical
// cloud runs that still use a worker-visible event journal. Controller
// candidate-admission proofs (from #212) and usage reservations (from #214)
// also classify older cloud runs. Lifecycle rows are intentionally excluded
// because local runs write them too. Markerless runs from before those proofs
// remain indistinguishable from local runs and are outside this guard's
// classification.
func ControllerBackedLegacyDeliveryRequiresApproval(targetDir, runID string) (bool, error) {
	target, err := filepath.Abs(targetDir)
	if err != nil {
		return false, err
	}
	if canonical, evalErr := filepath.EvalSymlinks(target); evalErr == nil {
		target = canonical
	} else if !errors.Is(evalErr, os.ErrNotExist) {
		return false, evalErr
	}
	target = filepath.Clean(target)

	eventStore := evidence.ControllerEventStore{TargetDir: target}
	eventReserved, err := eventStore.IsReserved(runID)
	if err != nil {
		legacyCandidate, eligibilityErr := eventStore.IsLegacyMigrationCandidate(runID)
		if eligibilityErr != nil {
			return false, errors.Join(fmt.Errorf("inspect controller event reservation: %w", err), eligibilityErr)
		}
		if !legacyCandidate {
			return false, fmt.Errorf("inspect controller event reservation: %w", err)
		}
	} else if eventReserved {
		return false, nil
	}
	candidateAdmission, err := (candidate.FileMetadataStore{}).HasControllerAdmissionProof(target, runID)
	if err != nil {
		return false, fmt.Errorf("inspect controller candidate admission: %w", err)
	}
	if candidateAdmission {
		return true, nil
	}
	manifestReserved, err := (evidence.ControllerAttemptManifestStore{TargetDir: target}).IsReserved(runID)
	if err != nil {
		return false, fmt.Errorf("inspect controller attempt-manifest reservation: %w", err)
	}
	usageReserved, err := metrics.UsageEnvelopeReservation(target, runID)
	if err != nil {
		return false, fmt.Errorf("inspect controller usage reservation: %w", err)
	}
	return manifestReserved || usageReserved, nil
}

// validateControllerBackedLegacyDelivery protects the manual retry entry
// point when a cloud worker's old event journal has not yet been reserved in
// the controller event store. A reserved event store is already the authority
// and is checked through the normal source resolver.
func (p *Pipeline) validateControllerBackedLegacyDelivery(targetDir, runDir, runID string) error {
	cloudLegacy, err := ControllerBackedLegacyDeliveryRequiresApproval(targetDir, runID)
	if err != nil {
		return err
	}
	if !cloudLegacy {
		return nil
	}
	if p.approvals == nil {
		if p.deliveryApprovalHash != "" {
			// The later deferred-plan check binds this current confirmation to
			// the exact canonical plan before any delivery service is invoked.
			return nil
		}
		return errors.New("controller approval authority is unavailable for legacy cloud delivery")
	}
	legacyPath := filepath.Join(runDir, "events.jsonl")
	events, err := evidence.VerifyEventLogWithSource(legacyPath, runID, evidence.NewFileEventLog(legacyPath))
	if err != nil {
		return fmt.Errorf("read legacy cloud delivery evidence: %w", err)
	}
	var approvals []approval.PendingApproval
	approvalsLoaded := false
	approved := make(map[string]bool)
	claimKey := func(attemptID, planHash string) string { return attemptID + "\x00" + planHash }
	for _, event := range events {
		switch event.Type {
		case "delivery_plan_approved":
			planHash, _ := event.Data["plan_hash"].(string)
			mode, _ := event.Data["mode"].(string)
			if event.AttemptID == "" || planHash == "" {
				return errors.New("legacy delivery approval event lacks attempt or plan identity")
			}
			switch mode {
			case "hash_flag":
				// The old journal does not preserve a controller-verifiable copy of
				// the one-shot job flag. A current exact --approve-plan can reassert it.
				approved[claimKey(event.AttemptID, planHash)] = p.deliveryApprovalHash == planHash
			case "resolved_approval":
				if !approvalsLoaded {
					approvals, err = p.approvals.List(runID)
					if err != nil {
						return fmt.Errorf("list controller delivery approvals: %w", err)
					}
					approvalsLoaded = true
				}
				matched := false
				for _, value := range approvals {
					if value.RunID == runID && value.Trigger == "delivery_plan" && value.SubjectHash == planHash &&
						value.AttemptID == event.AttemptID && value.Status == approval.StatusResolved && value.ResolvedAction == "approve" {
						trustedStore, trusted := p.approvals.(approval.TrustedDecisionAuthority)
						matched = trusted && trustedStore.HasAuthenticatedControllerDecision(value) || p.deliveryApprovalHash == planHash
						break
					}
				}
				if !matched {
					return fmt.Errorf("legacy resolved delivery claim %s has no exact controller approval", planHash)
				}
				approved[claimKey(event.AttemptID, planHash)] = true
			default:
				return fmt.Errorf("legacy delivery approval mode %q is unsupported", mode)
			}
		case "delivery_deferred":
			planHash, _ := event.Data["plan_hash"].(string)
			if event.AttemptID == "" || planHash == "" || !approved[claimKey(event.AttemptID, planHash)] {
				return errors.New("legacy delivery_deferred has no preceding exact controller approval for the same attempt")
			}
		}
	}
	return nil
}

// ReconcileTerminalDelivery is used by durable worker recovery after
// run_finished. A terminal pipeline outcome does not imply deferred delivery
// finished: only a validated delivery.json closes that obligation.
func (p *Pipeline) ReconcileTerminalDelivery(ctx context.Context, runID, targetDir string) error {
	runDir := filepath.Join(targetDir, ".ai-team", "runs", runID)
	if filepath.Base(runID) != runID || runID == "." || runID == ".." {
		return fmt.Errorf("recover delivery: invalid run id %q", runID)
	}
	record, found, err := delivery.ReadControllerDeliveryReceipt(targetDir, runID)
	if err != nil {
		return fmt.Errorf("recover controller delivery receipt: %w", err)
	}
	marker, markerErr := firstDeferredMarker(runDir, p.eventLogSource)
	if markerErr != nil {
		if found {
			return fmt.Errorf("recover delivery: terminal record exists without a verified delivery_deferred marker: %w", markerErr)
		}
		// A terminal run without a deferred-delivery marker has no delivery
		// obligation. A marker that exists but is malformed is rejected by
		// firstDeferredMarker, including when there is no terminal record yet.
		if errors.Is(markerErr, errDeferredMarkerNotFound) {
			return nil
		}
		return fmt.Errorf("recover delivery evidence: %w", markerErr)
	}
	manifest, err := readRunManifest(runDir)
	if err != nil {
		return fmt.Errorf("recover delivery manifest: %w", err)
	}
	if manifest.RunID != runID {
		return fmt.Errorf("recover delivery: run manifest id %q does not match requested run %q", manifest.RunID, runID)
	}
	if manifest.Feature == "" || (marker.Feature != "" && marker.Feature != manifest.Feature) {
		return errors.New("recover delivery: delivery_deferred marker feature does not match run manifest")
	}
	if marker.PlanHash == "" {
		return errors.New("recover delivery: delivery_deferred marker has no plan hash")
	}
	if p.approvals == nil {
		return errors.New("recover delivery: controller approval store is required for trusted recovery")
	}
	attemptStore := evidence.ControllerAttemptManifestStore{TargetDir: targetDir}
	reserved, err := attemptStore.IsReserved(runID)
	if err != nil {
		return fmt.Errorf("recover controller attempt manifest reservation: %w", err)
	}
	if !reserved {
		return errors.New("recover delivery: trusted recovery requires a controller-reserved attempt manifest")
	}
	_, attempt, err := evidence.ReadAttemptManifest(attemptStore, runDir, runID, marker.AttemptID)
	if err != nil {
		return fmt.Errorf("recover controller delivery attempt: %w", err)
	}
	if marker.Stage != "" && attempt.Stage != marker.Stage {
		return fmt.Errorf("recover delivery: marker stage %q differs from controller attempt stage %q", marker.Stage, attempt.Stage)
	}
	marker.Stage = attempt.Stage
	_, _, canonical, planHash, err := preparedDeferredPlan(targetDir, manifest.Feature, marker)
	if err != nil {
		return fmt.Errorf("recover delivery prepared plan: %w", err)
	}
	candidateSHA, _, err := candidateWorkspaceDigestOfRun(runDir)
	if err != nil {
		return fmt.Errorf("recover delivery candidate identity: %w", err)
	}
	if err := requireResolvedDeliveryOperationApproval(p.approvals, attemptStore, runDir,
		runID, marker.Stage, planHash, canonical, candidateSHA, p.deliveryApprovalHash); err != nil {
		return fmt.Errorf("recover delivery: %w", err)
	}
	if found {
		if err := validateRecoveredTerminalRecord(runDir, runID, manifest.Feature, marker, *record, p.eventLogSource); err != nil {
			return fmt.Errorf("recover delivery record identity mismatch: %w", err)
		}
		if err := evidence.ResealTerminalEvidence(runDir, p.eventLogSource); err != nil {
			return fmt.Errorf("recover delivery anchor: %w", err)
		}
		if err := publishDeferredPRResultFromRun(targetDir, runDir, manifest, marker, *record); err != nil {
			return fmt.Errorf("recover delivery stage result: %w", err)
		}
		return nil
	}
	if _, err := p.DeliverDeferred(ctx, runDir, manifest.Feature, targetDir); err != nil {
		return fmt.Errorf("recover deferred delivery: %w", err)
	}
	return nil
}

func validateRecoveredTerminalRecord(runDir, runID, feature string, marker deferredMarkerEvent, record delivery.TerminalRecord, eventSource evidence.EventLog) error {
	if record.RunID != runID || record.Feature != feature || record.PlanHash != marker.PlanHash {
		return fmt.Errorf("record run/feature/plan (%q, %q, %q) differs from requested run/marker (%q, %q, %q)",
			record.RunID, record.Feature, record.PlanHash, runID, feature, marker.PlanHash)
	}
	if marker.StatePath == "" {
		return errors.New("delivery_deferred marker has no state_path")
	}
	status, err := terminalStatusOfRun(runDir, runID, eventSource)
	if err != nil {
		return err
	}
	if status != string(workflow.RunCompleted) && status != string(workflow.RunCompletedWithWarnings) {
		return fmt.Errorf("run terminal status %q does not permit deferred delivery", status)
	}
	attestationDigest, err := attestationDigestOfRun(runDir)
	if err != nil {
		return err
	}
	runtimeIdentity, err := runtimeIdentityOfRun(runDir)
	if err != nil {
		return err
	}
	if record.AttestationSHA256 != attestationDigest || record.RuntimeIdentity != runtimeIdentity {
		return errors.New("record attestation/runtime identity differs from run evidence")
	}
	if record.PerformedAt.IsZero() {
		return errors.New("record performed_at is empty")
	}
	expected := map[string]string{
		delivery.TrailerRunID:   runID,
		delivery.TrailerRuntime: runtimeIdentity,
	}
	if record.StageDeliveryReceiptSHA256 != "" {
		if err := ValidateStageDeliveryTerminalRecord(filepath.Dir(filepath.Dir(filepath.Dir(runDir))), runDir, record, eventSource); err != nil {
			return fmt.Errorf("record stage delivery receipt: %w", err)
		}
	} else {
		expected[delivery.TrailerAttestation] = attestationDigest
	}
	if len(record.Trailers) != len(expected) {
		return fmt.Errorf("record has %d trailers; expected %d identity trailers", len(record.Trailers), len(expected))
	}
	for _, trailer := range record.Trailers {
		key, value, ok := strings.Cut(trailer, ": ")
		if !ok || expected[key] == "" || value != expected[key] {
			return fmt.Errorf("record trailer %q does not match run evidence", trailer)
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		return fmt.Errorf("record is missing identity trailers: %v", expected)
	}
	return nil
}

// ValidateStageDeliveryTerminalRecord verifies that a terminal attestation
// record refers to the immutable controller receipt that created the PR before
// human review. The late attestation remains a run-level fact and is never
// appended to or used to rewrite the already reviewed commit.
func ValidateStageDeliveryTerminalRecord(targetDir, runDir string, record delivery.TerminalRecord, eventSource evidence.EventLog) error {
	if record.StageDeliveryReceiptSHA256 == "" {
		return errors.New("terminal record has no stage delivery receipt reference")
	}
	request := delivery.StageDeliveryRequest{
		RunID: record.RunID, AttemptID: record.StageAttemptID,
		AttemptManifestSHA256: record.StageAttemptManifestSHA256,
		PlanHash:              record.PlanHash, RuntimeIdentity: record.RuntimeIdentity,
	}
	receipt, found, err := delivery.ReadControllerStageDeliveryReceipt(targetDir, request)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("controller stage delivery receipt is missing")
	}
	if receipt.ReceiptSHA256 != record.StageDeliveryReceiptSHA256 || receipt.Feature != record.Feature ||
		receipt.CommitSHA != record.CommitSHA || receipt.PRURL != record.PRURL {
		return errors.New("terminal record delivery result differs from controller stage receipt")
	}
	runtimeIdentity, err := runtimeIdentityOfRun(runDir)
	if err != nil || runtimeIdentity != record.RuntimeIdentity {
		return errors.Join(errors.New("terminal record runtime identity differs from run evidence"), err)
	}
	attestationDigest, err := attestationDigestOfRun(runDir)
	if err != nil || attestationDigest != record.AttestationSHA256 {
		return errors.Join(errors.New("terminal record attestation differs from run evidence"), err)
	}
	manifest, err := readRunManifest(runDir)
	if err != nil || manifest.RunID != record.RunID || manifest.Feature != record.Feature {
		return errors.Join(errors.New("terminal record run manifest identity mismatch"), err)
	}
	cfg, err := pinnedDeliveryConfig(runDir, manifest)
	if err != nil || cfg == nil {
		return errors.Join(errors.New("terminal record pinned config is unavailable or invalid"), err)
	}
	_, attempt, err := evidence.ReadAttemptManifest(evidence.ReservedAttemptManifestSource{TargetDir: targetDir}, runDir, record.RunID, record.StageAttemptID)
	if err != nil {
		return fmt.Errorf("terminal record delivery attempt: %w", err)
	}
	if attempt.Stage == "" || attempt.Delivery == nil || attempt.Delivery.PlanHash != record.PlanHash ||
		attempt.Status != string(workflow.OutcomePassed) || attempt.Outcome != string(workflow.OutcomePassed) {
		return errors.New("terminal record stage attempt does not match the successful delivered plan")
	}
	marker, approvalAttemptID, err := stageDeliveryEvidenceForAttempt(runDir, request, attempt.Stage, record.Feature, eventSource)
	if err != nil {
		return err
	}
	if marker.PlanHash != record.PlanHash || receipt.ApprovalIdentity != approval.NewID(record.RunID, approvalAttemptID, attempt.Stage, attempt.Stage, "delivery_plan", record.PlanHash) {
		return errors.New("terminal record receipt approval identity differs from run evidence")
	}
	replayed, err := evidence.ReplayEventLogWithEventSourcesAndTarget(filepath.Join(runDir, "events.jsonl"), record.RunID,
		eventSource, evidence.ReservedAttemptManifestSource{TargetDir: targetDir}, "")
	if err != nil {
		return fmt.Errorf("terminal record replay: %w", err)
	}
	active := false
	for _, replayedAttempt := range replayed.Attempts {
		if replayedAttempt.AttemptID == record.StageAttemptID && !replayedAttempt.Superseded &&
			replayedAttempt.ManifestSHA256 == request.AttemptManifestSHA256 && replayedAttempt.State.Outcome == workflow.OutcomePassed {
			active = true
			break
		}
	}
	if !active {
		return errors.New("terminal record stage attempt is not active in strict replay")
	}
	expectedTrailers := stageDeliveryTrailers(record.RunID, record.RuntimeIdentity)
	if !sameStringSlices(record.Trailers, expectedTrailers) {
		return errors.New("terminal record trailers differ from the controller stage delivery")
	}
	return nil
}

func sameStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// annotateDeliveryFailure объясняет, ПОЧЕМУ доставка оборвалась. Без этого
// пользователь видит только «delivery push failed: context canceled» у
// случайного шага и не может отличить зависший remote от нажатого Ctrl-C.
func annotateDeliveryFailure(ctx, parent context.Context, timeout time.Duration, err error) error {
	switch {
	case parent.Err() != nil:
		// Отменён внешний контекст (сигнал процессу) — дедлайн доставки ни при чём.
		return fmt.Errorf("%w (доставка прервана извне: %v)", err, parent.Err())
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%w (превышен delivery_timeout %s: внешняя команда не завершилась)", err, timeout)
	}
	return err
}

// deliveryWorkspaceFromStatePath выделяет корень workspace из абсолютного
// пути delivery state (<workspace>/.ai-team/delivery/<feature>.json) и
// валидирует его форму. Вернёт ошибку при попытке вывести стейт за пределы
// канонической структуры.
func deliveryWorkspaceFromStatePath(statePath, feature string) (string, error) {
	if statePath == "" {
		return "", errors.New("deliver: delivery_deferred маркер не содержит state_path")
	}
	if feature == "" || feature == "." || feature == ".." || strings.ContainsAny(feature, `/\\`) || strings.Contains(feature, "..") {
		return "", fmt.Errorf("deliver: невалидный feature %q", feature)
	}
	path := filepath.Clean(filepath.FromSlash(statePath))
	if !filepath.IsAbs(path) {
		return "", errors.New("deliver: state_path должен быть absolute")
	}
	workspace := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if workspace == "." || workspace == "/" || !filepath.IsAbs(workspace) {
		return "", errors.New("deliver: state_path не содержит candidate workspace")
	}
	expected := filepath.Join(workspace, ".ai-team", "delivery", feature+".json")
	if expected != path {
		return "", fmt.Errorf("deliver: state_path %q не соответствует workspace layout %q", path, expected)
	}
	return workspace, nil
}

// canonicalPath разрешает символические ссылки пути (для сравнения путей из
// разных источников — state_path и CLI target могут отличаться формой одного
// физического каталога, например /var vs /private/var на macOS).
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// candidateWorkspaceDigestOfRun читает attested subject 'candidate' из
// attestation.json (digest sha256). found=false для non-Git run'ов — там
// workspace == control target и digest верифицировать нечем.
func candidateWorkspaceDigestOfRun(runDir string) (digest string, found bool, err error) {
	data, readErr := attestationRecordForRun(runDir)
	if readErr != nil {
		return "", false, fmt.Errorf("deferred delivery: attestation: %w", readErr)
	}
	statement, parseErr := attest.Parse(data)
	if parseErr != nil {
		return "", false, fmt.Errorf("deferred delivery: attestation parse: %w", parseErr)
	}
	for _, subject := range statement.Subject {
		if subject.Name != "candidate" {
			continue
		}
		value, ok := subject.Digest["sha256"]
		if !ok || value == "" {
			return "", false, errors.New("deferred delivery: candidate subject не содержит sha256 digest")
		}
		return value, true, nil
	}
	return "", false, nil
}

// deferredMarkerEvent — доказательство утверждённой delivery-стадии в run.
type deferredMarkerEvent struct {
	PlanHash  string `json:"plan_hash"`
	Feature   string `json:"feature"`
	StatePath string `json:"state_path"`
	Stage     string `json:"-"`
	AttemptID string `json:"-"`
}

func firstDeferredMarker(runDir string, source evidence.EventLog) (deferredMarkerEvent, error) {
	events, err := evidence.VerifyEventLogWithSource(filepath.Join(runDir, "events.jsonl"), filepath.Base(runDir), source)
	if err != nil {
		return deferredMarkerEvent{}, fmt.Errorf("deliver: event log: %w", err)
	}
	for _, event := range events {
		if event.Type != "delivery_deferred" {
			continue
		}
		data, err := json.Marshal(event.Data)
		if err != nil {
			return deferredMarkerEvent{}, fmt.Errorf("deliver: decode delivery_deferred event: %w", err)
		}
		var marker deferredMarkerEvent
		if err := json.Unmarshal(data, &marker); err != nil {
			return deferredMarkerEvent{}, fmt.Errorf("deliver: decode delivery_deferred marker: %w", err)
		}
		marker.Stage, marker.AttemptID = event.Stage, event.AttemptID
		if marker.PlanHash == "" || marker.StatePath == "" || marker.AttemptID == "" {
			return deferredMarkerEvent{}, errors.New("deliver: некорректный delivery_deferred marker: отсутствует plan_hash, state_path или attempt_id")
		}
		return marker, nil
	}
	return deferredMarkerEvent{}, fmt.Errorf("deliver: %w", errDeferredMarkerNotFound)
}

// Older delivery_deferred events carry AttemptID but not Stage. Recover that
// value from the selected attempt-manifest source; cloud recovery supplies the
// reserved controller source, while trusted CLI replay can use legacy evidence.
func deferredMarkerWithAttemptStage(source evidence.AttemptManifestSource, runDir, runID string, marker deferredMarkerEvent) (deferredMarkerEvent, error) {
	if marker.Stage != "" {
		return marker, nil
	}
	_, attempt, err := evidence.ReadAttemptManifest(source, runDir, runID, marker.AttemptID)
	if err != nil {
		return deferredMarkerEvent{}, fmt.Errorf("deliver: resolve marker stage from attempt manifest: %w", err)
	}
	marker.Stage = attempt.Stage
	if marker.Stage == "" {
		return deferredMarkerEvent{}, errors.New("deliver: attempt manifest has no stage for delivery approval")
	}
	return marker, nil
}

func publishDeferredPRResultFromRun(targetDir, runDir string, manifest *evidence.RunManifest, marker deferredMarkerEvent, record delivery.TerminalRecord) error {
	cfg, err := pinnedDeliveryConfig(runDir, manifest)
	if err != nil {
		return err
	}
	if cfg == nil {
		// Older direct-delivery runs may not have a template snapshot. They have
		// no controller stage-result contract to project.
		return nil
	}
	var stage *config.TemplateStage
	for index := range cfg.Stages {
		if cfg.Stages[index].ID == marker.Stage {
			stage = &cfg.Stages[index]
			break
		}
	}
	if stage == nil || stage.Delivery == nil {
		return nil
	}
	if record.RunID != manifest.RunID || record.Feature != manifest.Feature ||
		record.PlanHash != marker.PlanHash || strings.TrimSpace(record.PRURL) == "" {
		return errors.New("terminal record does not match the pinned PR result stage")
	}
	_, err = writeDeferredPRResultLink(targetDir, cfg, manifest.Feature, marker.Stage, record)
	return err
}

func pinnedDeliveryConfig(runDir string, manifest *evidence.RunManifest) (*config.Config, error) {
	if manifest == nil || manifest.ConfigEvidence == "" {
		return nil, nil
	}
	if manifest.ConfigEvidence != "config.json" {
		return nil, errors.New("deferred delivery: unexpected pinned config evidence path")
	}
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, manifest.ConfigEvidence), 8<<20)
	if err != nil {
		return nil, fmt.Errorf("deferred delivery: pinned config snapshot: %w", err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != manifest.ConfigSHA256 {
		return nil, errors.New("deferred delivery: pinned config snapshot digest mismatch")
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("deferred delivery: pinned config parse: %w", err)
	}
	return &cfg, nil
}

// PinnedDeliveryConfig returns the hash-verified process config snapshot from a
// run manifest for trusted controller API consumers.
func PinnedDeliveryConfig(runDir string, manifest *evidence.RunManifest) (*config.Config, error) {
	return pinnedDeliveryConfig(runDir, manifest)
}

// preparedDeferredPlan resolves the plan only from the marker's prepared
// state path and confirms that the workspace belongs to the controller target.
// The returned canonical bytes are the exact payload that the release-manager
// approval must have resolved.
func preparedDeferredPlan(targetDir, feature string, marker deferredMarkerEvent) (string, delivery.Plan, []byte, string, error) {
	workspace, err := deliveryWorkspaceFromStatePath(marker.StatePath, feature)
	if err != nil {
		return "", delivery.Plan{}, nil, "", err
	}
	workspaceCanon, targetCanon := canonicalPath(workspace), canonicalPath(targetDir)
	rel, relErr := filepath.Rel(targetCanon, workspaceCanon)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", delivery.Plan{}, nil, "", fmt.Errorf("deliver: workspace %s вне control target %s", workspace, targetDir)
	}
	plan, found, err := delivery.LoadPreparedPlan(workspace, feature)
	if err != nil {
		return "", delivery.Plan{}, nil, "", fmt.Errorf("deliver: prepared plan: %w", err)
	}
	if !found {
		return "", delivery.Plan{}, nil, "", errors.New("deliver: prepared plan отсутствует — delivery state не найден")
	}
	planHash, err := plan.Hash()
	if err != nil {
		return "", delivery.Plan{}, nil, "", err
	}
	if planHash != marker.PlanHash {
		return "", delivery.Plan{}, nil, "", fmt.Errorf("deliver: plan hash mismatch: marker=%s prepared=%s", marker.PlanHash, planHash)
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return "", delivery.Plan{}, nil, "", fmt.Errorf("deliver: canonical prepared plan: %w", err)
	}
	return workspace, plan, canonical, planHash, nil
}

func terminalStatusOfRun(runDir, runID string, source evidence.EventLog) (string, error) {
	events, err := evidence.VerifyEventLogWithSource(filepath.Join(runDir, "events.jsonl"), runID, source)
	if err != nil {
		return "", fmt.Errorf("deliver: event log: %w", err)
	}
	status := ""
	for _, event := range events {
		if event.Type == "run_canceled" {
			status = string(workflow.RunCanceled)
			continue
		}
		if event.Type != "run_finished" {
			continue
		}
		finishedStatus, _ := event.Data["status"].(string)
		if finishedStatus != "" {
			status = finishedStatus
		}
	}
	if status != "" {
		return status, nil
	}
	return "", errors.New("deliver: run_finished event не найден — run не терминальный")
}

func attestationDigestOfRun(runDir string) (string, error) {
	data, err := attestationRecordForRun(runDir)
	if err != nil {
		return "", fmt.Errorf("deferred delivery: attestation: %w", err)
	}
	statement, err := attest.Parse(data)
	if err != nil {
		return "", fmt.Errorf("deferred delivery: attestation parse: %w", err)
	}
	digest, err := attest.Digest(statement)
	if err != nil {
		return "", fmt.Errorf("deferred delivery: attestation digest: %w", err)
	}
	return digest, nil
}

// deferredAttestationDigest uses the exact statement digest computed during
// finalize. In a bubblewrap child the controller-owned copy is masked and has
// no read API; the in-memory value still binds delivery trailers to that write.
// Host retry/recovery constructs a fresh state and falls back to stored evidence.
func (rs *runState) deferredAttestationDigest() (string, error) {
	if rs.attestationDigest != "" {
		return rs.attestationDigest, nil
	}
	return attestationDigestOfRun(rs.evidence.RunDir())
}

// attestationRecordForRun prefers controller-owned state when present. A
// corrupt controller record is an error and never falls back to worker evidence.
func attestationRecordForRun(runDir string) ([]byte, error) {
	runID := filepath.Base(filepath.Clean(runDir))
	targetDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(runDir))))
	stored, err := (attest.ControllerStore{TargetDir: targetDir}).Read(runID)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("deferred delivery: controller attestation: %w", err)
	}
	legacy, err := safeio.ReadRegularFile(filepath.Join(runDir, "attestation.json"), 1<<20)
	if err != nil {
		return nil, err
	}
	statement, err := attest.Parse(legacy)
	if err != nil {
		return nil, err
	}
	if statement.Predicate.RunID != runID {
		return nil, errors.New("legacy attestation run mismatch")
	}
	return legacy, nil
}

func runtimeIdentityOfRun(runDir string) (string, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), 1<<20)
	if err != nil {
		return "", fmt.Errorf("deferred delivery: run manifest: %w", err)
	}
	var manifest struct {
		Provenance json.RawMessage `json:"provenance,omitempty"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("deferred delivery: run manifest decode: %w", err)
	}
	if len(manifest.Provenance) == 0 || !json.Valid(manifest.Provenance) {
		return "", errors.New("deferred delivery: runtime identity: пустой provenance в run manifest")
	}
	sum := sha256.Sum256(manifest.Provenance)
	return hex.EncodeToString(sum[:]), nil
}

// readRunManifest читает манифест терминального run напрямую из run.json
// (anapek export.readRunManifest), сверяя identity/schema. evidence.Resume
// не подходит: он открывает только НЕ-терминальные run.
func readRunManifest(runDir string) (*evidence.RunManifest, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), 1<<20)
	if err != nil {
		return nil, fmt.Errorf("deferred delivery: run manifest: %w", err)
	}
	var manifest evidence.RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("deferred delivery: run manifest decode: %w", err)
	}
	if manifest.RunID == "" || manifest.SchemaVersion != evidence.SchemaVersion {
		return nil, errors.New("deferred delivery: run manifest identity/schema mismatch")
	}
	return &manifest, nil
}
