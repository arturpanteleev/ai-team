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
	"github.com/arturpanteleev/ai-team/pkg/humanartifact"
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
		AttemptID: "attempt-human-input", FromStage: "product_spec", ToStage: "product_spec",
		Trigger: humanInputTrigger, SubjectHash: strings.Repeat("a", 64),
		Status: approval.StatusResolved, ResolvedAction: "submit",
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
		AttemptID: value.AttemptID, FromStage: value.FromStage, ToStage: value.ToStage,
		Trigger: value.Trigger, SubjectHash: value.SubjectHash, Action: value.ResolvedAction,
	}}}
	if err := validateRecordedHumanInputDecision(replayed, value); err != nil {
		t.Fatalf("exact decision should match verified event: %v", err)
	}
	value.Decisions[0].Comment = "# Changed markdown\n"
	if err := validateRecordedHumanInputDecision(replayed, value); err == nil {
		t.Fatal("changed input bytes must not match the verified decision event")
	}
	value.Decisions[0].Comment = "# Original markdown\n"
	value.AttemptID = "different-attempt"
	if err := validateRecordedHumanInputDecision(replayed, value); err == nil {
		t.Fatal("changed approval identity must not match the verified decision event")
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

type humanCrashEvidenceFactory struct {
	point   string
	crashed bool
}

func (f *humanCrashEvidenceFactory) Start(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	store, err := (filesystemEvidenceStoreFactory{}).Start(root, manifest)
	if err != nil {
		return nil, err
	}
	return &humanCrashEvidenceStore{EvidenceStore: store, factory: f}, nil
}

func (f *humanCrashEvidenceFactory) Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	store, manifest, replayed, err := (filesystemEvidenceStoreFactory{}).Resume(root, runID)
	if err != nil {
		return nil, evidence.RunManifest{}, evidence.ReplayedRun{}, err
	}
	return &humanCrashEvidenceStore{EvidenceStore: store, factory: f}, manifest, replayed, nil
}

type humanCrashEvidenceStore struct {
	EvidenceStore
	factory *humanCrashEvidenceFactory
}

func (s *humanCrashEvidenceStore) Append(event evidence.Event) error {
	if !s.factory.crashed && s.factory.point == "after-attempt-started" && event.Type == "attempt_started" {
		if err := s.EvidenceStore.Append(event); err != nil {
			return err
		}
		if event.Data["executor"] == "human" {
			s.factory.crashed = true
			panic("simulated crash after durable human attempt_started")
		}
		return nil
	}
	if !s.factory.crashed && s.factory.point == "before-transition" && event.Type == "transition_selected" {
		s.factory.crashed = true
		panic("simulated crash before transition_selected")
	}
	return s.EvidenceStore.Append(event)
}

func (s *humanCrashEvidenceStore) PublishAttempt(manifest evidence.AttemptManifest, artifactRoot string, inputs, outputs []evidence.Artifact) error {
	if !s.factory.crashed && s.factory.point == "before-publish" {
		s.factory.crashed = true
		panic("simulated crash after output write before attempt publish")
	}
	return s.EvidenceStore.PublishAttempt(manifest, artifactRoot, inputs, outputs)
}

func humanApproveCrashFixture(t *testing.T) (string, *config.Config, *agent.Registry, *approval.Store, string, approval.PendingApproval) {
	t.Helper()
	dir := env(t)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-crash-recovery-test",
		Title:         "Human crash recovery test",
		Stages: []config.TemplateStage{{
			ID: "intent", Title: "Intent", Function: "business_owner", Result: "approve", Executor: "human", Confirm: "auto",
		}},
	}
	registry := agent.NewFS(fstest.MapFS{})
	p := New(cfg, registry, WithApprovalStore(store), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	result, runErr := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "Approve the feature", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(runErr, &required) {
		t.Fatalf("initial human stage should wait for input: result=%+v err=%v", result, runErr)
	}
	pending, err := store.Load(result.RunID, required.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(result.RunID, pending.ID, approval.Decision{
		ActorID: "alice", ActorRole: "business_owner", Action: "approve", SubjectHash: pending.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	return dir, cfg, registry, store, result.RunID, pending
}

func resumeHumanCrashRun(t *testing.T, p *Pipeline, dir, runID string) (RunResult, error) {
	t.Helper()
	return p.RunWithResult(context.Background(), RunConfig{ResumeRunID: runID, TargetDir: dir})
}

func expectHumanCrash(t *testing.T, action func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected simulated controller crash")
		}
	}()
	action()
}

func TestHumanInputResumeRecoversAfterOutputBeforeAttemptFinish(t *testing.T) {
	dir, cfg, registry, store, runID, pending := humanApproveCrashFixture(t)
	factory := &humanCrashEvidenceFactory{point: "before-publish"}
	crashing := New(cfg, registry, WithApprovalStore(store), WithEvidenceStoreFactory(factory), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	expectHumanCrash(t, func() {
		_, _ = resumeHumanCrashRun(t, crashing, dir, runID)
	})
	if !factory.crashed {
		t.Fatal("crash was not injected at the pre-finish boundary")
	}
	outputPath := filepath.Join(dir, ".ai-team", "artifacts", "feat", "human", "intent.json")
	before, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("human output should already exist at injected crash: %v", err)
	}

	completed, err := resumeHumanCrashRun(t, New(cfg, registry,
		WithApprovalStore(store), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{})), dir, runID)
	if err != nil || completed.Outcome != workflow.RunCompleted {
		t.Fatalf("resume did not recover resolved input after an interrupted attempt: result=%+v err=%v", completed, err)
	}
	after, err := os.ReadFile(outputPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("exact approve output retry changed immutable bytes: before=%q after=%q err=%v", before, after, err)
	}
	approvals, err := store.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	inputApprovals := 0
	for _, value := range approvals {
		if value.Kind == approval.KindInput && value.FromStage == "intent" {
			inputApprovals++
			if value.ID != pending.ID || value.Status != approval.StatusResolved {
				t.Fatalf("resume replaced the original decision: %+v", value)
			}
		}
	}
	if inputApprovals != 1 {
		t.Fatalf("expected exactly one input approval after recovery, got %d", inputApprovals)
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"), runID)
	if err != nil {
		t.Fatal(err)
	}
	var completedHumanAttempts int
	for _, attempt := range replayed.Attempts {
		if attempt.Stage == "intent" && attempt.Executor == "human" && !attempt.Superseded && attempt.State.Outcome == workflow.OutcomePassed {
			completedHumanAttempts++
		}
	}
	if completedHumanAttempts != 1 {
		t.Fatalf("recovery should publish exactly one successful human attempt, got %+v", replayed.Attempts)
	}
}

func TestHumanInputResumeReusesFinishedAttemptBeforeTransition(t *testing.T) {
	dir, cfg, registry, store, runID, _ := humanApproveCrashFixture(t)
	factory := &humanCrashEvidenceFactory{point: "before-transition"}
	crashing := New(cfg, registry, WithApprovalStore(store), WithEvidenceStoreFactory(factory), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	expectHumanCrash(t, func() {
		_, _ = resumeHumanCrashRun(t, crashing, dir, runID)
	})
	if !factory.crashed {
		t.Fatal("crash was not injected after attempt finish and before transition")
	}
	completed, err := resumeHumanCrashRun(t, New(cfg, registry,
		WithApprovalStore(store), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{})), dir, runID)
	if err != nil || completed.Outcome != workflow.RunCompleted {
		t.Fatalf("resume did not reuse the finished human attempt: result=%+v err=%v", completed, err)
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", runID, "events.jsonl"), runID)
	if err != nil {
		t.Fatal(err)
	}
	var stageAttempts int
	for _, attempt := range replayed.Attempts {
		if attempt.Stage == "intent" && attempt.Executor == "human" {
			stageAttempts++
		}
	}
	if stageAttempts != 1 {
		t.Fatalf("finished attempt must be reused without a duplicate; attempts=%+v", replayed.Attempts)
	}
}

func TestHumanInputResumeRecoversAlongsideForwardGraphHandoffAfterAttemptStarted(t *testing.T) {
	dir := env(t)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewFS(fstest.MapFS{
		"spec/def.yaml": def(`name: spec
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  spec: '{feature}/spec.md'
`),
		"review/def.yaml": def(`name: review
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  spec: '{feature}/spec.md'
outputs:
  review: '{feature}/review.md'
`),
		"delivery/def.yaml": def(`name: delivery
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs: {}
`),
		"spec/prompt.md":     def("spec"),
		"review/prompt.md":   def("review"),
		"delivery/prompt.md": def("delivery"),
	})
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-forward-handoff-recovery-test",
		Title:         "Human forward handoff recovery test",
		Stages: []config.TemplateStage{
			{ID: "spec", Title: "Specification", Function: "product_owner", Result: "md", Executor: "human", Agent: "spec", Confirm: "required"},
			{ID: "review", Title: "Review", Function: "reviewer", Result: "md", Executor: "human", Agent: "review", Confirm: "required"},
			{ID: "delivery", Title: "Delivery", Function: "operator", Result: "md", Executor: "human", Confirm: "auto"},
		},
	}
	p := New(cfg, registry, WithApprovalStore(store), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))

	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "Create and review a specification", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first human stage should request its typed result: result=%+v err=%v", first, err)
	}
	specInput, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || specInput.Kind != approval.KindInput || specInput.FromStage != "spec" {
		t.Fatalf("missing specification input approval: %+v err=%v", specInput, err)
	}
	if _, err := store.Decide(first.RunID, specInput.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: "submit", Comment: "# Original approved specification\n",
		SubjectHash: specInput.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("required graph gate should follow specification submission: %v", err)
	}
	graphApproval, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || graphApproval.Kind != approval.KindApprove || graphApproval.FromStage != "spec" || graphApproval.Trigger != "graph_outcome:passed" {
		t.Fatalf("missing required forward graph approval: %+v err=%v", graphApproval, err)
	}
	_, manifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(),
		filepath.Join(dir, ".ai-team", "runs", first.RunID), first.RunID, graphApproval.AttemptID)
	if err != nil {
		t.Fatalf("read specification attempt manifest: %v", err)
	}
	if len(manifest.Outputs) != 1 || manifest.Outputs[0].EvidencePath == "" {
		t.Fatalf("specification attempt output missing: %+v", manifest.Outputs)
	}
	output := manifest.Outputs[0]
	_, _, digest, err := evidence.ArtifactDigest(filepath.Join(dir, ".ai-team", "runs", first.RunID, filepath.FromSlash(output.EvidencePath)))
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := humanartifact.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := revisions.Append(first.RunID, output.EvidencePath, "", digest,
		"# Pinned specification revision\n", "Use this exact handoff", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(first.RunID, graphApproval.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: "approve", Comment: "Continue to review",
		SubjectHash: graphApproval.SubjectHash, ArtifactRevisions: map[string]string{output.EvidencePath: revision.ID},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("second human stage should request its typed result: %v", err)
	}
	reviewInput, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || reviewInput.Kind != approval.KindInput || reviewInput.FromStage != "review" {
		t.Fatalf("missing review input approval: %+v err=%v", reviewInput, err)
	}
	const submittedReview = "# Exact human review result\n"
	if _, err := store.Decide(first.RunID, reviewInput.ID, approval.Decision{
		ActorID: "reviewer", ActorRole: "reviewer", Action: "submit", Comment: submittedReview,
		SubjectHash: reviewInput.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}

	factory := &humanCrashEvidenceFactory{point: "after-attempt-started"}
	crashing := New(cfg, registry, WithApprovalStore(store), WithEvidenceStoreFactory(factory),
		WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	expectHumanCrash(t, func() {
		_, _ = resumeHumanCrashRun(t, crashing, dir, first.RunID)
	})
	if !factory.crashed {
		t.Fatal("crash was not injected after the human attempt_started event became durable")
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	foundInterruptedReview := false
	for _, attempt := range replayed.Attempts {
		if attempt.Stage == "review" && attempt.Executor == "human" && attempt.HumanInputApprovalID == reviewInput.ID && attempt.FinishedAt.IsZero() {
			foundInterruptedReview = true
		}
	}
	if !foundInterruptedReview {
		t.Fatalf("durable unfinished human attempt was not recorded: %+v", replayed.Attempts)
	}

	_, err = resumeHumanCrashRun(t, New(cfg, registry, WithApprovalStore(store),
		WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{})), dir, first.RunID)
	if !errors.As(err, &required) {
		t.Fatalf("recovery should finish review and stop at its separate required graph gate: %v", err)
	}
	reviewGate, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || reviewGate.Kind != approval.KindApprove || reviewGate.FromStage != "review" || reviewGate.Trigger != "graph_outcome:passed" {
		t.Fatalf("review graph gate was lost or replaced by another human input: %+v err=%v", reviewGate, err)
	}
	// Pending approvals store the selection in their subject hash. Recompute
	// that hash from the completed review attempt to prove the older forward
	// graph approval, rather than the human input, remained the selected source.
	replayed, err = evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	results, _, _, err := replayedStageResults(replayed, filepath.Join(dir, ".ai-team", "runs", first.RunID),
		evidence.FilesystemAttemptManifestSource(), registry, cfg, len(cfg.Stages))
	if err != nil {
		t.Fatal(err)
	}
	expectedSubject, err := (&runState{
		runID: first.RunID, results: results,
		selectedArtifactRevisions: map[string]string{output.EvidencePath: revision.ID},
	}).checkpointSubjectHash("переход review → delivery", "review")
	if err != nil {
		t.Fatal(err)
	}
	withoutGraphSelection, err := (&runState{runID: first.RunID, results: results}).checkpointSubjectHash("переход review → delivery", "review")
	if err != nil {
		t.Fatal(err)
	}
	if reviewGate.SubjectHash != expectedSubject || reviewGate.SubjectHash == withoutGraphSelection {
		t.Fatalf("recovered graph approval selection was shadowed by human input: subject=%s want pinned=%s unpinned=%s",
			reviewGate.SubjectHash, expectedSubject, withoutGraphSelection)
	}

	approvals, err := store.List(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	reviewInputCount := 0
	for _, value := range approvals {
		if value.Kind == approval.KindInput && value.FromStage == "review" {
			reviewInputCount++
			if value.ID != reviewInput.ID || value.Status != approval.StatusResolved {
				t.Fatalf("review submission was replaced by a duplicate approval: %+v", value)
			}
		}
	}
	if reviewInputCount != 1 {
		t.Fatalf("expected the original review submission to be reused once, found %d approvals", reviewInputCount)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".ai-team", "artifacts", "feat", "review.md"))
	if err != nil || string(content) != submittedReview {
		t.Fatalf("recovered stage did not use immutable submitted bytes: content=%q err=%v", content, err)
	}
}

func TestHumanInputResumeFromWaitingKeepsForwardGraphSelection(t *testing.T) {
	dir := env(t)
	store, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewFS(fstest.MapFS{
		"spec/def.yaml": def(`name: spec
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs:
  spec: '{feature}/spec.md'
`),
		"review/def.yaml": def(`name: review
runtime: agentcli
prompt_file: prompt.md
mutation: none
inputs:
  spec: '{feature}/spec.md'
outputs:
  review: '{feature}/review.md'
`),
		"delivery/def.yaml": def(`name: delivery
runtime: agentcli
prompt_file: prompt.md
mutation: none
outputs: {}
`),
		"spec/prompt.md":     def("spec"),
		"review/prompt.md":   def("review"),
		"delivery/prompt.md": def("delivery"),
	})
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Template:      "human-waiting-forward-handoff-test",
		Title:         "Human waiting forward handoff test",
		Stages: []config.TemplateStage{
			{ID: "spec", Title: "Specification", Function: "product_owner", Result: "md", Executor: "human", Agent: "spec", Confirm: "required"},
			{ID: "review", Title: "Review", Function: "reviewer", Result: "md", Executor: "human", Agent: "review", Confirm: "required"},
			{ID: "delivery", Title: "Delivery", Function: "operator", Result: "md", Executor: "human", Agent: "delivery", Confirm: "auto"},
		},
	}
	p := New(cfg, registry, WithApprovalStore(store), WithPrompter(&scriptedPrompter{}), WithNotifier(&captureNotifier{}))
	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "Create and review a specification", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first human stage should request its typed result: result=%+v err=%v", first, err)
	}
	specInput, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || specInput.Kind != approval.KindInput {
		t.Fatalf("missing specification input approval: %+v err=%v", specInput, err)
	}
	if _, err := store.Decide(first.RunID, specInput.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: "submit", Comment: "# Original approved specification\n",
		SubjectHash: specInput.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("required graph gate should follow specification submission: %v", err)
	}
	graphApproval, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || graphApproval.Kind != approval.KindApprove || graphApproval.FromStage != "spec" {
		t.Fatalf("missing required forward graph approval: %+v err=%v", graphApproval, err)
	}
	_, manifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(),
		filepath.Join(dir, ".ai-team", "runs", first.RunID), first.RunID, graphApproval.AttemptID)
	if err != nil || len(manifest.Outputs) != 1 {
		t.Fatalf("read specification attempt output: manifest=%+v err=%v", manifest, err)
	}
	output := manifest.Outputs[0]
	_, _, digest, err := evidence.ArtifactDigest(filepath.Join(dir, ".ai-team", "runs", first.RunID, filepath.FromSlash(output.EvidencePath)))
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := humanartifact.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := revisions.Append(first.RunID, output.EvidencePath, "", digest,
		"# Pinned specification revision\n", "Use this exact handoff", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(first.RunID, graphApproval.ID, approval.Decision{
		ActorID: "owner", ActorRole: "product_owner", Action: "approve", Comment: "Continue to review",
		SubjectHash: graphApproval.SubjectHash, ArtifactRevisions: map[string]string{output.EvidencePath: revision.ID},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("second human stage should request its typed result: %v", err)
	}
	reviewInput, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || reviewInput.Kind != approval.KindInput || reviewInput.FromStage != "review" {
		t.Fatalf("missing review input approval: %+v err=%v", reviewInput, err)
	}
	if _, err := store.Decide(first.RunID, reviewInput.ID, approval.Decision{
		ActorID: "reviewer", ActorRole: "reviewer", Action: "submit", Comment: "# Exact human review result\n",
		SubjectHash: reviewInput.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	// This ordinary resume starts from PhaseWaiting with the resolved human
	// input as PendingApprovalID. The graph approval must still supply the
	// selected source revision to the first review attempt and its next gate.
	_, err = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("review should finish and stop at its required graph gate: %v", err)
	}
	reviewGate, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || reviewGate.Kind != approval.KindApprove || reviewGate.FromStage != "review" {
		t.Fatalf("review graph gate missing after waiting resume: %+v err=%v", reviewGate, err)
	}
	replayed, err := evidence.ReplayEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var reviewAttempt *evidence.ReplayedAttempt
	for index := range replayed.Attempts {
		if replayed.Attempts[index].Stage == "review" && replayed.Attempts[index].Executor == "human" {
			reviewAttempt = &replayed.Attempts[index]
			break
		}
	}
	if reviewAttempt == nil || reviewAttempt.HumanInputApprovalID != reviewInput.ID {
		t.Fatalf("review submission was not used for the human attempt: %+v", reviewAttempt)
	}
	_, reviewManifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(),
		filepath.Join(dir, ".ai-team", "runs", first.RunID), first.RunID, reviewAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var sawPinnedRevision bool
	var specInputCount int
	for _, input := range reviewManifest.Inputs {
		if input.Name != "spec" {
			continue
		}
		specInputCount++
		data, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", first.RunID, filepath.FromSlash(input.EvidencePath)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) == "# Pinned specification revision\n" {
			sawPinnedRevision = true
		}
	}
	if specInputCount != 1 || !sawPinnedRevision {
		t.Fatalf("review attempt must receive exactly one graph-pinned spec input without an unpinned duplicate: count=%d pinned=%t inputs=%+v",
			specInputCount, sawPinnedRevision, reviewManifest.Inputs)
	}
	results, _, _, err := replayedStageResults(replayed, filepath.Join(dir, ".ai-team", "runs", first.RunID),
		evidence.FilesystemAttemptManifestSource(), registry, cfg, len(cfg.Stages))
	if err != nil {
		t.Fatal(err)
	}
	expectedSubject, err := (&runState{
		runID: first.RunID, results: results,
		selectedArtifactRevisions: map[string]string{output.EvidencePath: revision.ID},
	}).checkpointSubjectHash("переход review → delivery", "review")
	if err != nil {
		t.Fatal(err)
	}
	withoutGraphSelection, err := (&runState{runID: first.RunID, results: results}).checkpointSubjectHash("переход review → delivery", "review")
	if err != nil {
		t.Fatal(err)
	}
	if reviewGate.SubjectHash != expectedSubject || reviewGate.SubjectHash == withoutGraphSelection {
		t.Fatalf("next gate lost the graph revision selection: subject=%s want pinned=%s unpinned=%s",
			reviewGate.SubjectHash, expectedSubject, withoutGraphSelection)
	}
	if len(reviewManifest.Outputs) != 1 {
		t.Fatalf("review attempt should have one output for the agentless delivery handoff: %+v", reviewManifest.Outputs)
	}
	deliveryOutput := reviewManifest.Outputs[0]
	_, _, deliveryDigest, err := evidence.ArtifactDigest(filepath.Join(dir, ".ai-team", "runs", first.RunID, filepath.FromSlash(deliveryOutput.EvidencePath)))
	if err != nil {
		t.Fatal(err)
	}
	deliveryRevision, err := revisions.Append(first.RunID, deliveryOutput.EvidencePath, "", deliveryDigest,
		"# Pinned review for delivery\n", "Use the pinned review", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(first.RunID, reviewGate.ID, approval.Decision{
		ActorID: "reviewer", ActorRole: "reviewer", Action: "approve", Comment: "Continue to agentless delivery",
		SubjectHash: reviewGate.SubjectHash, ArtifactRevisions: map[string]string{deliveryOutput.EvidencePath: deliveryRevision.ID},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if !errors.As(err, &required) {
		t.Fatalf("agentless human delivery should request its typed result: %v", err)
	}
	deliveryInput, err := store.Load(first.RunID, required.ApprovalID)
	if err != nil || deliveryInput.Kind != approval.KindInput || deliveryInput.FromStage != "delivery" {
		t.Fatalf("agentless delivery input missing after selected graph handoff: %+v err=%v", deliveryInput, err)
	}
	if _, err := store.Decide(first.RunID, deliveryInput.ID, approval.Decision{
		ActorID: "operator", ActorRole: "operator", Action: "submit", Comment: "# Delivery complete\n",
		SubjectHash: deliveryInput.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil || completed.Outcome != workflow.RunCompleted {
		t.Fatalf("agentless delivery did not complete with its human result: result=%+v err=%v", completed, err)
	}
	_, deliveryManifest, err := evidence.ReadAttemptManifest(evidence.FilesystemAttemptManifestSource(),
		filepath.Join(dir, ".ai-team", "runs", first.RunID), first.RunID, deliveryInput.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var selectedDeliveryInputCount int
	for _, input := range deliveryManifest.Inputs {
		if input.Name != "graph-selected-review" {
			continue
		}
		selectedDeliveryInputCount++
		data, readErr := os.ReadFile(filepath.Join(dir, ".ai-team", "runs", first.RunID, filepath.FromSlash(input.EvidencePath)))
		if readErr != nil || string(data) != "# Pinned review for delivery\n" {
			t.Fatalf("agentless human stage lost its distinctly named selected input: content=%q err=%v", data, readErr)
		}
	}
	if selectedDeliveryInputCount != 1 {
		t.Fatalf("agentless human stage should receive exactly one selected graph input, got %d: %+v",
			selectedDeliveryInputCount, deliveryManifest.Inputs)
	}
}
