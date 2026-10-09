package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestHumanResultContentValidatesTypedResults(t *testing.T) {
	tests := []struct {
		name    string
		stage   config.TemplateStage
		input   approval.Decision
		want    string
		wantErr bool
	}{
		{
			name: "markdown exact bytes", stage: config.TemplateStage{ID: "spec", Result: "md", RequiredSections: []string{"Acceptance criteria"}},
			input: approval.Decision{Action: "submit", Comment: "\n## Acceptance criteria\n\n- exact bytes  \n"},
			want:  "\n## Acceptance criteria\n\n- exact bytes  \n",
		},
		{
			name: "missing required section", stage: config.TemplateStage{ID: "spec", Result: "md", RequiredSections: []string{"Acceptance criteria"}},
			input: approval.Decision{Action: "submit", Comment: "# Spec\n"}, wantErr: true,
		},
		{
			name: "PR URL", stage: config.TemplateStage{ID: "implementation", Result: "link", LinkKind: "pr"},
			input: approval.Decision{Action: "submit", Comment: "https://example.test/org/repo/pull/7"},
			want:  "https://example.test/org/repo/pull/7\n",
		},
		{
			name: "invalid PR URL", stage: config.TemplateStage{ID: "implementation", Result: "link", LinkKind: "pr"},
			input: approval.Decision{Action: "submit", Comment: "not a url"}, wantErr: true,
		},
		{
			name: "approve result", stage: config.TemplateStage{ID: "intent", Result: "approve"},
			input: approval.Decision{Action: "approve", ActorID: "owner", ActorRole: "business_owner"},
			want:  `{"kind":"human_stage_result","stage_id":"intent","action":"approve","actor_id":"owner","actor_role":"business_owner","at":"0001-01-01T00:00:00Z"}` + "\n",
		},
		{
			name: "reject is not a submitted result", stage: config.TemplateStage{ID: "intent", Result: "approve"},
			input: approval.Decision{Action: "reject"}, wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := humanResultContent(test.stage, test.input, "result", "feature/result")
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected validation error, got %q", got)
				}
				return
			}
			if err != nil || string(got) != test.want {
				t.Fatalf("result = %q, err=%v; want %q", got, err, test.want)
			}
		})
	}
}

func TestHumanOutputRetryIsIdempotentAndConflictsFailClosed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join("feature", "specs", "product", "spec.md")
	content := []byte("# Product spec\n")
	if err := writeHumanOutput(root, path, content); err != nil {
		t.Fatal(err)
	}
	if err := writeHumanOutput(root, path, content); err != nil {
		t.Fatalf("exact retry after a crash should be idempotent: %v", err)
	}
	if err := writeHumanOutput(root, path, []byte("# changed\n")); err == nil {
		t.Fatal("conflicting output must not replace the immutable submission")
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil || string(data) != string(content) {
		t.Fatalf("existing output changed: %q err=%v", data, err)
	}
	if err := writeHumanOutput(root, "../escape.md", content); err == nil {
		t.Fatal("output path traversal must fail")
	}
	if strings.Contains(string(data), "changed") {
		t.Fatal("conflicting retry modified the prior output")
	}
}

func TestRecordedHumanInputDecisionBindsExactSubmittedBytes(t *testing.T) {
	value := approval.PendingApproval{
		ID: "approval-human-input", Kind: approval.KindInput,
		Decisions: []approval.Decision{{
			ApprovalID: "approval-human-input", ActorID: "owner", ActorRole: "product_owner",
			Action: "submit", Comment: "# Original markdown\n", SubjectHash: strings.Repeat("a", 64), DecidedAt: time.Now().UTC(),
		}},
	}
	digest, err := evidence.DecisionSetDigest(value.Decisions)
	if err != nil {
		t.Fatal(err)
	}
	replayed := evidence.ReplayedRun{ApprovalDecisions: []evidence.ReplayedApprovalDecision{{
		ID: value.ID, Kind: string(approval.KindInput), DecisionSetSHA256: digest,
	}}}
	if err := validateRecordedHumanInputDecision(replayed, value); err != nil {
		t.Fatalf("exact decision should match verified event: %v", err)
	}
	value.Decisions[0].Comment = "# Changed markdown\n"
	if err := validateRecordedHumanInputDecision(replayed, value); err == nil {
		t.Fatal("changed input bytes must not match the verified decision event")
	}
}

func TestHumanExecutorPipelineWaitsForTypedInputsAndRequiredGraphGate(t *testing.T) {
	dir := env(t)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewFS(fstest.MapFS{
		"analyst/def.yaml": def(`name: analyst
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  task: tasks/{feature}/task.md
outputs:
  spec: '{feature}/specs/product/spec.md'
`),
		"human-implementer/def.yaml": def(`name: human-implementer
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  spec: '{feature}/specs/product/spec.md'
outputs:
  link: '{feature}/implementation/pr.txt'
`),
		"analyst/prompt.md":           def("fixture"),
		"human-implementer/prompt.md": def("fixture"),
	})
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-input-pipeline-test",
		Title:         "Human input pipeline test",
		Stages: []config.TemplateStage{
			{ID: "intent", Title: "Intent", Function: "business_owner", Result: "approve", Executor: "human", Confirm: "auto"},
			{ID: "product_spec", Title: "Product specification", Function: "product_owner", Result: "md", Executor: "human", Agent: "analyst", RequiredSections: []string{"Acceptance criteria"}, Confirm: "required"},
			{ID: "implementation", Title: "Implementation", Function: "developer", Result: "link", LinkKind: "pr", Executor: "human", Agent: "human-implementer", Confirm: "auto"},
		},
	}
	p := New(cfg, registry, WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}), WithApprovalStore(store))

	startAndFindApproval := func(resumeRunID string) (RunResult, approval.PendingApproval) {
		t.Helper()
		runCfg := RunConfig{TargetDir: dir}
		if resumeRunID == "" {
			runCfg.Feature, runCfg.TaskDesc = "feat", "Build the approved feature"
			runCfg.ApproveGates = true
		} else {
			runCfg.ResumeRunID = resumeRunID
		}
		result, runErr := p.RunWithResult(context.Background(), runCfg)
		var required *ApprovalRequiredError
		if !errors.As(runErr, &required) {
			t.Fatalf("run should wait for an explicit human boundary: result=%+v err=%v", result, runErr)
		}
		pending, loadErr := store.Load(result.RunID, required.ApprovalID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return result, pending
	}
	decide := func(value approval.PendingApproval, actor, role, action, comment string) {
		t.Helper()
		if _, decideErr := store.Decide(value.RunID, value.ID, approval.Decision{
			ActorID: actor, ActorRole: role, Action: action, Comment: comment, SubjectHash: value.SubjectHash,
		}); decideErr != nil {
			t.Fatal(decideErr)
		}
	}

	first, pending := startAndFindApproval("")
	if pending.Kind != approval.KindInput || pending.FromStage != "intent" {
		t.Fatalf("ApproveGates must not bypass human intent input: %+v", pending)
	}
	decide(pending, "alice", "business_owner", "approve", "Approved intent")

	_, pending = startAndFindApproval(first.RunID)
	if pending.Kind != approval.KindInput || pending.FromStage != "product_spec" {
		t.Fatalf("expected typed product spec input, got %+v", pending)
	}
	markdown := "# Product specification\n\n## Acceptance criteria\n\n- Ready for implementation.\n"
	decide(pending, "bob", "product_owner", "submit", markdown)

	_, pending = startAndFindApproval(first.RunID)
	if pending.Kind != approval.KindApprove || pending.FromStage != "product_spec" || pending.Trigger != "graph_outcome:passed" {
		t.Fatalf("required graph confirmation must remain separate from submitted markdown: %+v", pending)
	}
	implementationOutput := filepath.Join(dir, ".ai-team", "artifacts", "feat", "implementation", "pr.txt")
	if _, statErr := os.Stat(implementationOutput); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("implementation must not advance before product spec gate is approved: stat err=%v", statErr)
	}
	decide(pending, "carol", "product_owner", "approve", "Specification accepted")

	_, pending = startAndFindApproval(first.RunID)
	if pending.Kind != approval.KindInput || pending.FromStage != "implementation" {
		t.Fatalf("expected human implementation link input, got %+v", pending)
	}
	decide(pending, "dave", "developer", "submit", "https://example.test/org/repo/pull/42")

	result, runErr := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if runErr != nil || result.Outcome != workflow.RunCompleted {
		t.Fatalf("typed human flow did not complete: result=%+v err=%v", result, runErr)
	}
	for _, stageID := range []string{"intent", "product_spec", "implementation"} {
		var inputApproval approval.PendingApproval
		approvals, listErr := store.List(first.RunID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, value := range approvals {
			if value.Kind == approval.KindInput && value.FromStage == stageID {
				inputApproval = value
				break
			}
		}
		if inputApproval.ID == "" {
			t.Fatalf("missing input approval for %s", stageID)
		}
		_, manifest, readErr := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(), filepath.Join(dir, ".ai-team", "runs", first.RunID), first.RunID, inputApproval.AttemptID)
		if readErr != nil {
			t.Fatalf("read %s human attempt manifest: %v", stageID, readErr)
		}
		if manifest.Stage != stageID || manifest.Executor != "human" || manifest.ActorID == "" || manifest.HumanInputApprovalID != inputApproval.ID {
			t.Fatalf("%s manifest lost human identity: %+v", stageID, manifest)
		}
		if stageID == "implementation" {
			foundSpec := false
			for _, input := range manifest.Inputs {
				if input.Name == "spec" && strings.HasSuffix(input.SourcePath, "/feat/specs/product/spec.md") {
					foundSpec = true
				}
			}
			if !foundSpec || len(manifest.Outputs) != 1 {
				t.Fatalf("implementation attempt must consume the submitted spec and publish its link: %+v", manifest)
			}
		}
	}
}
