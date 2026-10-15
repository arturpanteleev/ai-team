package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/humandecision"
	"github.com/arturpanteleev/ai-team/pkg/lifecycle"
)

func decisionEvents(t *testing.T, fixture functionPermissionFixture, eventType string) []map[string]any {
	t.Helper()
	events, err := fixture.srv.store.GetRunEventsAfter("run-confirm", 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(event.DataJSON), &data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

func confirmFixture(t *testing.T) functionPermissionFixture {
	t.Helper()
	runs := map[string]lifecycle.State{
		"run-confirm": {Phase: lifecycle.PhaseWaiting, NextStage: "build", PendingApprovalID: "gate-1"},
	}
	return newFunctionPermissionFixture(t, true, []approval.PendingApproval{buildConfirmApproval(time.Now().UTC().Add(-time.Minute))}, runs)
}

var reviewerRoles = []cloudidentity.Role{cloudidentity.RoleReviewer}

// The task confirm endpoint (Web UI) and the generic decision endpoint (REST)
// are two doors to one service: the same person gets the same stored decision
// and the same audit record, differing only in the channel.
func TestHumanDecision_WebAndRESTShareDecisionAndAudit(t *testing.T) {
	web := confirmFixture(t)
	response := web.requestAs(t, "reviewer@example.test", reviewerRoles, http.MethodPost,
		"/api/runs/run-confirm/stages/build/confirm", `{"subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", response.Code, response.Body.String())
	}
	rest := confirmFixture(t)
	response = rest.requestAs(t, "reviewer@example.test", reviewerRoles, http.MethodPost,
		"/api/runs/run-confirm/approvals/gate-1/decisions",
		`{"action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", response.Code, response.Body.String())
	}
	for name, fixture := range map[string]functionPermissionFixture{"web": web, "rest": rest} {
		got := fixture.controller.decision
		if got.ActorID != "reviewer@example.test" || got.ActorRole != "reviewer" || got.Action != "approve" ||
			got.SubjectHash != testSubjectHash || !got.ControllerAuthenticated {
			t.Fatalf("%s decision = %+v", name, got)
		}
	}
	webAudit, restAudit := decisionEvents(t, web, humandecision.AuditEvent), decisionEvents(t, rest, humandecision.AuditEvent)
	if len(webAudit) != 1 || len(restAudit) != 1 {
		t.Fatalf("each decision must leave exactly one audit record: web %d, rest %d", len(webAudit), len(restAudit))
	}
	if webAudit[0]["channel"] != "web" || restAudit[0]["channel"] != "rest" {
		t.Fatalf("channels: %v / %v", webAudit[0]["channel"], restAudit[0]["channel"])
	}
	delete(webAudit[0], "channel")
	delete(restAudit[0], "channel")
	if a, b := mustMarshal(t, webAudit[0]), mustMarshal(t, restAudit[0]); a != b {
		t.Fatalf("audit records differ beyond the channel:\n%s\n%s", a, b)
	}
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHumanDecision_StaleHashIsRefusedOnBothDoors(t *testing.T) {
	stale := strings.Repeat("b", 64)
	for name, call := range map[string]func(functionPermissionFixture) *httptest.ResponseRecorder{
		"confirm": func(f functionPermissionFixture) *httptest.ResponseRecorder {
			return f.requestAs(t, "reviewer@example.test", reviewerRoles, http.MethodPost,
				"/api/runs/run-confirm/stages/build/confirm", `{"subject_hash":"`+stale+`"}`)
		},
		"decision": func(f functionPermissionFixture) *httptest.ResponseRecorder {
			return f.requestAs(t, "reviewer@example.test", reviewerRoles, http.MethodPost,
				"/api/runs/run-confirm/approvals/gate-1/decisions", `{"action":"approve","subject_hash":"`+stale+`"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := confirmFixture(t)
			response := call(fixture)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "subject_hash_mismatch") {
				t.Fatalf("a stale hash must be refused: %d %s", response.Code, response.Body.String())
			}
			if fixture.controller.decisionCalls != 0 || len(decisionEvents(t, fixture, humandecision.AuditEvent)) != 0 {
				t.Fatal("a refused decision must neither be stored nor audited")
			}
		})
	}
}

func TestHumanDecision_RoleAndSelfApprovalAreEnforcedBeforeTheStore(t *testing.T) {
	fixture := confirmFixture(t)
	response := fixture.requestAs(t, "dev@example.test", []cloudidentity.Role{cloudidentity.RoleDeveloper}, http.MethodPost,
		"/api/runs/run-confirm/approvals/gate-1/decisions", `{"action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusForbidden || fixture.controller.decisionCalls != 0 {
		t.Fatalf("a foreign role must be refused: %d %s", response.Code, response.Body.String())
	}
	response = fixture.requestAs(t, "reviewer@example.test", reviewerRoles, http.MethodPost,
		"/api/runs/run-confirm/approvals/gate-1/decisions", `{"action":"explode","subject_hash":"`+testSubjectHash+`"}`)
	if response.Code != http.StatusConflict || fixture.controller.decisionCalls != 0 {
		t.Fatalf("an action the approval does not offer must be refused: %d %s", response.Code, response.Body.String())
	}
}

func TestHumanDecision_ConfirmWithoutHashIsAuditedAsServerFilled(t *testing.T) {
	fixture := confirmFixture(t)
	response := fixture.requestAs(t, "reviewer@example.test", reviewerRoles, http.MethodPost,
		"/api/runs/run-confirm/stages/build/confirm", "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", response.Code, response.Body.String())
	}
	audit := decisionEvents(t, fixture, humandecision.AuditEvent)
	if len(audit) != 1 || audit[0]["hash_confirmed_by_actor"] != false {
		t.Fatalf("a confirmation that named no hash must say so in the audit: %v", audit)
	}
}
