package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

type viewerTaskView struct {
	AllowedActions []string `json:"allowed_actions"`
	ViewerActions  []string `json:"viewer_actions"`
	// ViewerDeniedReasons: action -> reason for policy denials.
	ViewerDeniedReasons map[string]string `json:"viewer_denied_reasons"`
}

func (f functionPermissionFixture) viewerView(t *testing.T, actorID string, roles []cloudidentity.Role, runID string) viewerTaskView {
	t.Helper()
	response := f.requestAs(t, actorID, roles, http.MethodGet, "/api/runs/"+runID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", runID, response.Code, response.Body.String())
	}
	var view viewerTaskView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ViewerActions == nil {
		t.Fatalf("viewer_actions must be a list, got null: %s", response.Body.String())
	}
	return view
}

// viewer_actions is the answer the server would give to the real request: an
// action is listed exactly when the handler does not refuse it with 403.
func TestViewerActionsMatchTheOutcomeOfRealRequests(t *testing.T) {
	now := time.Now().UTC()
	approvals := []approval.PendingApproval{
		buildInputApproval("input-take", approval.StatusPending, now),
		{
			Kind: approval.KindCheckDecision, ID: "check-1", RunID: "run", FromStage: "build", ToStage: "build",
			Trigger: approval.StageCheckTrigger, Status: approval.StatusPending, SubjectHash: testSubjectHash,
			RequiredRoles: []string{"developer"}, Quorum: approval.QuorumAny, Actions: []string{"override", "reject"},
			Targets: map[string]string{"override": "build"}, CreatedAt: now,
		},
		buildConfirmApproval(now.Add(time.Minute)),
	}
	runs := map[string]lifecycle.State{
		"run-input":   {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "input-take"},
		"run-check":   {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "check-1"},
		"run-skip":    {Phase: lifecycle.PhaseResumable, NextStage: "build"},
		"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "accept", PendingApprovalID: "gate-1"},
	}
	type probe struct{ action, runID, path, body string }
	probes := []probe{
		{"take", "run-input", "/api/runs/run-input/stages/build/take", ""},
		{"override", "run-check", "/api/runs/run-check/stages/build/override", `{"reason":"flaky"}`},
		{"skip", "run-skip", "/api/runs/run-skip/stages/build/skip", `{"reason":"not needed"}`},
		// The transition confirmation names the completed stage (build, whose
		// approver is the reviewer function), not the next one (accept).
		{"confirm", "run-confirm", "/api/runs/run-confirm/stages/build/confirm", ""},
		{"return", "run-confirm", "/api/runs/run-confirm/stages/build/return", `{"to_stage":"spec","reason":"gap"}`},
	}
	members := []struct {
		name  string
		roles []cloudidentity.Role
	}{
		{"developer", []cloudidentity.Role{cloudidentity.RoleDeveloper}},
		{"reviewer", []cloudidentity.Role{cloudidentity.RoleReviewer}},
		{"developer and reviewer", []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer}},
		{"product owner", []cloudidentity.Role{cloudidentity.RoleProductOwner}},
		{"admin", []cloudidentity.Role{cloudidentity.RoleAdmin}},
		{"qa", []cloudidentity.Role{cloudidentity.RoleQA}},
	}
	for _, member := range members {
		for _, current := range probes {
			t.Run(member.name+"/"+current.action, func(t *testing.T) {
				fixture := newFunctionPermissionFixture(t, false, append([]approval.PendingApproval(nil), approvals...), runs)
				view := fixture.viewerView(t, "member@example.test", member.roles, current.runID)
				for _, action := range view.ViewerActions {
					if !slices.Contains(view.AllowedActions, action) {
						t.Fatalf("viewer action %q is not in allowed_actions %v", action, view.AllowedActions)
					}
				}
				if !slices.Contains(view.AllowedActions, current.action) {
					t.Fatalf("fixture must offer %q, allowed=%v", current.action, view.AllowedActions)
				}
				response := fixture.requestAs(t, "member@example.test", member.roles, http.MethodPost, current.path, current.body)
				forbidden := response.Code == http.StatusForbidden
				listed := slices.Contains(view.ViewerActions, current.action)
				if listed == forbidden {
					t.Fatalf("viewer_actions=%v but %s answered %d: %s", view.ViewerActions, current.action, response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestViewerActionsDeveloperOnProductOwnerStageIsEmpty(t *testing.T) {
	now := time.Now().UTC()
	input := buildInputApproval("input-spec", approval.StatusPending, now)
	input.FromStage, input.ToStage = "spec", "spec"
	input.RequiredRoles = []string{"product_owner"}
	input.Targets = map[string]string{"submit": "spec", "skip": "spec"}
	runs := map[string]lifecycle.State{
		"run-spec": {Phase: lifecycle.PhaseWaiting, NextStage: "spec", PendingApprovalID: "input-spec"},
	}
	fixture := newFunctionPermissionFixture(t, false, []approval.PendingApproval{input}, runs)
	developer := fixture.viewerView(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, "run-spec")
	if !slices.Contains(developer.AllowedActions, "take") || len(developer.ViewerActions) != 0 {
		t.Fatalf("developer on a product_owner stage: allowed=%v viewer=%v", developer.AllowedActions, developer.ViewerActions)
	}
	owner := fixture.viewerView(t, "po@example.test", []cloudidentity.Role{cloudidentity.RoleProductOwner}, "run-spec")
	if !slices.Contains(owner.ViewerActions, "take") || !slices.Contains(owner.ViewerActions, "results") {
		t.Fatalf("product owner must be offered the stage actions: %v", owner.ViewerActions)
	}
}

func TestViewerActionsHideConfirmFromTheStageAuthor(t *testing.T) {
	submitted := time.Now().UTC().Add(-time.Hour)
	approvals := []approval.PendingApproval{
		buildInputApproval("input-1", approval.StatusResolved, submitted,
			approval.Decision{ActorID: "author@example.test", ActorRole: "developer", Action: "submit"}),
		buildConfirmApproval(submitted.Add(30 * time.Minute)),
	}
	runs := map[string]lifecycle.State{
		"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "accept", PendingApprovalID: "gate-1"},
	}
	fixture := newFunctionPermissionFixture(t, true, approvals, runs)
	both := []cloudidentity.Role{cloudidentity.RoleDeveloper, cloudidentity.RoleReviewer}
	author := fixture.viewerView(t, "author@example.test", both, "run-confirm")
	if slices.Contains(author.ViewerActions, "confirm") {
		t.Fatalf("the author must not be offered confirm under forbid_self_approval: %v", author.ViewerActions)
	}
	if slices.Contains(author.ViewerActions, "return") {
		t.Fatalf("the author must not be offered return under forbid_self_approval: %v", author.ViewerActions)
	}
	if author.ViewerDeniedReasons["confirm"] != "self_approval_forbidden" || author.ViewerDeniedReasons["return"] != "self_approval_forbidden" {
		t.Fatalf("the server must name the self-approval reason: %v", author.ViewerDeniedReasons)
	}
	other := fixture.viewerView(t, "reviewer@example.test", []cloudidentity.Role{cloudidentity.RoleReviewer}, "run-confirm")
	if !slices.Contains(other.ViewerActions, "confirm") {
		t.Fatalf("another reviewer must be offered confirm: %v", other.ViewerActions)
	}
	if !slices.Contains(other.ViewerActions, "return") {
		t.Fatalf("another reviewer must be offered return: %v", other.ViewerActions)
	}
}
