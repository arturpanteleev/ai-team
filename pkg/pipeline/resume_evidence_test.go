package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

// TestRun_ResumeBlockedRecordsEvent проверяет OPS-3: если evidence
// chain/snapshots активного run'а повреждены, resume fail-closed отклоняется
// и причина явно фиксируется событием resume_blocked (при аппендабельном логе).
func TestRun_ResumeBlockedRecordsEvent(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	rt := newScripted()
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges[0].Approval = &config.WorkflowApprovalConfig{
			Roles: []string{"product_owner"}, Quorum: "any",
			Actions: map[string]string{"approve": "reviewer", "reject": "$stop"},
		}
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))

	first, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тест", TargetDir: dir,
	})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидался pending approval, got result=%+v err=%v", first, err)
	}
	// Разрешаем approval, чтобы resume прошёл мимо approval-гейта и дошёл до
	// fail-closed проверки evidence (иначе resume остановится на pending раньше).
	approvalStore, aerr := approval.NewStore(dir)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if _, derr := approvalStore.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "approve",
		SubjectHash: required.SubjectHash,
	}); derr != nil {
		t.Fatal(derr)
	}

	// Повреждаем config snapshot активного run'а (evidence tamper).
	runDir := filepath.Join(dir, ".ai-team", "runs", first.RunID)
	configPath := filepath.Join(runDir, "config.json")
	if err := os.Chmod(configPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schema_version":1,"galtered":true}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Resume должен fail-closed отклониться.
	if _, err := p.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: first.RunID, TargetDir: dir,
	}); err == nil {
		t.Fatal("resume с повреждённой config snapshot должен отклониться")
	}

	// Причина должна быть явно зафиксирована в evidence событием resume_blocked.
	events, evErr := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), first.RunID)
	if evErr != nil {
		t.Fatalf("event chain должна остаться валидной (аппендабельной): %v", evErr)
	}
	found := false
	for _, ev := range events {
		if ev.Type == "resume_blocked" {
			found = true
			reason, _ := ev.Data["reason"].(string)
			if reason != "config_snapshot" {
				t.Fatalf("resume_blocked reason=%q, ожидали config_snapshot", reason)
			}
			break
		}
	}
	if !found {
		t.Fatalf("событие resume_blocked не записано")
	}
}

type countingEvidenceFactory struct {
	delegate EvidenceStoreFactory
	starts   int
	resumes  int
}

func (f *countingEvidenceFactory) Start(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	f.starts++
	return f.delegate.Start(root, manifest)
}

func (f *countingEvidenceFactory) Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	f.resumes++
	return f.delegate.Resume(root, runID)
}

// The injected factory owns both creation and reopening while the delegated
// filesystem implementation retains the existing hash-chained evidence and
// attempt publication behavior. The seam does not relocate target evidence or
// isolate a worker from it.
func TestRun_EvidenceFactoryUsedForCreateAndResume(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	rt := newScripted()
	rt.content["reviewer"] = map[string]string{"review": "**Verdict:** APPROVED\n"}
	cfg := cfgForGraph(func(wf *config.WorkflowConfig) {
		wf.Edges[0].Approval = &config.WorkflowApprovalConfig{
			Roles: []string{"product_owner"}, Quorum: "any",
			Actions: map[string]string{"approve": "reviewer", "reject": "$stop"},
		}
	}, config.AgentConfig{Name: "analyst"}, config.AgentConfig{Name: "reviewer"})
	factory := &countingEvidenceFactory{delegate: filesystemEvidenceStoreFactory{}}
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}), WithEvidenceStoreFactory(factory))

	first, err := p.RunWithResult(context.Background(), RunConfig{Feature: "feat", TaskDesc: "тест", TargetDir: dir})
	var required *ApprovalRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ожидался pending approval, got result=%+v err=%v", first, err)
	}
	if factory.starts != 1 || factory.resumes != 0 {
		t.Fatalf("после создания вызовы фабрики: starts=%d resumes=%d", factory.starts, factory.resumes)
	}

	approvalStore, err := approval.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvalStore.Decide(first.RunID, required.ApprovalID, approval.Decision{
		ActorID: "product-1", ActorRole: "product_owner", Action: "approve", SubjectHash: required.SubjectHash,
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := p.RunWithResult(context.Background(), RunConfig{ResumeRunID: first.RunID, TargetDir: dir})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.RunID != first.RunID || factory.starts != 1 || factory.resumes != 1 {
		t.Fatalf("resume result/factory calls: result=%+v starts=%d resumes=%d", resumed, factory.starts, factory.resumes)
	}

	events, err := evidence.VerifyEventLog(filepath.Join(dir, ".ai-team", "runs", first.RunID, "events.jsonl"), first.RunID)
	if err != nil {
		t.Fatalf("verify existing event-chain semantics: %v", err)
	}
	seenResume, seenFinish := false, false
	for _, event := range events {
		seenResume = seenResume || event.Type == "run_resumed"
		seenFinish = seenFinish || event.Type == "run_finished"
	}
	if !seenResume || !seenFinish {
		t.Fatalf("expected run_resumed and run_finished evidence events, got %v", eventTypes(events))
	}
}

func eventTypes(events []evidence.Event) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		result = append(result, event.Type)
	}
	return result
}

// TestRun_ResumeBlockedNotAppendedOnTerminal проверяет OPS-3: если resume
// отвергнут с причиной already_terminal (run уже terminal), событие
// resume_blocked НЕ дописывается после run_finished — терминальное событие
// остаётся последним в event log.
func TestRun_ResumeBlockedNotAppendedOnTerminal(t *testing.T) {
	dir := env(t)
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".ai-team/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	rt := newScripted()
	cfg := cfgFor(config.AgentConfig{Name: "analyst"})
	p := New(cfg, testRegistry(), WithRuntimeFactory(rt.factory), WithPrompter(&scriptedPrompter{}))

	// Terminal run до completion.
	result, err := p.RunWithResult(context.Background(), RunConfig{
		Feature: "feat", TaskDesc: "тест", TargetDir: dir,
	})
	if err != nil {
		t.Fatalf("terminal run: %v", err)
	}
	runDir := filepath.Join(dir, ".ai-team", "runs", result.RunID)
	terminal, evErr := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), result.RunID)
	if evErr != nil {
		t.Fatalf("event chain: %v", evErr)
	}
	if len(terminal) == 0 || terminal[len(terminal)-1].Type != "run_finished" {
		t.Fatalf("ожидался последним run_finished, got %d events (last=%q)", len(terminal), lastType(terminal))
	}

	// Resume терминального run должен отклониться (already_terminal).
	if _, err := p.RunWithResult(context.Background(), RunConfig{
		ResumeRunID: result.RunID, TargetDir: dir,
	}); err == nil {
		t.Fatal("resume терминального run должен отклониться")
	}

	// Terminal событие должно остаться последним: resume_blocked НЕ дописывается.
	after, evErr := evidence.VerifyEventLog(filepath.Join(runDir, "events.jsonl"), result.RunID)
	if evErr != nil {
		t.Fatalf("event chain после отклонения resume: %v", evErr)
	}
	if len(after) != len(terminal) {
		t.Fatalf("event log изменился после отклонения resume: было %d, стало %d", len(terminal), len(after))
	}
	if len(after) == 0 || after[len(after)-1].Type != "run_finished" {
		t.Fatalf("после отклонения resume терминальное событие должно остаться последним, got last=%q", lastType(after))
	}
	for _, ev := range after {
		if ev.Type == "resume_blocked" {
			t.Fatalf("resume_blocked не должен дописываться после run_finished")
		}
	}
}

func lastType(events []evidence.Event) string {
	if len(events) == 0 {
		return "<none>"
	}
	return events[len(events)-1].Type
}
