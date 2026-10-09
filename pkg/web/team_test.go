package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
)

func cloudSessionForTest(t *testing.T, s *Server, manager *cloudidentity.TokenManager, actor string, roles ...cloudidentity.Role) (*http.Cookie, string) {
	t.Helper()
	principal, err := cloudidentity.NewPrincipal(actor, roles)
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login actor %s: %d %s", actor, w.Code, w.Body.String())
	}
	var response sessionResponse
	if err = json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return w.Result().Cookies()[0], response.CSRFToken
}

func teamRequest(s *Server, cookie *http.Cookie, csrf, method, target, body string) *httptest.ResponseRecorder {
	req := newLoopbackRequest(method, target, strings.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func TestLocalAuthenticationCannotManageTeamOrCreateInvitations(t *testing.T) {
	const token = "local-team-mode-token-0123456789abcdef"
	verifier, err := NewLocalAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithLocalAuthenticator(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	config := httptest.NewRecorder()
	srv.router.ServeHTTP(config, newLoopbackRequest(http.MethodGet, "/api/auth/config", nil))
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"team_management_enabled":false`) {
		t.Fatalf("local auth config must disable team management: %d %s", config.Code, config.Body.String())
	}

	invite := authenticatedRequest(t, srv, token, http.MethodPost, "/api/team/invitations",
		`{"email":"new-member@example.com","roles":["reviewer"]}`)
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, invite)
	if response.Code != http.StatusForbidden {
		t.Fatalf("local auth must reject team invitations before creation: %d %s", response.Code, response.Body.String())
	}
	member, err := srv.store.TeamMember("new-member@example.com")
	if err != nil || member != nil {
		t.Fatalf("rejected local invitation must not create a team member: member=%+v err=%v", member, err)
	}

	for _, endpoint := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/team/members", ""},
		{http.MethodGet, "/api/team/audit", ""},
		{http.MethodPatch, "/api/team/members/member@example.com/roles", `{"roles":["qa"]}`},
		{http.MethodDelete, "/api/team/members/member@example.com", ""},
	} {
		request := authenticatedRequest(t, srv, token, endpoint.method, endpoint.path, endpoint.body)
		writer := httptest.NewRecorder()
		srv.router.ServeHTTP(writer, request)
		if writer.Code != http.StatusForbidden {
			t.Errorf("local auth must reject %s %s: %d %s", endpoint.method, endpoint.path, writer.Code, writer.Body.String())
		}
	}

	activation := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+strings.Repeat("x", 32)+`"}`))
	activation.Header.Set("Content-Type", "application/json")
	activationResponse := httptest.NewRecorder()
	srv.router.ServeHTTP(activationResponse, activation)
	if activationResponse.Code != http.StatusForbidden {
		t.Fatalf("local auth must reject team activation: %d %s", activationResponse.Code, activationResponse.Body.String())
	}
}

func TestTeamInviteCanonicalRolesAndImmediateSessionRevocation(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("m", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	config := httptest.NewRecorder()
	srv.router.ServeHTTP(config, newLoopbackRequest(http.MethodGet, "/api/auth/config", nil))
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"team_management_enabled":true`) {
		t.Fatalf("cloud auth config must enable team management: %d %s", config.Code, config.Body.String())
	}
	uninvitedPrincipal, _ := cloudidentity.NewPrincipal("uninvited@example.com", []cloudidentity.Role{cloudidentity.RoleDeveloper})
	uninvitedToken, _ := manager.Issue(uninvitedPrincipal, time.Hour)
	uninvitedRequest := newLoopbackRequest(http.MethodGet, "/api/session", nil)
	uninvitedRequest.Header.Set("Authorization", "Bearer "+uninvitedToken)
	uninvitedWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(uninvitedWriter, uninvitedRequest)
	if uninvitedWriter.Code != http.StatusForbidden {
		t.Fatalf("uninvited signed actor bypassed team bootstrap policy: %d %s", uninvitedWriter.Code, uninvitedWriter.Body.String())
	}
	adminCookie, adminCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations", `{"email":"qa@example.com","roles":["qa"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err = json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	activate := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activate.Header.Set("Content-Type", "application/json")
	activateWriter := httptest.NewRecorder()
	srv.router.ServeHTTP(activateWriter, activate)
	if activateWriter.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", activateWriter.Code, activateWriter.Body.String())
	}
	var activated struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.NewDecoder(activateWriter.Body).Decode(&activated); err != nil {
		t.Fatal(err)
	}
	issued, err := manager.Verify(activated.AccessToken)
	if err != nil || issued.ActorID != "qa@example.com" || !issued.Has(cloudidentity.RoleQA) || issued.Has(cloudidentity.RoleProductOwner) {
		t.Fatalf("activation did not issue canonical team identity: %+v %v", issued, err)
	}
	qaCookie, qaCSRF := cloudSessionForTest(t, srv, manager, "qa@example.com", cloudidentity.RoleProductOwner)
	me := teamRequest(srv, qaCookie, qaCSRF, http.MethodGet, "/api/auth/me", "")
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"roles":["qa"]`) {
		t.Fatalf("token claims overrode member role: %d %s", me.Code, me.Body.String())
	}
	start := teamRequest(srv, qaCookie, qaCSRF, http.MethodPost, "/api/runs", `{"feature":"f","task":"t"}`)
	if start.Code != http.StatusForbidden {
		t.Fatalf("QA inherited Product Owner start permission: %d %s", start.Code, start.Body.String())
	}
	forgedDecision := teamRequest(srv, qaCookie, qaCSRF, http.MethodPost, "/api/runs/r/approvals/a/decisions", `{"actor_id":"owner@example.com","actor_role":"product_owner","action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if forgedDecision.Code != http.StatusForbidden {
		t.Fatalf("QA escalated decision role through JSON: %d %s", forgedDecision.Code, forgedDecision.Body.String())
	}
	validDecision := teamRequest(srv, qaCookie, qaCSRF, http.MethodPost, "/api/runs/r/approvals/a/decisions", `{"actor_id":"spoofed","actor_role":"qa","action":"approve","subject_hash":"`+testSubjectHash+`"}`)
	if validDecision.Code != http.StatusOK {
		t.Fatalf("QA decision denied: %d %s", validDecision.Code, validDecision.Body.String())
	}
	if controller, ok := srv.controller.(*fakeRunController); ok && (controller.decision.ActorID != "qa@example.com" || controller.decision.ActorRole != "qa") {
		t.Fatalf("decision trusted JSON actor: %+v", controller.decision)
	}
	again := httptest.NewRecorder()
	againRequest := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	againRequest.Header.Set("Content-Type", "application/json")
	srv.router.ServeHTTP(again, againRequest)
	if again.Code != http.StatusGone {
		t.Fatalf("activation token was reusable: %d %s", again.Code, again.Body.String())
	}
	if changed := teamRequest(srv, adminCookie, adminCSRF, http.MethodPatch, "/api/team/members/qa@example.com/roles", `{"roles":["developer"]}`); changed.Code != http.StatusOK {
		t.Fatalf("change roles: %d %s", changed.Code, changed.Body.String())
	}
	if stale := teamRequest(srv, qaCookie, qaCSRF, http.MethodGet, "/api/auth/me", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("role change did not revoke old session: %d %s", stale.Code, stale.Body.String())
	}
	developerCookie, developerCSRF := cloudSessionForTest(t, srv, manager, "qa@example.com", cloudidentity.RoleProductOwner)
	if revoked := teamRequest(srv, adminCookie, adminCSRF, http.MethodDelete, "/api/team/members/qa@example.com", ""); revoked.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body.String())
	}
	oldSession := teamRequest(srv, developerCookie, developerCSRF, http.MethodGet, "/api/auth/me", "")
	if oldSession.Code != http.StatusUnauthorized {
		t.Fatalf("revoked user session remained valid: %d %s", oldSession.Code, oldSession.Body.String())
	}
	audit := teamRequest(srv, adminCookie, adminCSRF, http.MethodGet, "/api/team/audit", "")
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "owner@example.com") || !strings.Contains(audit.Body.String(), "qa@example.com") {
		t.Fatalf("audit lacks verified actor/target identity: %d %s", audit.Code, audit.Body.String())
	}
}

func TestTeamManagementRequiresProductOwnerAndRejectsJSONEscalation(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("n", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	adminCookie, adminCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	invite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations", `{"email":"qa@example.com","roles":["qa"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatal(invite.Code, invite.Body.String())
	}
	if premature := teamRequest(srv, adminCookie, adminCSRF, http.MethodPatch, "/api/team/members/qa@example.com/roles", `{"roles":["product_owner"]}`); premature.Code != http.StatusConflict {
		t.Fatalf("role update bypassed invitation activation: %d %s", premature.Code, premature.Body.String())
	}
	var token struct {
		Value string `json:"activation_token"`
	}
	if err = json.NewDecoder(invite.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	r := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+token.Value+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	qaCookie, qaCSRF := cloudSessionForTest(t, srv, manager, "qa@example.com", cloudidentity.RoleProductOwner)
	result := teamRequest(srv, qaCookie, qaCSRF, http.MethodPost, "/api/team/invitations", `{"email":"attacker@example.com","roles":["product_owner"],"actor_id":"owner@example.com"}`)
	if result.Code != http.StatusForbidden {
		t.Fatalf("non-admin escalated through JSON: %d %s", result.Code, result.Body.String())
	}
}

func TestTeamReadRoutesNeedAdminSessionButNotCSRF(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	adminCookie, adminCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)

	for _, endpoint := range []string{"/api/team/members", "/api/team/audit"} {
		request := newLoopbackRequest(http.MethodGet, endpoint, nil)
		request.AddCookie(adminCookie)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("product owner read %s without a CSRF header: %d %s", endpoint, response.Code, response.Body.String())
		}
	}

	writeWithoutCSRF := newLoopbackRequest(http.MethodPost, "/api/team/invitations", strings.NewReader(`{"email":"qa@example.com","roles":["qa"]}`))
	writeWithoutCSRF.AddCookie(adminCookie)
	writeWithoutCSRF.Header.Set("Content-Type", "application/json")
	blocked := httptest.NewRecorder()
	srv.router.ServeHTTP(blocked, writeWithoutCSRF)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("team write without CSRF must be rejected: %d %s", blocked.Code, blocked.Body.String())
	}

	invite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations", `{"email":"qa@example.com","roles":["qa"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite for read authorization test: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	activate := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activate.Header.Set("Content-Type", "application/json")
	activated := httptest.NewRecorder()
	srv.router.ServeHTTP(activated, activate)
	if activated.Code != http.StatusOK {
		t.Fatalf("activate QA: %d %s", activated.Code, activated.Body.String())
	}
	qaCookie, _ := cloudSessionForTest(t, srv, manager, "qa@example.com", cloudidentity.RoleProductOwner)
	for _, endpoint := range []string{"/api/team/members", "/api/team/audit"} {
		request := newLoopbackRequest(http.MethodGet, endpoint, nil)
		request.AddCookie(qaCookie)
		response := httptest.NewRecorder()
		srv.router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("non-admin must not read %s: %d %s", endpoint, response.Code, response.Body.String())
		}
	}
}

func TestEmptyTeamRolesAreRejectedWithoutChangingInvitationOrMember(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	adminCookie, adminCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)

	emptyInvite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations", `{"email":"qa@example.com","roles":[]}`)
	if emptyInvite.Code != http.StatusBadRequest {
		t.Fatalf("empty-role invitation must be rejected: %d %s", emptyInvite.Code, emptyInvite.Body.String())
	}
	if count, err := srv.store.TeamMemberCount(); err != nil || count != 1 {
		t.Fatalf("failed invite changed team membership: count=%d err=%v", count, err)
	}

	invite := teamRequest(srv, adminCookie, adminCSRF, http.MethodPost, "/api/team/invitations", `{"email":"qa@example.com","roles":["qa"]}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("valid invite after rejected empty invite: %d %s", invite.Code, invite.Body.String())
	}
	var invitation struct {
		Token string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&invitation); err != nil {
		t.Fatal(err)
	}
	if changed := teamRequest(srv, adminCookie, adminCSRF, http.MethodPatch, "/api/team/members/qa@example.com/roles", `{"roles":[]}`); changed.Code != http.StatusBadRequest {
		t.Fatalf("empty roles on invited member must be rejected: %d %s", changed.Code, changed.Body.String())
	}
	invited, err := srv.store.TeamMember("qa@example.com")
	if err != nil || invited == nil || invited.Status != "invited" || len(invited.Roles) != 1 || invited.Roles[0] != "qa" {
		t.Fatalf("rejected patch changed invitation: member=%+v err=%v", invited, err)
	}

	activate := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
	activate.Header.Set("Content-Type", "application/json")
	activated := httptest.NewRecorder()
	srv.router.ServeHTTP(activated, activate)
	if activated.Code != http.StatusOK {
		t.Fatalf("rejected role edit must preserve invitation token: %d %s", activated.Code, activated.Body.String())
	}
	qaCookie, qaCSRF := cloudSessionForTest(t, srv, manager, "qa@example.com", cloudidentity.RoleProductOwner)
	if changed := teamRequest(srv, adminCookie, adminCSRF, http.MethodPatch, "/api/team/members/qa@example.com/roles", `{"roles":[]}`); changed.Code != http.StatusBadRequest {
		t.Fatalf("empty roles on active member must be rejected: %d %s", changed.Code, changed.Body.String())
	}
	active, err := srv.store.TeamMember("qa@example.com")
	if err != nil || active == nil || active.Status != "active" || len(active.Roles) != 1 || active.Roles[0] != "qa" {
		t.Fatalf("rejected role edit changed active member: member=%+v err=%v", active, err)
	}
	me := teamRequest(srv, qaCookie, qaCSRF, http.MethodGet, "/api/auth/me", "")
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"roles":["qa"]`) {
		t.Fatalf("rejected role edit revoked a valid member session: %d %s", me.Code, me.Body.String())
	}
}
