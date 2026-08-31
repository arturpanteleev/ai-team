package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// ErrResumeEvidence — сентинель: apply-осведомлённая evidence chain/snapshots
// run'а не прошла fail-closed проверку перед resume (OPS-3).
var ErrResumeEvidence = errors.New("resume evidence verification failed")

// ResumeEvidenceReason — стабильный машинный код причины отказа, чтобы
// rejection reason можно было явно фиксировать и однозначно обрабатывать.
type ResumeEvidenceReason string

const (
	ReasonManifestIdentity ResumeEvidenceReason = "manifest_identity" // run.json schema/run_id mismatch
	ReasonConfigSnapshot   ResumeEvidenceReason = "config_snapshot"   // config digest не совпадает с manifest
	ReasonWorkflowSnapshot ResumeEvidenceReason = "workflow_snapshot" // workflow digest не совпадает с manifest
	ReasonEventChain       ResumeEvidenceReason = "event_chain"       // events.jsonl hash chain / parse сломан
	ReasonAttemptManifest  ResumeEvidenceReason = "attempt_manifest"  // digest attempt manifest не совпадает
	ReasonAlreadyTerminal  ResumeEvidenceReason = "already_terminal"
)

// ResumeEvidenceError детализирует ошибку fail-closed проверки evidence.
type ResumeEvidenceError struct {
	Reason ResumeEvidenceReason
	Detail string
}

// AppendBlockedEvent записывает событие resume_blocked с указанной причиной
// (OPS-3), если event chain run'а аппендабельна (целостна). Возвращает ошибку
// только если лог недоступен или повреждён — тогда cause нефиксируемо, но и
// сам провал уже означает fail-closed отказ resume.
func AppendBlockedEvent(runDir, runID string, reason ResumeEvidenceReason, detail string) error {
	store, err := openStoreForAppend(runDir, runID)
	if err != nil {
		return err
	}
	return store.Append(Event{
		Type: "resume_blocked", Timestamp: time.Now().UTC(),
		Data: map[string]any{"reason": string(reason), "detail": detail},
	})
}

// openStoreForAppend пере-открывает store по валидной event chain, чтобы
// записать событие без повторного прохода всей snapshot-проверки Resume.
func openStoreForAppend(runDir, runID string) (*Store, error) {
	events, err := VerifyEventLog(filepath.Join(runDir, "events.jsonl"), runID)
	if err != nil {
		return nil, fmt.Errorf("blocked event: event chain: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("blocked event: пустой event log")
	}
	return &Store{
		root: filepath.Dir(runDir), runID: runID,
		nextID: uint64(len(events)), lastEventHash: events[len(events)-1].SHA256,
		provenance: make(map[string]ArtifactRecord),
	}, nil
}

func (e *ResumeEvidenceError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s", ErrResumeEvidence, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrResumeEvidence, e.Reason, e.Detail)
}

func (e *ResumeEvidenceError) Unwrap() error { return ErrResumeEvidence }

func resumeErr(reason ResumeEvidenceReason, format string, args ...interface{}) *ResumeEvidenceError {
	return &ResumeEvidenceError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// VerifyResumeEvidence — fail-closed проверка применимой evidence
// chain/snapshots НЕ-терминального run перед его продолжением (OPS-3).
// Выполняется строго детерминированно и возвращает структурированную причину
// (Reasion) при любом расхождении: manifest identity, config/workflow snapshot
// digests, event hash chain, attempt manifest digests. Терминальный run
// отклоняется отдельной причиной.
func VerifyResumeEvidence(runDir string) error {
	manifestData, err := safeio.ReadRegularFile(filepath.Join(runDir, "run.json"), 1<<20)
	if err != nil {
		return resumeErr(ReasonManifestIdentity, "run.json: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	decoder.DisallowUnknownFields()
	var manifest RunManifest
	if err := decoder.Decode(&manifest); err != nil {
		return resumeErr(ReasonManifestIdentity, "run manifest decode: %v", err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.RunID == "" || filepath.Base(manifest.RunID) != manifest.RunID {
		return resumeErr(ReasonManifestIdentity, "schema/run_id mismatch (schema=%d run=%q)", manifest.SchemaVersion, manifest.RunID)
	}

	configData, err := safeio.ReadRegularFile(filepath.Join(runDir, manifest.ConfigEvidence), 8<<20)
	if err != nil || sha256Bytes(configData) != manifest.ConfigSHA256 {
		return resumeErr(ReasonConfigSnapshot, "config snapshot identity mismatch")
	}
	workflowData, err := safeio.ReadRegularFile(filepath.Join(runDir, manifest.ResolvedWorkflow), 8<<20)
	if err != nil || sha256Bytes(workflowData) != manifest.ResolvedWorkflowSHA256 {
		return resumeErr(ReasonWorkflowSnapshot, "resolved workflow snapshot identity mismatch")
	}

	replayed, err := ReplayEventLog(filepath.Join(runDir, "events.jsonl"), manifest.RunID)
	if err != nil {
		return resumeErr(ReasonEventChain, "event chain: %v", err)
	}
	if !replayed.FinishedAt.IsZero() {
		return resumeErr(ReasonAlreadyTerminal, "run %s уже terminal", manifest.RunID)
	}

	for _, attempt := range replayed.Attempts {
		if attempt.ManifestSHA256 == "" {
			continue
		}
		manifestPath := filepath.Join(runDir, "attempts", attempt.AttemptID, "manifest.json")
		artifactType, size, digest, digestErr := ArtifactDigest(manifestPath)
		if digestErr != nil || artifactType != "file" || size == 0 || digest != attempt.ManifestSHA256 {
			return resumeErr(ReasonAttemptManifest, "attempt %s manifest digest mismatch", attempt.AttemptID)
		}
	}
	return nil
}
