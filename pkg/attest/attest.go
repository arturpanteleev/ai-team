// Package attest строит in-toto compatible attestation statement (V0-3),
// связывающий candidate-subject с run evidence: run, spec (resolved
// workflow/config snapshots), checks, mutations, approvals, verdicts и
// provenance manifest. Canonical JSON, golden fixtures и строгий парсинг
// (DisallowUnknownFields + schema/predicate version) — часть контракта.
package attest

import (
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

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
)

const (
	// StatementType — обязательное поле in-toto Statement v0.1.
	StatementType = "https://in-toto.io/Statement/v0.1"
	// PredicateTypeV1 — версионированный predicate для run-атестаций.
	PredicateTypeV1 = "https://ai-team.dev/attestation/run/v1"
	// PredicateSchemaVersion — семантическая версия структуры predicate.
	PredicateSchemaVersion = 1
)

// Subject — in-toto subject: идентифицируемая цель (candidate workspace).
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"` // {"sha256": "..."}
}

// Statement — in-toto compatible envelope.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

// Predicate связывает candidate с run/spec/checks/mutations/approvals/
// verdicts/provenance. Все поля стабильно упорядочены (canonical JSON).
type Predicate struct {
	SchemaVersion int                  `json:"schema_version"`
	RunID         string               `json:"run_id"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at"`
	Outcome       string               `json:"outcome"`
	Run           RunSummary           `json:"run"`
	Spec          SpecSummary          `json:"spec"`
	Checks        []CheckSummary       `json:"checks,omitempty"`
	Mutations     []MutationSummary    `json:"mutations,omitempty"`
	Approvals     []ApprovalSummary    `json:"approvals,omitempty"`
	Verdicts      []VerdictSummary     `json:"verdicts,omitempty"`
	Provenance    *provenance.Manifest `json:"provenance,omitempty"`
}

// RunSummary — идентичность run evidence.
type RunSummary struct {
	EvidenceSchemaVersion   int    `json:"evidence_schema_version"`
	ConfigEvidence          string `json:"config_evidence"`
	ConfigSHA256            string `json:"config_sha256"`
	EventLogSHA256          string `json:"event_log_sha256"`
	ControllerExecutableSHA string `json:"controller_executable_sha256,omitempty"`
	AttemptCount            int    `json:"attempt_count"`
}

// SpecSummary — какой именно резолвнутый workflow/spec исполнялся.
type SpecSummary struct {
	ResolvedWorkflowEvidence string `json:"resolved_workflow_evidence"`
	ResolvedWorkflowSHA256   string `json:"resolved_workflow_sha256"`
}

// CheckSummary — один required/optional check одной попытки.
type CheckSummary struct {
	Stage     string `json:"stage"`
	AttemptID string `json:"attempt_id,omitempty"`
	Name      string `json:"name"`
	Class     string `json:"class"`
	Policy    string `json:"policy"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Reason    string `json:"reason,omitempty"`
}

// MutationSummary — attributed mutation change (V0-1 typed).
type MutationSummary struct {
	AttemptID string `json:"attempt_id"`
	Stage     string `json:"stage"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Class     string `json:"class"`
}

// ApprovalSummary — lifecycle approval + решения (per-gate decisions).
type ApprovalSummary struct {
	ID              string            `json:"id"`
	Trigger         string            `json:"trigger"`
	FromStage       string            `json:"from_stage"`
	ToStage         string            `json:"to_stage"`
	SubjectHash     string            `json:"subject_hash"`
	CandidateSHA256 string            `json:"candidate_sha256,omitempty"`
	Deferred        bool              `json:"deferred,omitempty"`
	Status          string            `json:"status"`
	ResolvedAction  string            `json:"resolved_action,omitempty"`
	Decisions       []DecisionSummary `json:"decisions,omitempty"`
}

// DecisionSummary — человеческое решение внутри approval.
type DecisionSummary struct {
	ActorRole   string    `json:"actor_role"`
	Action      string    `json:"action"`
	SubjectHash string    `json:"subject_hash"`
	DecidedAt   time.Time `json:"decided_at"`
}

// VerdictSummary — вердикт попытки (что заявил agent и чем закончилось).
type VerdictSummary struct {
	AttemptID string `json:"attempt_id"`
	Stage     string `json:"stage"`
	Verdict   string `json:"verdict,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Status    string `json:"status"`
}

// Build опций сборки statement из immutable run evidence.
type Options struct {
	RunDir           string
	RunID            string
	FinishedAt       time.Time
	Outcome          string
	CandidateSubject []Subject // пусто — subject не резолвится
	Approvals        []approval.PendingApproval
}

// Build читает run evidence из runDir (run.json, workflow/config, events.jsonl,
// attempt manifests) и строит canonical statement. Ошибка = evidence не
// интерпретируется (fail-closed).
func Build(opt Options) (*Statement, error) {
	manifestData, err := os.ReadFile(filepath.Join(opt.RunDir, "run.json"))
	if err != nil {
		return nil, fmt.Errorf("attestation run manifest: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifestData)))
	decoder.DisallowUnknownFields()
	var run evidence.RunManifest
	if err := decoder.Decode(&run); err != nil {
		return nil, fmt.Errorf("attestation run manifest decode: %w", err)
	}
	if run.RunID != opt.RunID || run.SchemaVersion != evidence.SchemaVersion {
		return nil, fmt.Errorf("attestation: run manifest identity mismatch")
	}

	eventLog, err := os.ReadFile(filepath.Join(opt.RunDir, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("attestation events: %w", err)
	}
	eventLogSha := sha256.Sum256(eventLog)

	spec := SpecSummary{
		ResolvedWorkflowEvidence: run.ResolvedWorkflow,
		ResolvedWorkflowSHA256:   run.ResolvedWorkflowSHA256,
	}

	predicate := Predicate{
		SchemaVersion: PredicateSchemaVersion,
		RunID:         opt.RunID,
		StartedAt:     run.StartedAt,
		FinishedAt:    opt.FinishedAt,
		Outcome:       opt.Outcome,
		Run: RunSummary{
			EvidenceSchemaVersion:   run.SchemaVersion,
			ConfigEvidence:          run.ConfigEvidence,
			ConfigSHA256:            run.ConfigSHA256,
			EventLogSHA256:          hex.EncodeToString(eventLogSha[:]),
			ControllerExecutableSHA: run.Controller.ExecutableSHA256,
		},
		Spec: spec,
	}

	if len(run.Provenance) > 0 {
		var prov provenance.Manifest
		if err := json.Unmarshal(run.Provenance, &prov); err != nil {
			return nil, fmt.Errorf("attestation provenance: %w", err)
		}
		if prov.SchemaVersion != provenance.SchemaVersion {
			return nil, fmt.Errorf("attestation: provenance schema mismatch")
		}
		predicate.Provenance = &prov
	}

	attemptDirs, err := os.ReadDir(filepath.Join(opt.RunDir, "attempts"))
	if err != nil {
		return nil, fmt.Errorf("attestation attempts: %w", err)
	}
	attemptIDs := make([]string, 0, len(attemptDirs))
	for _, entry := range attemptDirs {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			attemptIDs = append(attemptIDs, entry.Name())
		}
	}
	sort.Strings(attemptIDs)
	predicate.Run.AttemptCount = len(attemptIDs)

	for _, attemptID := range attemptIDs {
		data, readErr := os.ReadFile(filepath.Join(opt.RunDir, "attempts", attemptID, "manifest.json"))
		if readErr != nil {
			return nil, fmt.Errorf("attestation attempt %s: %w", attemptID, readErr)
		}
		var attempt evidence.AttemptManifest
		if json.Unmarshal(data, &attempt) != nil {
			return nil, fmt.Errorf("attestation attempt manifest %s повреждён", attemptID)
		}
		for _, check := range attempt.Checks {
			predicate.Checks = append(predicate.Checks, CheckSummary{
				Stage: attempt.Stage, AttemptID: attempt.AttemptID, Name: check.Name,
				Class: check.Class, Policy: check.Policy, Status: check.Status,
				ExitCode: check.ExitCode, Reason: check.Reason,
			})
		}
		for _, change := range attempt.MutationChanges {
			predicate.Mutations = append(predicate.Mutations, MutationSummary{
				AttemptID: attempt.AttemptID, Stage: attempt.Stage,
				Path: change.Path, Kind: change.Kind, Class: string(change.Class),
			})
		}
		predicate.Verdicts = append(predicate.Verdicts, VerdictSummary{
			AttemptID: attempt.AttemptID, Stage: attempt.Stage,
			Verdict: attempt.Verdict, Outcome: attempt.Outcome, Status: attempt.Status,
		})
	}

	for _, pending := range opt.Approvals {
		summary := ApprovalSummary{
			ID: pending.ID, Trigger: pending.Trigger,
			FromStage: pending.FromStage, ToStage: pending.ToStage,
			SubjectHash: pending.SubjectHash, CandidateSHA256: pending.CandidateSHA256,
			Deferred: pending.Deferred, Status: string(pending.Status),
			ResolvedAction: pending.ResolvedAction,
		}
		for _, decision := range pending.Decisions {
			summary.Decisions = append(summary.Decisions, DecisionSummary{
				ActorRole: decision.ActorRole, Action: decision.Action,
				SubjectHash: decision.SubjectHash, DecidedAt: decision.DecidedAt,
			})
		}
		predicate.Approvals = append(predicate.Approvals, summary)
	}

	return &Statement{
		Type:          StatementType,
		Subject:       append([]Subject(nil), opt.CandidateSubject...),
		PredicateType: PredicateTypeV1,
		Predicate:     predicate,
	}, nil
}

// Serialize возвращает canonical JSON (детерминированный байт-порядок).
func Serialize(statement *Statement) ([]byte, error) {
	return json.MarshalIndent(statement, "", "  ")
}

// Digest возвращает sha256 canonical-сериализации statement.
func Digest(statement *Statement) (string, error) {
	data, err := Serialize(statement)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Parse — строгий парсинг: неизвестные поля и version/type mismatch — ошибки.
func Parse(data []byte) (*Statement, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var statement Statement
	if err := decoder.Decode(&statement); err != nil {
		return nil, fmt.Errorf("attestation statement decode: %w", err)
	}
	if statement.Type != StatementType {
		return nil, fmt.Errorf("attestation: неожиданный _type %q", statement.Type)
	}
	if statement.PredicateType != PredicateTypeV1 {
		return nil, fmt.Errorf("attestation: неожиданный predicateType %q", statement.PredicateType)
	}
	if statement.Predicate.SchemaVersion != PredicateSchemaVersion {
		return nil, fmt.Errorf("attestation: неподдерживаемая predicate schema %d",
			statement.Predicate.SchemaVersion)
	}
	if statement.Predicate.RunID == "" {
		return nil, errors.New("attestation: пустой run_id")
	}
	return &statement, nil
}
