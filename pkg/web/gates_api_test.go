package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/gatesched"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gates_api_test.go — advisory warnings, technical retries and the
// administrator override in the API (S06).

const gatesAPITemplate = `schema_version: 6
template: gates-api
title: Gates API
forbid_self_approval: %FORBID%
stages:
  - id: build
    title: Build
    function: developer
    result: md
    exit_gate_groups:
      - - {gate: required_sections, version: 1, params: {sections: [A]}}
        - {gate: verdict_marker, version: 1, mode: advisory}
      - - {gate: required_sections, version: 1, name: second, params: {sections: [B]}}
  - id: done
    title: Done
    function: bo
    result: approve
`

// gatesAPIWorkflowTemplate pins a v7 config whose workflow declares both an
// entry and an exit chain; the stage has its own exit gate as well.
const gatesAPIWorkflowTemplate = `schema_version: 7
template: gates-api-workflow
title: Gates API Workflow
workflow: gates-api-flow
workflows:
  - id: gates-api-flow
    title: Gates API flow
    version: 1
    graphs:
      - id: sdlc
        template: gates-api-workflow
    entry_gate_groups:
      - - {gate: verdict_marker, version: 1, name: wf_entry_marker}
    exit_gate_groups:
      - - {gate: required_sections, version: 1, name: wf_exit_sections, params: {sections: [X]}}
stages:
  - id: build
    title: Build
    function: developer
    result: md
    input_artifacts: [md]
    exit_gate_groups:
      - - {gate: required_sections, version: 1, name: stage_sections, params: {sections: [Y]}}
  - id: done
    title: Done
    function: bo
    result: approve
`

const (
	gatesTestSHA1 = "1111111111111111111111111111111111111111111111111111111111111111"
	gatesTestSHA2 = "2222222222222222222222222222222222222222222222222222222222222222"
)

// retryRecorder is a controller that accepts the gate retry and the override.
type retryRecorder struct {
	*stageActionRecorder
	retries   [][]string
	overrides int
}

func (c *retryRecorder) RetryStageGates(_ context.Context, _, _ string, gates []string, _, _, _ string) error {
	c.retries = append(c.retries, gates)
	return nil
}

func (c *retryRecorder) OverrideStageCheck(context.Context, string, string, string, string, string) error {
	c.overrides++
	return nil
}

func checkDecisionFor(status string, retryable bool) approval.PendingApproval {
	retryableGates := []string(nil)
	if retryable {
		retryableGates = []string{"second"}
	}
	return checkDecisionForGates("build", gatesTestSHA1, status, retryableGates)
}

func checkDecisionForGates(stageID, sha, status string, retryableGates []string) approval.PendingApproval {
	actions := []string{"new_version", "override", "return"}
	targets := map[string]string{"new_version": stageID, "override": stageID, "return": stageID}
	if len(retryableGates) > 0 {
		actions = append(actions, approval.ActionRetryGate)
		targets[approval.ActionRetryGate] = stageID
	}
	payload, _ := json.Marshal(approval.CheckDecisionPayload{
		Kind: string(approval.KindCheckDecision), StageID: stageID, CheckKind: "hard", Status: status,
		ArtifactSHA256: sha, RetryableGates: retryableGates,
	})
	return approval.PendingApproval{
		Kind: approval.KindCheckDecision, ID: "check-1", RunID: "run", AttemptID: "att-1", FromStage: stageID, ToStage: stageID,
		Trigger: approval.StageCheckTrigger, Status: approval.StatusPending, SubjectHash: testSubjectHash,
		RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny, Actions: actions, Targets: targets,
		CreatedAt: time.Now().UTC(), Payload: payload,
	}
}

type gatesFixture struct {
	functionPermissionFixture
	recorder *retryRecorder
}

func newGatesFixture(t *testing.T, retryable bool) gatesFixture {
	t.Helper()
	base := newTemplateFixture(t, gatesAPITemplate, false,
		[]approval.PendingApproval{checkDecisionFor("error", retryable)},
		map[string]lifecycle.State{"run": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "check-1"}})
	recorder := &retryRecorder{stageActionRecorder: base.controller}
	base.srv.controller = recorder
	return gatesFixture{functionPermissionFixture: base, recorder: recorder}
}

func newWorkflowGatesFixture(t *testing.T, retryableGates []string) gatesFixture {
	t.Helper()
	base := newTemplateFixture(t, gatesAPIWorkflowTemplate, false,
		[]approval.PendingApproval{checkDecisionForGates("build", gatesTestSHA1, "error", retryableGates)},
		map[string]lifecycle.State{"run": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "check-1"}})
	recorder := &retryRecorder{stageActionRecorder: base.controller}
	base.srv.controller = recorder
	return gatesFixture{functionPermissionFixture: base, recorder: recorder}
}

func (f gatesFixture) journal() *gatesched.EventJournal {
	return gatesched.NewEventJournal(gatesched.EventSink{
		Read: func() ([]evidence.Event, bool, error) { return nil, false, nil },
		Append: func(event evidence.Event) error {
			encoded, err := json.Marshal(event.Data)
			if err != nil {
				return err
			}
			return f.srv.store.AppendEventNext(&store.Event{RunID: "run", Type: event.Type, Stage: event.Stage,
				AttemptID: event.AttemptID, Timestamp: event.Timestamp, DataJSON: string(encoded)})
		},
	})
}

func (f gatesFixture) attempt(t *testing.T, sha, name, gate string, mode workflow.GateMode, group, n int,
	execution workflow.GateExecution, verdict workflow.GateVerdict) workflow.GateAttempt {
	t.Helper()
	return f.attemptWithPhase(t, "", sha, name, gate, mode, group, n, execution, verdict)
}

func (f gatesFixture) attemptWithPhase(t *testing.T, phase workflow.GatePhase, sha, name, gate string, mode workflow.GateMode, group, n int,
	execution workflow.GateExecution, verdict workflow.GateVerdict) workflow.GateAttempt {
	t.Helper()
	key := gatesched.WaveKey{TaskID: "run", Stage: "build", Visit: 1, Scope: "att-" + sha[:1], ArtifactSHA256: sha, Phase: phase}
	ref := workflow.GateRef{Gate: gate, Version: 1, Name: name, Mode: mode}
	started := time.Now().UTC().Add(-time.Minute)
	attempt := gatesched.NewAttempt(key, ref, workflow.GateDefinition{ID: gate, Version: 1, Kind: workflow.GateKindBuiltin, Title: gate}, group, n, started)
	attempt.Phase = phase.Effective()
	journal := f.journal()
	if err := journal.Record(gatesched.Record{Type: gatesched.RecordStarted, Key: key, Attempt: attempt}); err != nil {
		t.Fatal(err)
	}
	attempt.Execution, attempt.Verdict, attempt.FinishedAt = execution, verdict, started.Add(time.Second)
	attempt.Reason = "причина " + name
	if err := journal.Record(gatesched.Record{Type: gatesched.RecordFinished, Key: key, Attempt: attempt}); err != nil {
		t.Fatal(err)
	}
	return attempt
}

func (f gatesFixture) get(t *testing.T) taskGatesResponse {
	t.Helper()
	response := f.requestAs(t, "admin-1", []cloudidentity.Role{cloudidentity.RoleAdmin}, http.MethodGet, "/api/runs/run/gates", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET gates: %d %s", response.Code, response.Body.String())
	}
	var decoded taskGatesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func (f gatesFixture) gate(stage gateStageView, name string) gatesched.GateStatus {
	for _, status := range stage.Current.Gates {
		if status.GateName == name {
			return status
		}
	}
	return gatesched.GateStatus{}
}

func gatesStage(view taskGatesResponse, id string) gateStageView {
	for _, stage := range view.Stages {
		if stage.StageID == id {
			return stage
		}
	}
	return gateStageView{}
}

// Acceptance: the API shows real gates, an advisory fail as a warning that does
// not block, and the aggregated transition decision with its reason.
func TestGatesAPI_ShowsAdvisoryWarningAndTransitionDecision(t *testing.T) {
	f := newGatesFixture(t, false)
	f.attempt(t, gatesTestSHA1, "required_sections", "required_sections", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
	f.attempt(t, gatesTestSHA1, "verdict_marker", "verdict_marker", workflow.GateModeAdvisory, 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	second := f.attempt(t, gatesTestSHA1, "second", "required_sections", "", 1, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
	_ = second

	view := f.get(t)
	if len(view.Stages) != 1 || view.Stages[0].StageID != "build" || view.Stages[0].Current == nil {
		t.Fatalf("stages = %+v", view.Stages)
	}
	stage := view.Stages[0]
	if len(stage.Groups) != 2 || stage.Groups[0][0] != "required_sections" || stage.Groups[1][0] != "second" {
		t.Fatalf("groups = %v", stage.Groups)
	}
	if !stage.Current.Resolution.Allowed || stage.Current.Resolution.State != gatesched.ResolutionPassedWithWarnings {
		t.Fatalf("an advisory fail must not block: %+v", stage.Current.Resolution)
	}
	marker := f.gate(stage, "verdict_marker")
	if !marker.Warning || marker.Blocking || marker.Mode != workflow.GateModeAdvisory || marker.Attempt.Verdict != workflow.GateVerdictFail {
		t.Fatalf("the advisory gate keeps its fail and warns: %+v", marker)
	}
	if len(view.Warnings) != 1 || view.Warnings[0].Gate != "verdict_marker" || view.Warnings[0].Verdict != workflow.GateVerdictFail {
		t.Fatalf("warnings = %+v", view.Warnings)
	}
	if !view.CanOverride {
		t.Fatal("an administrator must see can_override")
	}
}

// Acceptance: a blocked version is blocked until an administrator overrides it;
// the override leaves the outcome red, is visible everywhere, and covers only
// the version it was made for.
func TestGatesAPI_OverrideKeepsTheOutcomeAndIsBoundToTheVersion(t *testing.T) {
	f := newGatesFixture(t, false)
	f.attempt(t, gatesTestSHA1, "required_sections", "required_sections", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
	failed := f.attempt(t, gatesTestSHA1, "verdict_marker", "verdict_marker", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)

	blocked := f.get(t).Stages[0].Current
	if blocked.Resolution.Allowed || blocked.Resolution.State != gatesched.ResolutionBlocked {
		t.Fatalf("a failed required gate blocks: %+v", blocked.Resolution)
	}
	if len(blocked.Resolution.Reasons) == 0 {
		t.Fatal("the decision must say why")
	}

	event := evidence.Event{
		Type: gatesched.EventCheckOverridden, Stage: "build", AttemptID: "att-1", Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"reason": "срочный релиз", "actor_id": "admin-1", "authority": gatesched.AuthoritySystemAdmin,
			"artifact_sha256": gatesTestSHA1, "visit": 1, "approval_id": "check-1", "outcome": "fail",
			"gates": gatesched.OverriddenGates([]workflow.GateAttempt{failed}),
		},
	}
	encoded, _ := json.Marshal(event.Data)
	if err := f.srv.store.AppendEventNext(&store.Event{RunID: "run", Type: event.Type, Stage: event.Stage,
		AttemptID: event.AttemptID, Timestamp: event.Timestamp, DataJSON: string(encoded)}); err != nil {
		t.Fatal(err)
	}

	view := f.get(t)
	current := view.Stages[0].Current
	if !current.Resolution.Allowed || current.Resolution.State != gatesched.ResolutionOverridden || current.Override == nil {
		t.Fatalf("the override allows this version: %+v", current.Resolution)
	}
	marker := f.gate(view.Stages[0], "verdict_marker")
	if !marker.Overridden || marker.Attempt.Verdict != workflow.GateVerdictFail || marker.Attempt.Execution != workflow.GateExecutionSucceeded {
		t.Fatalf("the gate must stay red under an override: %+v", marker)
	}
	if len(view.Overrides) != 1 || view.Overrides[0].ActorID != "admin-1" || view.Overrides[0].Reason != "срочный релиз" ||
		view.Overrides[0].ArtifactSHA256 != gatesTestSHA1 || view.Overrides[0].Gates[0].AttemptID != failed.AttemptID {
		t.Fatalf("overrides = %+v", view.Overrides)
	}

	// A new version of the result fails again: the old override does not apply.
	f.attempt(t, gatesTestSHA2, "required_sections", "required_sections", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
	f.attempt(t, gatesTestSHA2, "verdict_marker", "verdict_marker", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictFail)
	view = f.get(t)
	stage := view.Stages[0]
	if len(stage.Waves) != 2 || !stage.Waves[1].Current || stage.Waves[0].Current {
		t.Fatalf("two versions, the newer one current: %+v", stage.Waves)
	}
	if stage.Current.Resolution.Allowed || stage.Current.Override != nil {
		t.Fatalf("an override of one version must not allow another: %+v", stage.Current.Resolution)
	}
	if stage.Waves[0].Override == nil || stage.Waves[0].Resolution.State != gatesched.ResolutionOverridden {
		t.Fatalf("the old version keeps its history: %+v", stage.Waves[0])
	}
}

// Acceptance: a non-administrator, a product owner holding every process
// function included, gets 403; an administrator without a reason gets a
// validation error; with a reason the override reaches the controller.
func TestGateOverrideEndpoint_AdminOnlyWithMandatoryReason(t *testing.T) {
	path := "/api/runs/run/stages/build/override"
	allFunctions := []cloudidentity.Role{
		cloudidentity.RoleProductOwner, cloudidentity.RoleArchitect, cloudidentity.RoleDeveloper,
		cloudidentity.RoleReviewer, cloudidentity.RoleQA, cloudidentity.RoleReleaseManager,
	}
	t.Run("product owner with every function is forbidden", func(t *testing.T) {
		f := newGatesFixture(t, false)
		for _, body := range []string{`{"reason":"нужно"}`, `{}`} {
			response := f.requestAs(t, "po@example.test", allFunctions, http.MethodPost, path, body)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "admin_required") {
				t.Fatalf("PO must get 403 admin_required (body %s): %d %s", body, response.Code, response.Body.String())
			}
		}
		if f.recorder.overrides != 0 || f.controller.decisionCalls != 0 {
			t.Fatal("a forbidden override must not reach the controller")
		}
	})
	t.Run("administrator without a reason gets a validation error", func(t *testing.T) {
		f := newGatesFixture(t, false)
		for _, body := range []string{`{}`, `{"reason":"   "}`} {
			response := f.requestAs(t, "admin-1", []cloudidentity.Role{cloudidentity.RoleAdmin}, http.MethodPost, path, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "reason_required") {
				t.Fatalf("a reasonless override must be a validation error: %d %s", response.Code, response.Body.String())
			}
		}
		if f.recorder.overrides != 0 {
			t.Fatal("a reasonless override must not reach the controller")
		}
	})
	t.Run("administrator with a reason overrides", func(t *testing.T) {
		f := newGatesFixture(t, false)
		response := f.requestAs(t, "admin-1", []cloudidentity.Role{cloudidentity.RoleAdmin}, http.MethodPost, path, `{"reason":"риск принят"}`)
		if response.Code != http.StatusAccepted || f.recorder.overrides != 1 {
			t.Fatalf("override: %d %s overrides=%d", response.Code, response.Body.String(), f.recorder.overrides)
		}
	})
	t.Run("the generic decision endpoint is not a way around the right", func(t *testing.T) {
		f := newGatesFixture(t, false)
		body := `{"action":"override","comment":"обход","subject_hash":"` + testSubjectHash + `"}`
		response := f.requestAs(t, "po@example.test", allFunctions, http.MethodPost, "/api/runs/run/approvals/check-1/decisions", body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("a PO must not override through the generic endpoint: %d %s", response.Code, response.Body.String())
		}
		response = f.requestAs(t, "admin-1", []cloudidentity.Role{cloudidentity.RoleAdmin}, http.MethodPost, "/api/runs/run/approvals/check-1/decisions", body)
		if response.Code != http.StatusConflict {
			t.Fatalf("even an administrator uses the stage endpoint: %d %s", response.Code, response.Body.String())
		}
		if f.controller.decisionCalls != 0 || f.recorder.overrides != 0 {
			t.Fatal("the generic endpoint must not resolve a check decision")
		}
	})
}

func TestGateRetryEndpoint_ByGateAndByAttempt(t *testing.T) {
	f := newGatesFixture(t, true)
	failed := f.attempt(t, gatesTestSHA1, "second", "required_sections", "", 1, 1, workflow.GateExecutionError, "")

	response := f.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
		"/api/runs/run/stages/build/gates/second/retry", "")
	if response.Code != http.StatusAccepted || len(f.recorder.retries) != 1 || f.recorder.retries[0][0] != "second" {
		t.Fatalf("retry by gate: %d %s retries=%v", response.Code, response.Body.String(), f.recorder.retries)
	}
	response = f.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
		"/api/runs/run/gate-attempts/"+failed.AttemptID+"/retry", `{"reason":"сеть восстановлена"}`)
	if response.Code != http.StatusAccepted || len(f.recorder.retries) != 2 || f.recorder.retries[1][0] != "second" {
		t.Fatalf("retry by attempt: %d %s retries=%v", response.Code, response.Body.String(), f.recorder.retries)
	}
	for _, tc := range []struct {
		name  string
		roles []cloudidentity.Role
		path  string
		code  int
	}{
		{"a foreign function is forbidden", []cloudidentity.Role{cloudidentity.RoleQA}, "/api/runs/run/stages/build/gates/second/retry", http.StatusForbidden},
		{"an unknown gate is not found", []cloudidentity.Role{cloudidentity.RoleDeveloper}, "/api/runs/run/stages/build/gates/ghost/retry", http.StatusNotFound},
		{"an unknown attempt is not found", []cloudidentity.Role{cloudidentity.RoleDeveloper}, "/api/runs/run/gate-attempts/nope/retry", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.recorder.retries)
			response := f.requestAs(t, "member@example.test", tc.roles, http.MethodPost, tc.path, "")
			if response.Code != tc.code || len(f.recorder.retries) != before {
				t.Fatalf("%d %s retries=%d", response.Code, response.Body.String(), len(f.recorder.retries))
			}
		})
	}
}

func TestGateRetryAdvertisedInAllowedActionsOnlyWhenOffered(t *testing.T) {
	for _, retryable := range []bool{true, false} {
		f := newGatesFixture(t, retryable)
		details, status, err := f.srv.loadTaskRunDetails("run")
		if err != nil || status != http.StatusOK {
			t.Fatalf("details: %d %v", status, err)
		}
		has := false
		for _, action := range details.AllowedActions {
			if action == "gate-retry" {
				has = true
			}
		}
		if has != retryable {
			t.Fatalf("retryable=%v allowed actions=%v", retryable, details.AllowedActions)
		}
	}
}

// A retry button is offered only for a gate the pending decision really offers
// to retry: a technically failed advisory gate, or a gate of a version without
// such an offer, gets none (the retry would end in a conflict).
func TestGatesAPI_RetryableOnlyWhenTheDecisionOffersIt(t *testing.T) {
	for _, offered := range []bool{true, false} {
		f := newGatesFixture(t, offered)
		f.attempt(t, gatesTestSHA1, "required_sections", "required_sections", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
		f.attempt(t, gatesTestSHA1, "verdict_marker", "verdict_marker", workflow.GateModeAdvisory, 0, 1, workflow.GateExecutionError, "")
		f.attempt(t, gatesTestSHA1, "second", "required_sections", "", 1, 1, workflow.GateExecutionError, "")
		stage := f.get(t).Stages[0]
		if marker := f.gate(stage, "verdict_marker"); marker.Retryable {
			t.Fatalf("an advisory gate with an error has no retry: %+v", marker)
		}
		if second := f.gate(stage, "second"); second.Retryable != offered {
			t.Fatalf("offered=%v but second.Retryable=%v", offered, second.Retryable)
		}
	}
}

// A config without workflow chains keeps its view: no entry groups, the same
// stage groups and the effective exit phase on its waves.
func TestGatesAPI_StageOnlyConfigKeepsItsShape(t *testing.T) {
	f := newGatesFixture(t, false)
	f.attempt(t, gatesTestSHA1, "required_sections", "required_sections", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)

	stage := gatesStage(f.get(t), "build")
	if len(stage.EntryGroups) != 0 {
		t.Fatalf("a stage-only config has no entry chain: %v", stage.EntryGroups)
	}
	if len(stage.Groups) != 2 || stage.Groups[0][0] != "required_sections" || stage.Groups[1][0] != "second" {
		t.Fatalf("stage groups changed: %v", stage.Groups)
	}
	if len(stage.Waves) != 1 || stage.Waves[0].Phase != workflow.GatePhaseExit {
		t.Fatalf("an old wave is the effective exit chain: %+v", stage.Waves)
	}
}

// A workflow-level exit gate is part of the effective exit chain: it is shown
// in the API and its technical failure is retriable through the stage endpoint.
func TestGatesAPI_WorkflowExitGateIsVisibleAndRetryable(t *testing.T) {
	f := newWorkflowGatesFixture(t, []string{"wf_exit_sections"})
	f.attempt(t, gatesTestSHA1, "stage_sections", "required_sections", "", 0, 1, workflow.GateExecutionSucceeded, workflow.GateVerdictPass)
	failed := f.attempt(t, gatesTestSHA1, "wf_exit_sections", "required_sections", "", 0, 1, workflow.GateExecutionError, "")

	view := f.get(t)
	stage := gatesStage(view, "build")
	if stage.StageID == "" {
		t.Fatalf("stages = %+v", view.Stages)
	}
	if len(stage.Groups) != 2 || len(stage.Groups[0]) != 1 || stage.Groups[0][0] != "wf_exit_sections" ||
		len(stage.Groups[1]) != 1 || stage.Groups[1][0] != "stage_sections" {
		t.Fatalf("the exit view must merge workflow and stage gates: %v", stage.Groups)
	}
	if stage.Current == nil || stage.Current.Phase != workflow.GatePhaseExit {
		t.Fatalf("current wave must carry the exit phase: %+v", stage.Current)
	}
	status := f.gate(stage, "wf_exit_sections")
	if !status.Retryable || status.Attempt == nil || status.Attempt.AttemptID != failed.AttemptID {
		t.Fatalf("a failed workflow exit gate must be retryable: %+v", status)
	}

	response := f.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
		"/api/runs/run/stages/build/gates/wf_exit_sections/retry", "")
	if response.Code != http.StatusAccepted || len(f.recorder.retries) != 1 || f.recorder.retries[0][0] != "wf_exit_sections" {
		t.Fatalf("retry of a workflow exit gate: %d %s retries=%v", response.Code, response.Body.String(), f.recorder.retries)
	}
}

// An entry gate is a separate wave with its own phase. It must be advertised
// with the entry chain, and must not borrow an exit gate's identity.
func TestGatesAPI_EntryGateIsVisibleWithPhase(t *testing.T) {
	f := newWorkflowGatesFixture(t, nil)
	f.attemptWithPhase(t, workflow.GatePhaseEntry, gatesTestSHA1, "wf_entry_marker", "verdict_marker", "", 0, 1,
		workflow.GateExecutionSucceeded, workflow.GateVerdictFail)

	stage := gatesStage(f.get(t), "build")
	if len(stage.EntryGroups) != 1 || len(stage.EntryGroups[0]) != 1 || stage.EntryGroups[0][0] != "wf_entry_marker" {
		t.Fatalf("entry groups = %v", stage.EntryGroups)
	}
	if len(stage.Waves) != 1 {
		t.Fatalf("waves = %+v", stage.Waves)
	}
	wave := stage.Waves[0]
	if wave.Phase != workflow.GatePhaseEntry || len(wave.Gates) != 1 || wave.Gates[0].GateName != "wf_entry_marker" {
		t.Fatalf("the entry gate must resolve against the entry chain: %+v", wave)
	}

	response := f.requestAs(t, "admin-1", []cloudidentity.Role{cloudidentity.RoleAdmin}, http.MethodGet, "/api/runs/run/gates", "")
	if !strings.Contains(response.Body.String(), `"phase":"entry"`) {
		t.Fatalf("the API must expose the gate phase: %s", response.Body.String())
	}
}

// The retry of an attempt is validated against the chain of that attempt's
// phase: an entry gate cannot hide behind a same-named exit gate and vice versa.
func TestGateRetryByAttemptUsesTheAttemptPhase(t *testing.T) {
	t.Run("entry attempt is validated against the entry chain", func(t *testing.T) {
		f := newWorkflowGatesFixture(t, []string{"wf_entry_marker"})
		attempt := f.attemptWithPhase(t, workflow.GatePhaseEntry, gatesTestSHA1, "wf_entry_marker", "verdict_marker", "", 0, 1,
			workflow.GateExecutionError, "")
		response := f.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
			"/api/runs/run/gate-attempts/"+attempt.AttemptID+"/retry", "")
		if response.Code != http.StatusAccepted || len(f.recorder.retries) != 1 || f.recorder.retries[0][0] != "wf_entry_marker" {
			t.Fatalf("entry attempt retry: %d %s retries=%v", response.Code, response.Body.String(), f.recorder.retries)
		}
	})
	t.Run("an exit gate name in an entry wave is not an entry gate", func(t *testing.T) {
		f := newWorkflowGatesFixture(t, []string{"stage_sections"})
		attempt := f.attemptWithPhase(t, workflow.GatePhaseEntry, gatesTestSHA1, "stage_sections", "required_sections", "", 0, 1,
			workflow.GateExecutionError, "")
		response := f.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
			"/api/runs/run/gate-attempts/"+attempt.AttemptID+"/retry", "")
		if response.Code != http.StatusNotFound || len(f.recorder.retries) != 0 {
			t.Fatalf("an entry attempt must not resolve an exit gate: %d %s retries=%v", response.Code, response.Body.String(), f.recorder.retries)
		}
	})
	t.Run("the named-gate route stays on the exit chain", func(t *testing.T) {
		f := newWorkflowGatesFixture(t, []string{"wf_entry_marker"})
		response := f.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
			"/api/runs/run/stages/build/gates/wf_entry_marker/retry", "")
		if response.Code != http.StatusNotFound || len(f.recorder.retries) != 0 {
			t.Fatalf("an entry gate must not be retried as an exit gate: %d %s retries=%v", response.Code, response.Body.String(), f.recorder.retries)
		}
	})
}
