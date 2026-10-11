package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

func TestChangeStageExecutorBindsSelectionToCurrentReadyApproval(t *testing.T) {
	target := t.TempDir()
	artifactRoot := filepath.Join(target, ".ai-team", "artifacts")
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		t.Fatal(err)
	}
	const runID, approvalID, stageID = "run-executor", "approval-ready", "implementation"
	template := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion, Template: "executor-switch", Title: "Executor switch",
		Stages: []config.TemplateStage{{ID: stageID, Title: "Implementation", Function: "developer", Result: "link", Executor: "human", Agent: "coder"}},
	}
	data, err := template.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	templates, err := config.NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templates.PinDataForRun(runID, "task-executor", data); err != nil {
		t.Fatal(err)
	}
	states, err := lifecycle.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := states.Create(lifecycle.State{
		RunID: runID, Feature: "feat", TargetDir: target, Task: "Implement feature", Phase: lifecycle.PhaseWaiting,
		NextStage: stageID, PendingApprovalID: approvalID, ConfigSHA256: strings.Repeat("a", 64),
		WorkflowSHA256: strings.Repeat("b", 64), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	controller := &fakeRunController{approvals: []approval.PendingApproval{{
		ID: approvalID, RunID: runID, Status: approval.StatusPending,
		Targets: map[string]string{"approve": stageID}, RequiredRoles: []string{"developer"},
	}}}
	srv, err := NewServer(":memory:", "", artifactRoot, WithTargetDir(target), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	request := authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+runID+"/executor", `{"stage_id":"implementation","executor":"agent"}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("executor change: %d %s", response.Code, response.Body.String())
	}
	state, err := states.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	selected := state.ExecutorOverrides[stageID]
	if selected.Executor != "agent" || selected.PreviousExecutor != "human" || selected.ActorID != "local-user" ||
		selected.ApprovalID != approvalID || selected.VisitID != approvalID || selected.ChangedAt.IsZero() {
		t.Fatalf("executor override lost ready visit identity: %+v", selected)
	}

	controller.approvals[0].ID = "approval-next-visit"
	request = authorizedRequest(t, srv, http.MethodPost, "/api/runs/"+runID+"/executor", `{"stage_id":"implementation","executor":"human"}`)
	response = httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale approval must not accept executor change: %d %s", response.Code, response.Body.String())
	}
}
