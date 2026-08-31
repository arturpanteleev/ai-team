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
