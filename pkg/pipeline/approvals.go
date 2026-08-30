package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/verdict"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// approvals.go — единый approval-примитив переходов и subject hash.

func isBackwardTransition(graph workflow.Graph, value *approval.PendingApproval) bool {
	if value == nil || value.Trigger == "delivery_plan" {
		return false
	}
	fromIndex := graph.Index(value.FromStage)
	targetIndex := graph.Index(value.Targets[value.ResolvedAction])
	return fromIndex >= 0 && targetIndex >= 0 && targetIndex <= fromIndex
}

func (rs *runState) authorizeTransition(
	fromStage, toStage, trigger string,
	result notifier.StageResult,
	roles []string,
	quorum string,
	actions []string,
	targets map[string]string,
	deferred bool,
) (string, error) {
	if quorum == "" {
		quorum = approval.QuorumAny
	}
	label := fmt.Sprintf("переход %s → %s", fromStage, toStage)
	subjectHash, err := rs.checkpointSubjectHash(label, fromStage)
	if err != nil {
		return "", err
	}
	candidateSHA := ""
	if rs.candidate != nil {
		identity, identityErr := rs.candidate.Identity()
		if identityErr != nil {
			return "", identityErr
		}
		candidateSHA = identity.WorkspaceSHA256
	}

	// APF-1: повторный проход того же перехода с тем же subject не создаёт
	// новое ожидание и не требует повторного решения (нет перевалидации
	// прежних этапов при loopback).
	if list, listErr := rs.approvalStore.List(rs.runID); listErr == nil {
		if prior := priorApprovalIn(list, fromStage, toStage, trigger, subjectHash, deferred); prior != nil {
			action := prior.ResolvedAction
			if prior.Status == approval.StatusPending {
				action = actions[0]
			}
			reusedAt := time.Now().UTC()
			if err := rs.evidence.Append(evidence.Event{
				Type: "approval_reused", AttemptID: result.AttemptID,
				Timestamp: reusedAt, Data: map[string]any{
					"approval_id": prior.ID, "prior_status": prior.Status,
					"from_stage": fromStage, "to_stage": toStage, "trigger": trigger,
					"subject_hash": subjectHash,
				},
			}); err != nil {
				return "", err
			}
			return action, nil
		}
	}

	value, err := rs.approvalStore.Create(approval.PendingApproval{
		RunID: rs.runID, AttemptID: result.AttemptID,
		FromStage: fromStage, ToStage: toStage, Trigger: trigger,
		SubjectHash: subjectHash, CandidateSHA256: candidateSHA,
		RequiredRoles: append([]string(nil), roles...),
		Quorum:        quorum, Actions: append([]string(nil), actions...), Targets: targets,
		Deferred: deferred,
	})
	if err != nil {
		return "", err
	}
	requestedAt := time.Now().UTC()
	if err := rs.evidence.Append(evidence.Event{
		Type: "approval_requested", AttemptID: result.AttemptID,
		Timestamp: requestedAt, Data: approvalEventData(value),
	}); err != nil {
		return "", err
	}
	if rs.p.recorder != nil {
		rs.p.recorder.ApprovalRequested(rs.runID, value.ID, result.AttemptID, requestedAt, approvalEventData(value))
	}

	// Deferred гейт: переход не паузит и не решается здесь — отдельный
	// approval аттестуется с точным subject и разрешится одним
	// consolidated delivery-решением run'а (APF-1).
	if deferred {
		return actions[0], nil
	}

	action := ""
	if rs.runCfg.ApproveGates {
		action = actions[0]
	} else if rs.p.prompter.Interactive() {
		for {
			answer := rs.p.prompter.Ask(fmt.Sprintf(
				"%s %s, subject %s [%s/diff]",
				ui.Colorize("Решение человека:", ui.ColorBold), label, subjectHash,
				strings.Join(actions, "/"),
			))
			if answer == "diff" {
				fmt.Println(gitDiffOutput(rs.sourceDir()))
				continue
			}
			if answer == "" || answer == "y" {
				answer = actions[0]
			} else if answer == "n" && containsString(actions, "reject") {
				answer = "reject"
			}
			if containsString(actions, answer) {
				action = answer
				break
			}
			fmt.Printf("  неизвестный ответ: %s\n", answer)
		}
	}
	if action == "" {
		if err := rs.saveWaiting(targets[actions[0]], value.ID); err != nil {
			return "", err
		}
		return "", &ApprovalRequiredError{
			Checkpoint: label, RunID: rs.runID, ApprovalID: value.ID, SubjectHash: value.SubjectHash,
		}
	}

	rolesToDecide := value.RequiredRoles
	if value.Quorum == approval.QuorumAny {
		rolesToDecide = rolesToDecide[:1]
	}
	for _, role := range rolesToDecide {
		value, err = rs.approvalStore.Decide(value.RunID, value.ID, approval.Decision{
			ActorID: "local-user", ActorRole: role, Action: action,
			SubjectHash: value.SubjectHash,
		})
		if err != nil {
			return "", err
		}
	}
	decidedAt := time.Now().UTC()
	if err := rs.evidence.Append(evidence.Event{
		Type: "approval_decided", AttemptID: result.AttemptID,
		Timestamp: decidedAt, Data: approvalEventData(value),
	}); err != nil {
		return "", err
	}
	if rs.p.recorder != nil {
		rs.p.recorder.ApprovalDecided(rs.runID, value.ID, result.AttemptID, decidedAt, approvalEventData(value))
	}
	return value.ResolvedAction, nil
}

// priorApprovalIn — APF-1: поиск в списке ранее зафиксированного approval
// того же перехода и точного subject. Возвращает первый (по времени создания)
// подходящий approval, если:
//
//   - pending deferred-гейт того же перехода/subject ещё не разрешён — новый
//     не создаётся, повтора при loopback не бывает;
//   - resolved-approval, чьё действие ведёт в тот же toStage и остаётся
//     валидным action'ом ребра — решение не запрашивается повторно (нет
//     перевалидации прежних этапов).
func priorApprovalIn(list []approval.PendingApproval, from, to, trigger, subject string, deferred bool) *approval.PendingApproval {
	for i := range list {
		value := &list[i]
		if value.FromStage != from || value.ToStage != to || value.Trigger != trigger || value.SubjectHash != subject {
			continue
		}
		switch value.Status {
		case approval.StatusPending:
			if value.Deferred == deferred {
				return value
			}
		case approval.StatusResolved:
			if value.Targets[value.ResolvedAction] == to && containsString(list[i].Actions, value.ResolvedAction) {
				return value
			}
		}
	}
	return nil
}

func approvalEventData(value approval.PendingApproval) map[string]any {
	data := map[string]any{
		"approval_id": value.ID, "subject_hash": value.SubjectHash,
		"candidate_sha256": value.CandidateSHA256,
		"from_stage":       value.FromStage, "to_stage": value.ToStage,
		"trigger": value.Trigger, "required_roles": value.RequiredRoles,
		"quorum": value.Quorum, "actions": value.Actions,
		"status": value.Status, "resolved_action": value.ResolvedAction,
		"decisions": value.Decisions,
	}
	if len(value.Payload) > 0 {
		data["payload"] = value.Payload
	}
	// Event hashing is verified after JSON is decoded into generic values.
	// Normalize named string types and nested structs before hashing so the
	// in-memory and replay representations are byte-identical.
	encoded, err := json.Marshal(data)
	if err != nil {
		return data
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return data
	}
	return normalized
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// checkpoints применяет единую checkpoint policy. Legacy gate/transition поля
// нормализуются Config.Checkpoint*Policy и не создают отдельные механизмы.
func (rs *runState) checkpointSubjectHash(label, stage string) (string, error) {
	type artifactSubject struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	type subject struct {
		RunID           string                `json:"run_id"`
		Label           string                `json:"label"`
		AttemptID       string                `json:"attempt_id"`
		Stage           string                `json:"stage"`
		State           workflow.AttemptState `json:"state"`
		Verdict         verdict.Verdict       `json:"verdict,omitempty"`
		Artifacts       []artifactSubject     `json:"artifacts,omitempty"`
		CheckProof      []string              `json:"check_evidence_digests,omitempty"`
		CandidateSHA256 string                `json:"candidate_sha256,omitempty"`
	}
	value := subject{RunID: rs.runID, Label: label, Stage: stage}
	for index := len(rs.results) - 1; index >= 0; index-- {
		result := rs.results[index]
		if result.Name != stage || result.Superseded {
			continue
		}
		value.AttemptID, value.State, value.Verdict = result.AttemptID, result.State, result.Verdict
		for _, output := range result.Outputs {
			artifactType, size, digest, err := evidence.ArtifactDigest(output.Path)
			if err != nil {
				return "", fmt.Errorf("checkpoint %s artifact %s: %w", label, output.Name, err)
			}
			value.Artifacts = append(value.Artifacts, artifactSubject{Name: output.Name, Type: artifactType, Size: size, SHA256: digest})
		}
		for _, check := range result.Checks {
			if check.EvidenceDigest != "" {
				value.CheckProof = append(value.CheckProof, check.EvidenceDigest)
			}
		}
		break
	}
	if value.AttemptID == "" {
		return "", fmt.Errorf("checkpoint %s: subject stage %s не найден", label, stage)
	}
	if rs.candidate != nil {
		identity, identityErr := rs.candidate.Identity()
		if identityErr != nil {
			return "", fmt.Errorf("checkpoint %s candidate identity: %w", label, identityErr)
		}
		value.CandidateSHA256 = identity.WorkspaceSHA256
	}
	sort.Strings(value.CheckProof)
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

// finalize — итоговый отчёт, статус-бар, сводка, запись финального статуса.
// Вызывается на всех исходах, включая отмену контекста.
