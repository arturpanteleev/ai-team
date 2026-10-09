package humanartifact

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
)

type submissionController struct {
	store  *approval.Store
	mu     sync.Mutex
	values []approval.PendingApproval
}

func (c *submissionController) Approvals(runID string) ([]approval.PendingApproval, error) {
	if c.store != nil {
		return c.store.List(runID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]approval.PendingApproval(nil), c.values...), nil
}

func (c *submissionController) Decide(runID, approvalID string, decision approval.Decision) (approval.PendingApproval, error) {
	return c.store.Decide(runID, approvalID, decision)
}

func createHumanInput(t *testing.T, store *approval.Store, runID, attemptID, stageID, result, linkKind string) approval.PendingApproval {
	t.Helper()
	payload, err := json.Marshal(approval.InputPayload{Kind: string(approval.KindInput), StageID: stageID, Result: result,
		LinkKind: linkKind, OutputName: "result", OutputPath: "feature/result." + resultExtension(result)})
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{"submit", "reject"}
	if result == "approve" {
		actions = []string{"approve", "reject"}
	}
	targets := map[string]string{}
	for _, action := range actions {
		targets[action] = stageID
	}
	value, err := store.Create(approval.PendingApproval{
		Kind: approval.KindInput, RunID: runID, AttemptID: attemptID, FromStage: stageID, ToStage: stageID,
		Trigger: "human_input", SubjectHash: strings.Repeat("a", 64), RequiredRoles: []string{"developer"},
		Quorum: approval.QuorumAny, Actions: actions, Targets: targets, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func resultExtension(result string) string {
	if result == "approve" {
		return "json"
	}
	return result
}

func TestSubmitPersistsTypedVersionAndDescriptionWithApproval(t *testing.T) {
	target := t.TempDir()
	approvalStore, err := approval.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	first := createHumanInput(t, approvalStore, "run-human", "attempt-1", "spec", "md", "")
	artifacts, err := New(target)
	if err != nil {
		t.Fatal(err)
	}
	controller := &submissionController{store: approvalStore}
	command := SubmissionCommand{StageID: "spec", Result: "md", Content: " \n# Spec  \n\n", Description: "  finished spec \n",
		ActorID: "dev-1", ActorRole: "developer"}
	result, err := artifacts.Submit(controller, first.RunID, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision.Revision != 1 || result.Revision.Content != command.Content || result.Revision.Description != command.Description ||
		result.Approval.Status != approval.StatusResolved || len(result.Approval.Decisions) != 1 ||
		result.Approval.Decisions[0].SubmissionVersion != 1 || result.Approval.Decisions[0].ContentSHA256 != Digest([]byte(command.Content)) {
		t.Fatalf("typed submission metadata was not bound to immutable version: %+v", result)
	}
	retry, err := artifacts.Submit(controller, first.RunID, command)
	if err != nil || retry.Revision.ID != result.Revision.ID || retry.Revision.Revision != 1 {
		t.Fatalf("exact retry must retain the original version: result=%+v err=%v", retry, err)
	}

	second := createHumanInput(t, approvalStore, "run-human", "attempt-2", "spec", "md", "")
	command.Content = "# Updated spec\n"
	command.Description = "updated"
	updated, err := artifacts.Submit(controller, second.RunID, command)
	if err != nil || updated.Revision.Revision != 2 || updated.Revision.BaseRevision != result.Revision.ID {
		t.Fatalf("return-to-stage submission must append a new version: result=%+v err=%v", updated, err)
	}
	history, err := artifacts.List("run-human", "stages/spec/result.md")
	if err != nil || len(history) != 2 || history[0].Content != result.Revision.Content || history[1].Content != updated.Revision.Content {
		t.Fatalf("stage versions were overwritten: history=%+v err=%v", history, err)
	}
}

func TestSubmitConcurrentChangedBytesForApprovalConflict(t *testing.T) {
	target := t.TempDir()
	approvalStore, err := approval.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value := createHumanInput(t, approvalStore, "run-race", "attempt-race", "spec", "md", "")
	artifacts, err := New(target)
	if err != nil {
		t.Fatal(err)
	}
	controller := &submissionController{store: approvalStore}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"# A\n", "# B\n"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			<-start
			_, submitErr := artifacts.Submit(controller, value.RunID, SubmissionCommand{StageID: "spec", Result: "md", Content: content,
				ActorID: "dev-1", ActorRole: "developer"})
			results <- submitErr
		}(content)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, conflicts := 0, 0
	for resultErr := range results {
		if resultErr == nil {
			succeeded++
		} else if strings.Contains(resultErr.Error(), "conflict") {
			conflicts++
		} else {
			t.Fatal(resultErr)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatalf("concurrent changed submissions should produce one immutable winner: success=%d conflicts=%d", succeeded, conflicts)
	}
	loaded, err := approvalStore.Load(value.RunID, value.ID)
	if err != nil || loaded.Status != approval.StatusResolved || len(loaded.Decisions) != 1 {
		t.Fatalf("approval decision was overwritten: %+v err=%v", loaded, err)
	}
}

func TestSubmitValidatesConfiguredLinkKindAndURLScheme(t *testing.T) {
	target := t.TempDir()
	approvalStore, err := approval.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value := createHumanInput(t, approvalStore, "run-link", "attempt-link", "implementation", "link", "pr")
	artifacts, err := New(target)
	if err != nil {
		t.Fatal(err)
	}
	controller := &submissionController{store: approvalStore}
	for _, command := range []SubmissionCommand{
		{StageID: "implementation", Result: "link", LinkKind: "build", Content: "https://example.test/job/1", ActorID: "dev", ActorRole: "developer"},
		{StageID: "implementation", Result: "link", LinkKind: "pr", Content: "file:///tmp/pr", ActorID: "dev", ActorRole: "developer"},
	} {
		if _, err := artifacts.Submit(controller, value.RunID, command); err == nil {
			t.Fatalf("invalid link submission accepted: %+v", command)
		}
	}
	result, err := artifacts.Submit(controller, value.RunID, SubmissionCommand{StageID: "implementation", Result: "link", LinkKind: "pr",
		Content: "https://example.test/org/repo/pull/7", Description: "PR created", ActorID: "dev", ActorRole: "developer"})
	if err != nil || result.Revision.Result != "link" || result.Revision.LinkKind != "pr" {
		t.Fatalf("valid configured PR link rejected: result=%+v err=%v", result, err)
	}
}
