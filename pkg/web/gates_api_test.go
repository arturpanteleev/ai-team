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
	actions := []string{"new_version", "override", "return"}
	targets := map[string]string{"new_version": "build", "override": "build", "return": "build"}
	if retryable {
		actions = append(actions, approval.ActionRetryGate)
		targets[approval.ActionRetryGate] = "build"
	}
	payload, _ := json.Marshal(approval.CheckDecisionPayload{
		Kind: string(approval.KindCheckDecision), StageID: "build", CheckKind: "hard", Status: status,
		ArtifactSHA256: gatesTestSHA1, RetryableGates: map[bool][]string{true: {"second"}, false: nil}[retryable],
	})
	return approval.PendingApproval{
		Kind: approval.KindCheckDecision, ID: "check-1", RunID: "run", AttemptID: "att-1", FromStage: "build", ToStage: "build",
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
	key := gatesched.WaveKey{TaskID: "run", Stage: "build", Visit: 1, Scope: "att-" + sha[:1], ArtifactSHA256: sha}
	ref := workflow.GateRef{Gate: gate, Version: 1, Name: name, Mode: mode}
	started := time.Now().UTC().Add(-time.Minute)
	attempt := gatesched.NewAttempt(key, ref, workflow.GateDefinition{ID: gate, Version: 1, Kind: workflow.GateKindBuiltin, Title: gate}, group, n, started)
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
