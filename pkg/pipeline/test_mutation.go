package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/scope"
	"github.com/arturpanteleev/ai-team/pkg/ui"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// test_mutation.go — V0-1: provenance и политика на мутации тестового флота.
// Классифицированные мутации этапа (tests class) проходят через политику
// агента: for source-агентов по умолчанию required — создаётся deferred
// exact-subject approval, который консолидируется в единое delivery-решение
// (APF-1). warn фиксирует предупреждение без гейта, off — только evidence.

const testMutationTrigger = "test_mutation"

// testMutationChanges выделяет из атрибутированных мутаций этапа изменения
// тестового флота.
func testMutationChanges(changes []workflow.MutationChange) []workflow.MutationChange {
	var out []workflow.MutationChange
	for _, change := range changes {
		if change.Class == scope.ClassTests {
			out = append(out, change)
		}
	}
	return out
}

// testMutationSubjectHash — exact subject мутации тестов: run_id, этап и
// неизменяемый список tests-мутаций (пути, режимы, классы). Не включает
// attempt_id, чтобы повторных проходов этапа с идентичной мутацией
// (loopback) не порождал новый approval.
func testMutationSubjectHash(runID, stage string, changes []workflow.MutationChange) (string, error) {
	type subject struct {
		RunID string                    `json:"run_id"`
		Stage string                    `json:"stage"`
		Class string                    `json:"class"`
		Mut   []workflow.MutationChange `json:"mutations"`
	}
	value := subject{RunID: runID, Stage: stage, Class: scope.ClassTests, Mut: changes}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

// enforceTestMutationPolicy применяет политику агента к tests-мутациям этапа.
// Вызывается сразу после runStage, когда mutation guard уже атрибутировал
// изменения. По умолчанию (fail-closed) source-агент, тронувший тестовый
// флот, создаёт deferred exact-subject approval; ни один следующий артефакт
// или delivery не освобождается от точного subject.
func (rs *runState) enforceTestMutationPolicy(stage string, result notifier.StageResult) error {
	changes := testMutationChanges(result.MutationChanges)
	if len(changes) == 0 || result.Err != nil {
		return nil
	}
	policy := "off"
	if a, err := rs.p.reg.Load(stage); err == nil {
		policy = a.EffectiveTestModifyPolicy()
	}

	subjectHash := ""
	if policy == "required" {
		var err error
		subjectHash, err = testMutationSubjectHash(rs.runID, stage, changes)
		if err != nil {
			return err
		}
	}
	if err := rs.evidence.Append(evidence.Event{
		Type: "test_mutations", Stage: stage, AttemptID: result.AttemptID,
		Timestamp: time.Now().UTC(), Data: normalizeEventData(map[string]any{
			"policy":       policy,
			"test_changes": changes,
		}),
	}); err != nil {
		return err
	}
	if policy != "required" {
		return nil
	}

	// Reuse уже зафиксированного гейта этого exact subject (loopback без
	// повторного решения), иначе создать новый deferred-гейт.
	if list, listErr := rs.approvalStore.List(rs.runID); listErr == nil {
		if priorApprovalIn(list, stage, stage, testMutationTrigger, subjectHash, true) != nil {
			return nil
		}
	}
	value, err := rs.approvalStore.Create(approval.PendingApproval{
		RunID: rs.runID, AttemptID: result.AttemptID,
		FromStage: stage, ToStage: stage, Trigger: testMutationTrigger,
		SubjectHash: subjectHash, Deferred: true,
		RequiredRoles: []string{deliveryApprovalRole}, Quorum: approval.QuorumAny,
		Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": stage, "reject": "$stop"},
		Payload: json.RawMessage(mustTestChangesJSON(changes)),
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
	fmt.Fprintf(os.Stderr, "  %s %s изменил тестовый флот: %s (exact subject %s; ratification в consolidated delivery решении)\n",
		ui.Colorize("⚠", ui.ColorYellow), stage, testMutationSummary(changes), subjectHash)
	return nil
}

func mustTestChangesJSON(changes []workflow.MutationChange) []byte {
	data, _ := json.Marshal(changes)
	return data
}

// normalizeEventData приводит Data к канонической JSON-форме (map с
// отсортированными ключами). Без этого вложенные typed-структуры при
// переэталоне события на верификации цепочки хешируются иначе, чем при
// записи, и ломают event log integrity.
func normalizeEventData(data map[string]any) map[string]any {
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

func testMutationSummary(changes []workflow.MutationChange) string {
	counts := map[string]int{workflow.MutationAdded: 0, workflow.MutationModified: 0, workflow.MutationRemoved: 0}
	for _, change := range changes {
		counts[change.Kind]++
	}
	return fmt.Sprintf("%d added, %d modified, %d removed",
		counts[workflow.MutationAdded], counts[workflow.MutationModified], counts[workflow.MutationRemoved])
}
