package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/attest"
	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/delivery"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

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
func (rs *runState) executeDeferredDelivery() error {
	if rs.deferredDelivery == nil {
		return nil
	}
	ctx := context.Background() // run ctx может быть завершён; delivery-операция нужна до конца
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

	attestationDigest, err := attestationDigestOfRun(rs.evidence.RunDir())
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
		return fmt.Errorf("deferred delivery: controller.Execute: %w", err)
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
	if err := delivery.WriteTerminalRecord(rs.evidence.RunDir(), record); err != nil {
		return fmt.Errorf("deferred delivery: запись terminal record: %w", err)
	}
	fmt.Printf("\n%s delivery по run %s: commit=%s pr=%s\n",
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
func (p *Pipeline) DeliverDeferred(runDir, feature, targetDir string) (delivery.TerminalRecord, error) {
	if _, err := safeio.ExistingDir(runDir); err != nil {
		return delivery.TerminalRecord{}, err
	}
	runID := filepath.Base(runDir)
	if runID == "." || runID == string(filepath.Separator) {
		return delivery.TerminalRecord{}, errors.New("deliver: недопустимый run dir")
	}
	if _, ok, err := delivery.ReadTerminalRecord(runDir); err != nil {
		return delivery.TerminalRecord{}, err
	} else if ok {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: run %s уже доставлен (delivery.json существует)", runID)
	}

	// evidence.Resume не применим: он открывает только НЕ-терминальные run
	// (терминальный отклоняется «already terminal»), а DeliverDeferred работает
	// именно с терминальными. Читаем манифест терминального run напрямую из
	// run.json (паттерн export.readRunManifest), сверяя identity/schema.
	manifest, err := readRunManifest(runDir)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: replayed evidence: %w", err)
	}
	terminalStatus, err := terminalStatusOfRun(runDir, runID)
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

	marker, err := firstDeferredMarker(runDir)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	if marker.PlanHash == "" || (marker.Feature != "" && marker.Feature != feature) {
		return delivery.TerminalRecord{}, errors.New("deliver: delivery_deferred маркер не согласован с run")
	}
	// AUD-05: prepared plan и delivery выполняются в workspace, записанном в
	// state_path (для Git-run — candidate worktree), а не в указанном CLI
	// control target. Без state_path retry fail-closed: невозможно выбрать
	// верный workspace.
	workspace, err := deliveryWorkspaceFromStatePath(marker.StatePath, feature)
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	// workspace должен лежать внутри control target (или совпадать с ним),
	// иначе cmdDeliver был вызван для чуждого репозитория. Пути канонизируем:
	// state_path может быть записан уже через символическую ссылку
	// (на macOS /var → /private/var), а CLI target — нет.
	workspaceCanon, targetCanon := canonicalPath(workspace), canonicalPath(targetDir)
	rel, relErr := filepath.Rel(targetCanon, workspaceCanon)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace %s вне control target %s", workspace, targetDir)
	}

	lock, err := evidence.AcquireWorkspaceLock(workspace)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace lock: %w", err)
	}
	defer lock.Close()

	// AUD-05: retry обязан исполнять доставку в тот же candidate, который был
	// attestated в terminal finalize; проверив digest workspace — fail-closed
	// при реконфигурации ворктри между finalize и повторной доставкой.
	if candidateDigest, found, digestErr := candidateWorkspaceDigestOfRun(runDir); digestErr != nil {
		return delivery.TerminalRecord{}, digestErr
	} else if found {
		currentDigest, wsErr := checks.WorkspaceDigest(workspace)
		if wsErr != nil {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace digest: %w", wsErr)
		}
		if currentDigest != candidateDigest {
			return delivery.TerminalRecord{}, fmt.Errorf("deliver: workspace %s не совпадает с attested candidate %s", currentDigest, candidateDigest)
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

	plan, found, err := delivery.LoadPreparedPlan(workspace, feature)
	if err != nil {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: prepared plan: %w", err)
	}
	if !found {
		return delivery.TerminalRecord{}, errors.New("deliver: prepared plan отсутствует — delivery state не найден")
	}
	planHash, err := plan.Hash()
	if err != nil {
		return delivery.TerminalRecord{}, err
	}
	if planHash != marker.PlanHash {
		return delivery.TerminalRecord{}, fmt.Errorf("deliver: plan hash mismatch: marker=%s prepared=%s", marker.PlanHash, planHash)
	}

	trailers := []string{
		delivery.TrailerRunID + ": " + runID,
		delivery.TrailerRuntime + ": " + runtimeIdentity,
		delivery.TrailerAttestation + ": " + attestationDigest,
	}
	result, err := p.delivery.Execute(context.Background(), delivery.Request{
		TargetDir: workspace, Feature: feature, Plan: plan, Trailers: trailers,
	})
	if err != nil {
		return delivery.TerminalRecord{}, err
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
	if err := delivery.WriteTerminalRecord(runDir, record); err != nil {
		return delivery.TerminalRecord{}, err
	}
	return record, nil
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
	data, readErr := safeio.ReadRegularFile(filepath.Join(runDir, "attestation.json"), 1<<20)
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
}

func firstDeferredMarker(runDir string) (deferredMarkerEvent, error) {
	events, err := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), filepath.Base(runDir))
	if err != nil {
		return deferredMarkerEvent{}, fmt.Errorf("deliver: event log: %w", err)
	}
	for _, event := range events {
		if event.Type != "delivery_deferred" {
			continue
		}
		data, err := json.Marshal(event.Data)
		if err != nil {
			continue
		}
		var marker deferredMarkerEvent
		if err := json.Unmarshal(data, &marker); err == nil && marker.PlanHash != "" {
			return marker, nil
		}
	}
	return deferredMarkerEvent{}, errors.New("deliver: delivery_deferred event не найден")
}

func terminalStatusOfRun(runDir, runID string) (string, error) {
	events, err := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), runID)
	if err != nil {
		return "", fmt.Errorf("deliver: event log: %w", err)
	}
	for _, event := range events {
		if event.Type != "run_finished" {
			continue
		}
		status, _ := event.Data["status"].(string)
		if status != "" {
			return status, nil
		}
	}
	return "", errors.New("deliver: run_finished event не найден — run не терминальный")
}

func attestationDigestOfRun(runDir string) (string, error) {
	data, err := safeio.ReadRegularFile(filepath.Join(runDir, "attestation.json"), 1<<20)
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
