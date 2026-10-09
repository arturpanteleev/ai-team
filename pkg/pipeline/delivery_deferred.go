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
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

var errDeferredMarkerNotFound = errors.New("delivery_deferred event не найден")

// delivery_deferred.go (V0-9): string post-terminal доставка. Git-история
// (commit, push, PR) меняется ТОЛЬКО после terminal finalize — когда
// attestation digest и runtime identity детерминированы. Трейлеры commit
// (run ID, runtime identity, attestation digest) собираются из evidence, а не
// из состояния рантайма, поэтому не зависят от харнесс-детерминизма.

// executeDeferredDelivery — вызывается строго после финального finalize при
// outcome Completed. Работает от реального контроллера (rs.p.delivery) и
// workspace lock'а (мы всё ещё внутри RunWithResult). plan перегружается из
// подготовленного delivery state и сверяется с утверждённым (approvedPlanHash)
// и с deferred-маркером стадии.
// QS-06: parent — контекст процесса (signal.NotifyContext в cmd/ai-team), а НЕ
// budgetCtx run'а. Доставка уже одобрена человеком и исполняется после
// терминального finalize, поэтому исчерпанный max_wall_time не должен её
// обрывать; но Ctrl-C обязан, и собственный delivery_timeout — тоже.
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
	trailers := []string{
		delivery.TrailerRunID + ": " + rs.runID,
		delivery.TrailerRuntime + ": " + runtimeIdentity,
		delivery.TrailerAttestation + ": " + attestationDigest,
	}

	result, err := rs.p.delivery.Execute(ctx, delivery.Request{
		TargetDir: rs.sourceDir(), Feature: rs.runCfg.Feature, Plan: plan, Trailers: trailers,
	})
	if err != nil {
		return fmt.Errorf("deferred delivery: controller.Execute: %w", annotateDeliveryFailure(ctx, parent, timeout, err))
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
	logging.Printf("\n%s delivery по run %s: commit=%s pr=%s\n",
		ui.Colorize("✓", ui.ColorGreen), rs.runID, result.CommitSHA, result.PRURL)
	return nil
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
	if err := requireResolvedDeliveryOperationApproval(approvalStore, approvalAttempts, runDir,
		runID, marker.Stage, planHash, canonical, candidateSHA, p.deliveryApprovalHash); err != nil {
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
		return *existing, nil
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
		delivery.TrailerRunID:       runID,
		delivery.TrailerRuntime:     runtimeIdentity,
		delivery.TrailerAttestation: attestationDigest,
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
