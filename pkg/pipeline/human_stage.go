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
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/report"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const humanInputTrigger = "human_input"

type humanInputSubject struct {
	RunID      string                     `json:"run_id"`
	Feature    string                     `json:"feature"`
	StageID    string                     `json:"stage_id"`
	Function   string                     `json:"function"`
	Result     string                     `json:"result"`
	LinkKind   string                     `json:"link_kind,omitempty"`
	OutputName string                     `json:"output_name"`
	OutputPath string                     `json:"output_path"`
	Inputs     []humanInputArtifactDigest `json:"inputs"`
	Candidate  string                     `json:"candidate_sha256,omitempty"`
}

type humanInputArtifactDigest struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type humanApprovalResult struct {
	Kind        string    `json:"kind"`
	StageID     string    `json:"stage_id"`
	Action      string    `json:"action"`
	ActorID     string    `json:"actor_id"`
	ActorRole   string    `json:"actor_role"`
	Comment     string    `json:"comment,omitempty"`
	Description string    `json:"description,omitempty"`
	At          time.Time `json:"at"`
}

func (rs *runState) runHumanStage(ctx context.Context, index int, stageID string) (notifier.StageResult, error) {
	select {
	case <-ctx.Done():
		return notifier.StageResult{}, ctx.Err()
	default:
	}
	stage, ok := rs.p.templateStage(stageID)
	if !ok || stage.Executor != "human" {
		return notifier.StageResult{}, fmt.Errorf("stage %s is not a configured human stage", stageID)
	}
	definition, err := rs.p.loadStageDefinition(stageID)
	if err != nil {
		return notifier.StageResult{}, fmt.Errorf("load human stage contract %s: %w", stageID, err)
	}
	outputName, outputPath, err := humanOutputContract(stage, definition, rs.runCfg.Feature)
	if err != nil {
		return notifier.StageResult{}, err
	}
	inputs, err := rs.humanStageInputs(definition, stageID)
	if err != nil {
		return notifier.StageResult{}, err
	}
	subject, err := rs.humanInputSubject(stage, outputName, outputPath, inputs)
	if err != nil {
		return notifier.StageResult{}, err
	}
	encodedSubject, err := json.Marshal(subject)
	if err != nil {
		return notifier.StageResult{}, err
	}
	subjectDigest := sha256.Sum256(encodedSubject)
	subjectHash := hex.EncodeToString(subjectDigest[:])

	var resolved *approval.PendingApproval
	inputApproval := rs.resumedApproval
	recoveredInput := false
	if inputApproval == nil || inputApproval.Kind != approval.KindInput ||
		inputApproval.Trigger != humanInputTrigger || inputApproval.FromStage != stageID {
		inputApproval = rs.recoveredHumanApproval
		recoveredInput = true
	}
	if inputApproval != nil && inputApproval.Kind == approval.KindInput &&
		inputApproval.Trigger == humanInputTrigger && inputApproval.FromStage == stageID {
		value := *inputApproval
		if recoveredInput {
			rs.recoveredHumanApproval = nil
		} else {
			rs.resumedApproval = nil // consume this exact human input once per invocation.
		}
		if value.SubjectHash != subjectHash || value.ToStage != stageID || value.Status != approval.StatusResolved {
			return notifier.StageResult{}, errors.New("resumed human input does not match the current stage subject")
		}
		var payload approval.InputPayload
		if json.Unmarshal(value.Payload, &payload) != nil || payload.StageID != stageID ||
			payload.Result != stage.Result || payload.OutputName != outputName || payload.OutputPath != outputPath || payload.LinkKind != stage.LinkKind {
			return notifier.StageResult{}, errors.New("resumed human input contract differs from the pinned stage")
		}
		resolved = &value
	} else {
		attemptID := rs.evidence.NewAttemptID(stageID, rs.attemptOrdinal+1)
		payload, payloadErr := json.Marshal(approval.InputPayload{
			Kind: string(approval.KindInput), StageID: stageID, Result: stage.Result,
			LinkKind: stage.LinkKind, OutputName: outputName, OutputPath: outputPath,
		})
		if payloadErr != nil {
			return notifier.StageResult{}, payloadErr
		}
		actions := []string{"reject"}
		if stage.Result == "approve" {
			actions = append(actions, "approve")
		} else {
			actions = append(actions, "submit")
		}
		roles := []string{stage.Function}
		value, createErr := rs.approvalStore.Create(approval.PendingApproval{
			Kind: approval.KindInput, RunID: rs.runID, AttemptID: attemptID,
			FromStage: stageID, ToStage: stageID, Trigger: humanInputTrigger,
			SubjectHash: subjectHash, RequiredRoles: roles, Quorum: approval.QuorumAny,
			Actions: actions, Targets: map[string]string{actions[0]: stageID, actions[1]: stageID}, Payload: payload,
		})
		if createErr != nil {
			return notifier.StageResult{}, fmt.Errorf("create human input approval: %w", createErr)
		}
		if value.Status == approval.StatusResolved {
			resolved = &value
		} else {
			requestedAt := time.Now().UTC()
			if err := rs.evidence.Append(evidence.Event{
				Type: "approval_requested", AttemptID: value.AttemptID, Timestamp: requestedAt,
				Data: approvalEventData(value),
			}); err != nil {
				return notifier.StageResult{}, fmt.Errorf("record human input request: %w", err)
			}
			if rs.p.recorder != nil {
				rs.p.recorder.ApprovalRequested(rs.runID, value.ID, value.AttemptID, requestedAt, approvalEventData(value))
			}
			if err := rs.saveWaiting(stageID, value.ID); err != nil {
				return notifier.StageResult{}, err
			}
			return notifier.StageResult{}, &ApprovalRequiredError{
				Checkpoint: fmt.Sprintf("human input %s (%s)", stageID, stage.Result),
				RunID:      rs.runID, ApprovalID: value.ID, SubjectHash: value.SubjectHash,
			}
		}
	}
	if resolved == nil || len(resolved.Decisions) == 0 {
		return notifier.StageResult{}, errors.New("resolved human input has no decision actor")
	}
	decision := resolved.Decisions[len(resolved.Decisions)-1]
	if decision.SubjectHash != subjectHash || decision.ActorID == "" || decision.ActorRole != stage.Function {
		return notifier.StageResult{}, errors.New("human input decision actor or subject is invalid")
	}
	if stage.Result == "approve" && decision.Action != "approve" && decision.Action != "reject" ||
		stage.Result != "approve" && decision.Action != "submit" && decision.Action != "reject" {
		return notifier.StageResult{}, errors.New("human input action does not match stage result type")
	}
	if decision.SubmissionVersion > 0 || decision.ContentSHA256 != "" {
		if decision.SubmissionVersion < 1 || humanartifact.Digest([]byte(decision.Comment)) != decision.ContentSHA256 {
			return notifier.StageResult{}, errors.New("human input submission version/hash does not match submitted bytes")
		}
	}

	// If a crash happened after this attempt was durably finished but before
	// graph advancement, the immutable attempt is replayed and reused below.
	if prior, found := rs.finishedHumanAttempt(resolved, stageID, decision); found {
		return prior, nil
	}

	attemptID := resolved.AttemptID
	for _, previous := range rs.results {
		if previous.AttemptID == attemptID {
			// An interrupted/abandoned human attempt is never reused. The durable
			// input approval remains valid, but the retry gets a fresh attempt ID.
			attemptID = rs.evidence.NewAttemptID(stageID, rs.attemptOrdinal+1)
			break
		}
	}
	rs.attemptOrdinal++
	started := time.Now().UTC()
	result := notifier.StageResult{
		RunID: rs.runID, AttemptID: attemptID, Name: stageID, Executor: "human",
		ActorID: decision.ActorID, ActorRole: decision.ActorRole, HumanInputApprovalID: resolved.ID,
		StageIndex: index + 1, TotalStages: len(rs.names), StartedAt: started,
		Usage: &workflow.AttemptUsage{Attested: true},
	}
	rs.usageTotal.Attested = true
	rs.ps.StartAgent(index+1, stageID)
	if rs.p.recorder != nil {
		rs.p.recorder.StageStarted(rs.runID, attemptID, stageID, index+1, started)
	}
	if err := rs.evidence.Append(evidence.Event{Type: "attempt_started", Stage: stageID, AttemptID: attemptID,
		Timestamp: started, Data: map[string]any{"stage_index": index + 1, "executor": "human", "actor_id": decision.ActorID, "actor_role": decision.ActorRole, "human_input_approval_id": resolved.ID}}); err != nil {
		result.Err = fmt.Errorf("record human attempt start: %w", err)
	}
	if result.Err == nil && decision.Action != "reject" && strings.TrimSpace(decision.Description) == "" {
		if err := rs.evidence.AppendControllerEvent(evidence.Event{Type: "description_missing", Stage: stageID, AttemptID: attemptID,
			Timestamp: time.Now().UTC(), Data: map[string]any{"field": "description", "approval_id": resolved.ID}}); err != nil {
			result.Err = fmt.Errorf("record missing human submission description: %w", err)
		}
	}

	var evidenceInputs []evidence.Artifact
	cleanupEvidenceInputs := func() error { return nil }
	if result.Err == nil {
		evidenceInputs, cleanupEvidenceInputs, err = rs.evidence.SnapshotInputs(attemptID, toEvidenceArtifacts(inputs))
		if err != nil {
			result.Err = fmt.Errorf("snapshot human stage inputs: %w", err)
		} else {
			defer func() { _ = cleanupEvidenceInputs() }()
		}
	}
	if result.Err == nil && decision.Action != "reject" {
		content, contentErr := humanResultContent(stage, decision, outputName, outputPath)
		if contentErr != nil {
			result.Err = contentErr
			result.ValidationFailed = true
		} else if writeErr := writeHumanOutput(rs.task.ArtifactRoot, outputPath, content); writeErr != nil {
			result.Err = writeErr
		}
		if result.Err == nil {
			outputFullPath, pathErr := confinedArtifactPath(rs.task.ArtifactRoot, filepath.FromSlash(outputPath))
			if pathErr != nil {
				result.Err = pathErr
			} else {
				info, statErr := os.Stat(outputFullPath)
				if statErr != nil {
					result.Err = statErr
				} else {
					result.Outputs = []runtime.Artifact{{Name: outputName, Path: outputFullPath, Size: info.Size(), ModTime: info.ModTime()}}
				}
			}
		}
	}
	if decision.Action == "reject" {
		result.Verdict = verdict.Rejected
	} else if result.Err == nil {
		result.Verdict = verdict.Approved
	}
	result.FinishedAt = time.Now().UTC()
	result.Duration = result.FinishedAt.Sub(started)
	result.Summary = report.ReadStageSummary(rs.task.ArtifactRoot, rs.runCfg.Feature, stageID)
	rs.deriveStageState(&result)
	manifest := evidence.AttemptManifest{
		AttemptID: attemptID, Stage: stageID, Executor: "human", ActorID: decision.ActorID, ActorRole: decision.ActorRole,
		HumanInputApprovalID: resolved.ID,
		StageIndex:           index + 1, TotalStages: len(rs.names), StartedAt: started, FinishedAt: result.FinishedAt,
		Status: result.Status, Verdict: string(result.Verdict), Error: errorString(result.Err),
		Execution: string(result.State.Execution), Decision: string(result.State.Decision), Outcome: string(result.State.Outcome),
		Usage: result.Usage,
	}
	if decision.Action != "reject" && decision.SubmissionVersion > 0 {
		manifest.HumanSubmissionVersion = decision.SubmissionVersion
		manifest.HumanSubmissionSHA256 = decision.ContentSHA256
		manifest.HumanSubmissionResult = stage.Result
		manifest.HumanSubmissionLinkKind = stage.LinkKind
		manifest.HumanSubmissionDescription = decision.Description
	}
	rs.deriveStageState(&result)
	manifest.Status, manifest.Error = result.Status, errorString(result.Err)
	manifest.Execution, manifest.Decision, manifest.Outcome = string(result.State.Execution), string(result.State.Decision), string(result.State.Outcome)
	manifestPublished := false
	if publishErr := rs.evidence.PublishAttempt(manifest, rs.task.ArtifactRoot, evidenceInputs, toEvidenceArtifacts(result.Outputs)); publishErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("publish human attempt: %w", publishErr))
		rs.deriveStageState(&result)
	} else {
		manifestPublished = true
		if rs.p.attemptManifestWriter != nil {
			_, canonical, readErr := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), rs.evidence.RunDir(), rs.runID, attemptID)
			if readErr == nil {
				readErr = rs.p.attemptManifestWriter.WriteAttemptManifest(canonical)
			}
			if readErr != nil {
				result.Err = errors.Join(result.Err, fmt.Errorf("controller human attempt manifest: %w", readErr))
				manifestPublished = false
				rs.deriveStageState(&result)
			}
		}
	}
	if cleanupErr := cleanupEvidenceInputs(); cleanupErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("cleanup human input snapshot: %w", cleanupErr))
		rs.deriveStageState(&result)
	}
	finishedData := map[string]any{
		"status": result.Status, "execution": result.State.Execution, "decision": result.State.Decision,
		"outcome": result.State.Outcome, "verdict": result.Verdict, "executor": "human",
		"actor_id": decision.ActorID, "actor_role": decision.ActorRole, "human_input_approval_id": resolved.ID,
	}
	if result.Err != nil {
		finishedData["error"] = result.Err.Error()
	}
	if manifestPublished {
		if digest, _, digestErr := evidence.AttemptManifestDigest(rs.p.attemptManifestSource, rs.evidence.RunDir(), rs.runID, attemptID); digestErr == nil {
			finishedData["manifest_sha256"] = digest
		} else {
			finishedData["status"] = string(workflow.OutcomeFailed)
			finishedData["execution"] = workflow.ExecutionInfraFailed
			finishedData["decision"] = workflow.DecisionNotApplicable
			finishedData["outcome"] = workflow.OutcomeFailed
			finishedData["error"] = "human attempt manifest digest unavailable"
			manifestPublished = false
			result.Err = errors.Join(result.Err, digestErr)
		}
	}
	if !manifestPublished {
		finishedData["status"] = result.Status
		finishedData["execution"] = result.State.Execution
		finishedData["decision"] = result.State.Decision
		finishedData["outcome"] = result.State.Outcome
		if result.Err != nil {
			finishedData["error"] = result.Err.Error()
		}
	}
	if err := rs.evidence.Append(evidence.Event{Type: "attempt_finished", Stage: stageID, AttemptID: attemptID, Timestamp: result.FinishedAt, Data: finishedData}); err != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("record human attempt finish: %w", err))
	}
	if rs.p.recorder != nil {
		rs.p.recorder.StageFinished(result)
	}
	return result, nil
}

func (rs *runState) finishedHumanAttempt(approvalValue *approval.PendingApproval, stageID string, decision approval.Decision) (notifier.StageResult, bool) {
	if approvalValue == nil {
		return notifier.StageResult{}, false
	}
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.Name == stageID && result.Executor == "human" && !result.FinishedAt.IsZero() &&
			result.HumanInputApprovalID == approvalValue.ID && !result.Superseded &&
			result.ActorID == decision.ActorID && result.ActorRole == decision.ActorRole &&
			!result.FinishedAt.Before(decision.DecidedAt) {
			return result, true
		}
	}
	return notifier.StageResult{}, false
}

func approvalDecisionRecorded(replayed evidence.ReplayedRun, approvalID string) bool {
	for _, decision := range replayed.ApprovalDecisions {
		if decision.ID == approvalID {
			return true
		}
	}
	return false
}

func validateRecordedHumanInputDecision(replayed evidence.ReplayedRun, value approval.PendingApproval) error {
	for _, decision := range replayed.ApprovalDecisions {
		if decision.ID != value.ID {
			continue
		}
		if decision.Kind != string(approval.KindInput) || decision.DecisionSetSHA256 == "" ||
			decision.AttemptID != value.AttemptID || decision.SubjectHash != value.SubjectHash ||
			decision.FromStage != value.FromStage || decision.ToStage != value.ToStage ||
			decision.Trigger != value.Trigger || decision.Action != value.ResolvedAction {
			return fmt.Errorf("human input approval %s is not bound to a complete verified decision event", value.ID)
		}
		digest, err := evidence.DecisionSetDigest(value.Decisions)
		if err != nil {
			return err
		}
		if digest != decision.DecisionSetSHA256 {
			return fmt.Errorf("human input approval %s decisions differ from verified event evidence", value.ID)
		}
		return nil
	}
	return fmt.Errorf("human input approval %s has no verified decision event", value.ID)
}

// recoveredHumanInputApproval restores an input after resume cleared the
// lifecycle's PendingApprovalID. Prefer the verified human attempt binding;
// when the crash happened before attempt_started, fall back to the latest
// hash-chained human_input decision for the current stage.
func recoveredHumanInputApproval(store ApprovalStore, runID, stageID string, replayed evidence.ReplayedRun) (*approval.PendingApproval, error) {
	load := func(approvalID, expectedAttemptID string) (*approval.PendingApproval, error) {
		value, err := store.Load(runID, approvalID)
		if err != nil {
			return nil, fmt.Errorf("load recovered human input %s: %w", approvalID, err)
		}
		if value.Kind != approval.KindInput || value.Trigger != humanInputTrigger || value.FromStage != stageID ||
			value.Status != approval.StatusResolved || value.AttemptID == "" ||
			(expectedAttemptID != "" && value.AttemptID != expectedAttemptID) {
			return nil, fmt.Errorf("recovered human input %s does not match stage %s", approvalID, stageID)
		}
		if err := validateRecordedHumanInputDecision(replayed, value); err != nil {
			return nil, err
		}
		return &value, nil
	}
	for index := len(replayed.Attempts) - 1; index >= 0; index-- {
		attempt := replayed.Attempts[index]
		if attempt.Stage != stageID || attempt.Executor != "human" || attempt.HumanInputApprovalID == "" ||
			attempt.Superseded || transitionRecorded(replayed, attempt.AttemptID) {
			continue
		}
		return load(attempt.HumanInputApprovalID, "")
	}
	for index := len(replayed.ApprovalDecisions) - 1; index >= 0; index-- {
		decision := replayed.ApprovalDecisions[index]
		if decision.Kind != string(approval.KindInput) || decision.FromStage != stageID || decision.Trigger != humanInputTrigger ||
			transitionRecorded(replayed, decision.AttemptID) {
			continue
		}
		return load(decision.ID, decision.AttemptID)
	}
	return nil, nil
}

func transitionRecorded(replayed evidence.ReplayedRun, attemptID string) bool {
	for _, transition := range replayed.Transitions {
		if transition.AttemptID == attemptID {
			return true
		}
	}
	return false
}

func (rs *runState) humanStageInputs(definition *agent.Agent, stageID string) ([]runtime.Artifact, error) {
	var inputs []runtime.Artifact
	if definition != nil {
		_, collected, err := rs.collectInputs(definition, stageID)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, collected...)
	} else {
		taskPath := filepath.Join(rs.task.ArtifactRoot, "tasks", rs.runCfg.Feature, "task.md")
		info, err := os.Stat(taskPath)
		if err != nil {
			return nil, fmt.Errorf("human stage %s task input: %w", stageID, err)
		}
		inputs = append(inputs, runtime.Artifact{Name: "task", Path: taskPath, Size: info.Size(), ModTime: info.ModTime()})
		inputs = append(inputs, rs.extraInputs[stageID]...)
	}
	if rs.brief.Path != "" {
		inputs = append(inputs, briefInputs(rs.brief)...)
	}
	seen := make(map[string]bool, len(inputs))
	unique := inputs[:0]
	for _, input := range inputs {
		absolute, err := filepath.Abs(input.Path)
		if err != nil {
			return nil, err
		}
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		unique = append(unique, input)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Name != unique[j].Name {
			return unique[i].Name < unique[j].Name
		}
		return unique[i].Path < unique[j].Path
	})
	return unique, nil
}

func humanOutputContract(stage config.TemplateStage, definition *agent.Agent, feature string) (string, string, error) {
	name, configured := "", ""
	if definition != nil {
		switch stage.Result {
		case "md":
			if definition.Outputs["spec"] != "" {
				name, configured = "spec", definition.Outputs["spec"]
			}
		case "link":
			if definition.Outputs["link"] != "" {
				name, configured = "link", definition.Outputs["link"]
			}
		case "approve":
			if definition.Outputs["approval"] != "" {
				name, configured = "approval", definition.Outputs["approval"]
			}
		}
		if configured == "" && len(definition.Outputs) == 1 {
			for outputName, outputPath := range definition.Outputs {
				name, configured = outputName, outputPath
			}
		}
	}
	if configured == "" {
		name = map[string]string{"md": "document", "link": "link", "approve": "approval"}[stage.Result]
		filename := map[string]string{"md": stage.ID + ".md", "link": stage.ID + ".txt", "approve": stage.ID + ".json"}[stage.Result]
		configured = filepath.ToSlash(filepath.Join(feature, "human", filename))
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.FromSlash(runtime.ReplaceVars(configured, feature))))
	if name == "" || resolved == "." || strings.HasPrefix(resolved, "../") || filepath.IsAbs(resolved) {
		return "", "", fmt.Errorf("human stage %s has an unsafe output contract", stage.ID)
	}
	return name, resolved, nil
}

func (rs *runState) humanInputSubject(stage config.TemplateStage, name, outputPath string, inputs []runtime.Artifact) (humanInputSubject, error) {
	value := humanInputSubject{
		RunID: rs.runID, Feature: rs.runCfg.Feature, StageID: stage.ID, Function: stage.Function,
		Result: stage.Result, LinkKind: stage.LinkKind, OutputName: name, OutputPath: outputPath,
		Inputs: make([]humanInputArtifactDigest, 0, len(inputs)),
	}
	for _, input := range inputs {
		artifactType, size, digest, err := evidence.ArtifactDigest(input.Path)
		if err != nil {
			return humanInputSubject{}, fmt.Errorf("human input subject %s: %w", input.Name, err)
		}
		value.Inputs = append(value.Inputs, humanInputArtifactDigest{Name: input.Name, Type: artifactType, Size: size, SHA256: digest})
	}
	if rs.candidate != nil {
		identity, err := rs.candidate.Identity()
		if err != nil {
			return humanInputSubject{}, fmt.Errorf("human input candidate identity: %w", err)
		}
		value.Candidate = identity.WorkspaceSHA256
	}
	return value, nil
}

func humanResultContent(stage config.TemplateStage, decision approval.Decision, outputName, outputPath string) ([]byte, error) {
	if err := humanartifact.ValidateSubmission(stage.Result, stage.LinkKind, decision.Comment); err != nil {
		return nil, err
	}
	switch stage.Result {
	case "md":
		for _, section := range stage.RequiredSections {
			if !strings.Contains(decision.Comment, section) {
				return nil, fmt.Errorf("human markdown is missing required section %q", section)
			}
		}
		return []byte(decision.Comment), nil
	case "link":
		return []byte(decision.Comment), nil
	case "approve":
		if decision.Action != "approve" {
			return nil, errors.New("approve stage requires approve action")
		}
		data, err := json.Marshal(humanApprovalResult{
			Kind: "human_stage_result", StageID: stage.ID, Action: decision.Action,
			ActorID: decision.ActorID, ActorRole: decision.ActorRole, Comment: decision.Comment,
			Description: decision.Description, At: decision.DecidedAt,
		})
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	default:
		return nil, fmt.Errorf("unsupported human result type %q", stage.Result)
	}
}

func writeHumanOutput(artifactRoot, outputPath string, content []byte) error {
	path, err := confinedArtifactPath(artifactRoot, filepath.FromSlash(outputPath))
	if err != nil {
		return err
	}
	if err := safeio.WriteRegularFileNoFollow(path, content, 0o644); err == nil {
		return nil
	} else {
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return err
		}
		previous, readErr := safeio.ReadRegularFile(path, approval.MaxInputCommentBytes+1)
		if readErr != nil || string(previous) != string(content) {
			return fmt.Errorf("human output path is already occupied by different bytes: %w", err)
		}
		return nil // exact retry after a crash between workspace write and evidence publish.
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
