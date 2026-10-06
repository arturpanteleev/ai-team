package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const (
	maxQuestionBytes  = 32 << 10
	maxAnswerBytes    = 16 << 10
	maxQuestionRounds = 3
)

type questionPayload struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
}

func analystQuestionsPath(artifactRoot, feature string) string {
	return filepath.Join(artifactRoot, "tasks", feature, "questions.md")
}

func questionsPayload(resultOutputs []runtime.Artifact) (json.RawMessage, bool, error) {
	for _, output := range resultOutputs {
		if output.Name != "questions" {
			continue
		}
		data, err := safeio.ReadRegularFile(output.Path, maxQuestionBytes)
		if err != nil {
			return nil, false, fmt.Errorf("read analyst questions: %w", err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, false, errors.New("analyst questions artifact is empty")
		}
		encoded, err := json.Marshal(questionPayload{Kind: "questions", Markdown: string(data)})
		return encoded, true, err
	}
	return nil, false, nil
}

func questionAnswer(decisions []approval.Decision) string {
	for index := len(decisions) - 1; index >= 0; index-- {
		if decisions[index].Action == "answer_questions" {
			return strings.TrimSpace(decisions[index].Comment)
		}
	}
	return ""
}

// recoveredQuestionApproval finds the durable clarification decision that
// selected the analyst as the next stage. A process can stop after the resume
// decision and running lifecycle state have been persisted, before the stage
// consumes its extra inputs. On the next resume, the lifecycle no longer has a
// pending approval ID, so reconstruct these inputs from the approval store.
func recoveredQuestionApproval(store *approval.Store, runID, nextStage string) (*approval.PendingApproval, error) {
	if nextStage != "analyst" {
		return nil, nil
	}
	values, err := store.List(runID)
	if err != nil {
		return nil, err
	}
	for index := len(values) - 1; index >= 0; index-- {
		value := &values[index]
		if value.Status != approval.StatusResolved || value.FromStage != "analyst" ||
			value.ResolvedAction != "answer_questions" || value.Targets[value.ResolvedAction] != nextStage ||
			value.Trigger != "graph_outcome:blocked" {
			continue
		}
		var payload questionPayload
		if json.Unmarshal(value.Payload, &payload) != nil || payload.Kind != "questions" || strings.TrimSpace(payload.Markdown) == "" {
			continue
		}
		if questionAnswer(value.Decisions) == "" {
			return nil, fmt.Errorf("resolved clarification approval %s has no durable answer", value.ID)
		}
		return value, nil
	}
	return nil, nil
}

func countQuestionApprovals(store *approval.Store, runID string) (int, error) {
	values, err := store.List(runID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, value := range values {
		if value.Trigger == "graph_outcome:blocked" && value.FromStage == "analyst" && value.Payload != nil {
			var payload questionPayload
			if json.Unmarshal(value.Payload, &payload) == nil && payload.Kind == "questions" {
				count++
			}
		}
	}
	return count, nil
}

// writeQuestionAnswerInput materializes the durable approval comment as an
// immutable run input. The approval remains the source of truth; on recovery
// the same input can be reconstructed from its persisted decision.
func writeQuestionAnswerInput(targetDir, runID, approvalID, answer string) (runtime.Artifact, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > maxAnswerBytes {
		return runtime.Artifact{}, errors.New("answer must contain 1..16384 bytes")
	}
	path := filepath.Join(targetDir, ".ai-team", "runs", runID, "inputs", approvalID+"-answer.md")
	content := []byte("# Ответ Product Owner\n\n" + answer + "\n")
	if err := safeio.WriteRegularFileNoFollow(path, content, 0o444); err != nil {
		if info, statErr := os.Lstat(path); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return runtime.Artifact{}, err
		}
		existing, readErr := safeio.ReadRegularFile(path, maxAnswerBytes+128)
		if readErr != nil || string(existing) != string(content) {
			return runtime.Artifact{}, fmt.Errorf("existing clarification input differs from durable decision: %w", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return runtime.Artifact{}, err
	}
	return runtime.Artifact{Name: "clarification-answer", Path: path, Size: info.Size(), ModTime: info.ModTime()}, nil
}
