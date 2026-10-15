package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/agent"
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/control"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gates_e2e_test.go — the administrator override end to end (S06): the HTTP
// API, the real task controller, the real approval store and the real pipeline.
// Nothing between the request and the evidence is faked except the model.

// resultWithoutSection is a stage result that fails the required section gate.
type resultWithoutSection struct{}

func (resultWithoutSection) Execute(_ context.Context, a *runtime.Agent, task *runtime.Task, _ []runtime.Artifact) error {
	for _, outPath := range a.Outputs {
		full := filepath.Join(task.ArtifactRoot, runtime.ReplaceVars(outPath, task.Feature))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte("# Решение\n\nбез нужного раздела\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type e2eNotifier struct{}

func (e2eNotifier) Notify(context.Context, notifier.StageResult) error { return nil }

func e2eConfig() *config.Config {
	return &config.Config{
		SchemaVersion:  config.CurrentSchemaVersion,
		Template:       "gates-e2e",
		Title:          "Gates e2e",
		PipelineAgents: []config.AgentConfig{{Name: "tech"}},
		Stages: []config.TemplateStage{{
			ID: "tech", Title: "Техническое решение", Function: "architect", Result: "md", Executor: "agent", Agent: "tech", Confirm: "auto",
			ExitGateGroups: workflow.GateGroups{{
				{Gate: gatelib.GateRequiredSections, Version: 1, Params: map[string]any{"sections": []any{"Критерии приёмки"}}},
				{Gate: gatelib.GateVerdictMarker, Version: 1, Mode: workflow.GateModeAdvisory},
			}},
		}},
		CLI: "opencode",
	}
}

func e2eRegistry() *agent.Registry {
	return agent.NewFS(fstest.MapFS{
		"tech/def.yaml":  &fstest.MapFile{Data: []byte("name: tech\nruntime: agentcli\nprompt_file: prompt.md\nmutation: none\ninputs:\n  task: tasks/{feature}/task.md\noutputs:\n  result: '{feature}/tech.md'\n")},
		"tech/prompt.md": &fstest.MapFile{Data: []byte("test")},
	})
}

func (f functionPermissionFixture) e2eEvents(t *testing.T, runID string) []evidence.Event {
	t.Helper()
	events, err := evidence.VerifyEventLogWithSource(filepath.Join(f.target, ".ai-team", "runs", runID, "events.jsonl"), runID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestGateOverrideEndToEnd_HTTPControllerPipeline(t *testing.T) {
	cfg := e2eConfig()
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	f := newTemplateFixture(t, string(data), false, nil, map[string]lifecycle.State{})
	options := func() []pipeline.Option {
		return []pipeline.Option{
			pipeline.WithRuntimeFactory(func(string) (runtime.Runtime, error) { return resultWithoutSection{}, nil }),
			pipeline.WithNotifier(e2eNotifier{}),
			pipeline.WithRecorder(NewStoreRecorder(f.srv.store)),
		}
	}
	taskDir := filepath.Join(f.target, ".ai-team", "artifacts", "tasks", "feat")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte("задача"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The first run fails the required gate and parks on the check decision.
	// Both the first run and the resume go through the real run engine, which
	// pins the template of the task exactly as in production.
	engine := pipeline.NewRunEngine(pipeline.New(cfg, e2eRegistry(), options()...))
	started, startErr := engine.Start(context.Background(), pipeline.RunConfig{Feature: "feat", TaskDesc: "задача", TargetDir: f.target, ApproveGates: true})
	if startErr == nil || !errors.Is(startErr, pipeline.ErrUserStopped) {
		t.Fatalf("a failed required gate must park the run: %v", startErr)
	}
	runID := started.RunID
	controller, err := control.New(engine, f.target)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.controller = controller

	approvals, err := approval.NewStore(f.target)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := approvals.List(runID)
	if err != nil || len(pending) == 0 {
		t.Fatalf("approvals: %v %v", pending, err)
	}
	var check approval.PendingApproval
	for _, value := range pending {
		if value.Kind == approval.KindCheckDecision {
			check = value
		}
	}
	if check.ID == "" {
		t.Fatalf("no check decision: %+v", pending)
	}
	path := "/api/runs/" + runID + "/stages/tech/override"
	allFunctions := []cloudidentity.Role{
		cloudidentity.RoleProductOwner, cloudidentity.RoleArchitect, cloudidentity.RoleDeveloper,
		cloudidentity.RoleReviewer, cloudidentity.RoleQA, cloudidentity.RoleReleaseManager,
	}
	admin := []cloudidentity.Role{cloudidentity.RoleAdmin}
	stillWaiting := func(step string) {
		t.Helper()
		states, err := lifecycle.NewStore(f.target)
		if err != nil {
			t.Fatal(err)
		}
		state, err := states.Load(runID)
		if err != nil || state.Phase != lifecycle.PhaseWaiting {
			t.Fatalf("%s: the task must stay parked: %+v %v", step, state, err)
		}
	}

	// A product owner with every function is refused.
	if response := f.requestAs(t, "po@example.test", allFunctions, http.MethodPost, path, `{"reason":"нужно"}`); response.Code != http.StatusForbidden {
		t.Fatalf("PO must get 403: %d %s", response.Code, response.Body.String())
	}
	stillWaiting("PO override")
	// An administrator without a reason gets a validation error.
	if response := f.requestAs(t, "admin-1", admin, http.MethodPost, path, `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("a reasonless override must be 400: %d %s", response.Code, response.Body.String())
	}
	stillWaiting("reasonless override")

	// The CLI or a direct store write cannot stand in for the right: the
	// approval store refuses an override that the server did not confirm, and
	// nothing is resolved.
	for name, decision := range map[string]approval.Decision{
		"cli": {ActorID: "anyone", ActorRole: "architect", Action: "override", Comment: "x", SubjectHash: check.SubjectHash},
		"claimed authority without the controller": {ActorID: "anyone", ActorRole: "architect", Action: "override", Comment: "x", SubjectHash: check.SubjectHash, Authority: approval.AuthoritySystemAdmin},
	} {
		if _, err := approvals.Decide(runID, check.ID, decision); err == nil {
			t.Fatalf("%s: the override without the confirmed authority must be refused", name)
		}
	}
	stillWaiting("CLI-style override")
	if events := f.e2eEvents(t, runID); len(eventsOfTypeWeb(events, "check_overridden")) != 0 {
		t.Fatal("no override may be recorded yet")
	}

	// An administrator with a reason lets the task on.
	if response := f.requestAs(t, "admin-1", admin, http.MethodPost, path, `{"reason":"риск принят, релиз срочный"}`); response.Code != http.StatusAccepted {
		t.Fatalf("admin override: %d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		states, err := lifecycle.NewStore(f.target)
		if err != nil {
			t.Fatal(err)
		}
		state, err := states.Load(runID)
		if err == nil && state.Phase != lifecycle.PhaseWaiting && state.Phase != lifecycle.PhaseRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the override did not carry the task on: %+v %v", state, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Evidence: an immutable record with the confirmed authority and the
	// failed gate; the gate outcome itself is unchanged.
	events := f.e2eEvents(t, runID)
	overrides := eventsOfTypeWeb(events, "check_overridden")
	if len(overrides) != 1 {
		t.Fatalf("one override record expected: %d", len(overrides))
	}
	record, ok := gatesched.DecodeOverride(overrides[0])
	if !ok || record.Authority != approval.AuthoritySystemAdmin || record.ActorID != "admin-1" || record.Reason != "риск принят, релиз срочный" ||
		record.ArtifactSHA256 == "" || len(record.Gates) != 1 || record.Gates[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("override evidence = %+v", record)
	}

	// API: the gate stays failed, the override is visible with its author.
	response := f.requestAs(t, "admin-1", admin, http.MethodGet, "/api/runs/"+runID+"/gates", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET gates: %d %s", response.Code, response.Body.String())
	}
	var view taskGatesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Stages) != 1 || view.Stages[0].Current == nil {
		t.Fatalf("gates view = %s", response.Body.String())
	}
	current := view.Stages[0].Current
	var required *gatesched.GateStatus
	for index := range current.Gates {
		if current.Gates[index].Gate == gatelib.GateRequiredSections {
			required = &current.Gates[index]
		}
	}
	if required == nil || required.Attempt == nil || required.Attempt.Verdict != workflow.GateVerdictFail ||
		required.Attempt.Execution != workflow.GateExecutionSucceeded || !required.Overridden {
		t.Fatalf("the gate must stay a fail and be marked overridden: %+v", required)
	}
	if !current.Resolution.Allowed || current.Resolution.State != gatesched.ResolutionOverridden || current.Override == nil ||
		current.Override.Authority != approval.AuthoritySystemAdmin || len(view.Overrides) != 1 {
		t.Fatalf("resolution/override view = %+v", current)
	}
	if !strings.Contains(response.Body.String(), "admin-1") {
		t.Fatal("the author must be visible to the UI")
	}
}

func eventsOfTypeWeb(events []evidence.Event, eventType string) []evidence.Event {
	var out []evidence.Event
	for _, event := range events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}
