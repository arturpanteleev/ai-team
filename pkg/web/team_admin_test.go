package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

// activateInvited activates the invitation returned by the invite response.
func activateInvited(t *testing.T, srv *Server, invite *httptest.ResponseRecorder) {
	t.Helper()
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", invite.Code, invite.Body.String())
	}
	var token struct {
		Value string `json:"activation_token"`
	}
	if err := json.NewDecoder(invite.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	request := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+token.Value+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", response.Code, response.Body.String())
	}
}

// The creator of the team holds the system administrator right; an invited
// product owner does not, and only an administrator grants or takes it away.
func TestTeamAdminRightIsSeparateFromProductOwner(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	ownerCookie, ownerCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	me := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodGet, "/api/auth/me", "")
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"admin"`) || !strings.Contains(me.Body.String(), `"product_owner"`) {
		t.Fatalf("the team creator must be admin and product owner: %d %s", me.Code, me.Body.String())
	}

	activateInvited(t, srv, teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPost, "/api/team/invitations",
		`{"email":"po2@example.com","roles":["product_owner"]}`))
	poCookie, poCSRF := cloudSessionForTest(t, srv, manager, "po2@example.com", cloudidentity.RoleProductOwner)
	me = teamRequest(srv, poCookie, poCSRF, http.MethodGet, "/api/auth/me", "")
	if strings.Contains(me.Body.String(), `"admin"`) {
		t.Fatalf("an invited product owner must not get admin: %s", me.Body.String())
	}

	// A product owner manages the team but cannot hand out or take the right.
	if response := teamRequest(srv, poCookie, poCSRF, http.MethodPost, "/api/team/invitations",
		`{"email":"sneaky@example.com","roles":["admin"]}`); response.Code != http.StatusForbidden {
		t.Fatalf("a PO invited an administrator: %d %s", response.Code, response.Body.String())
	}
	if response := teamRequest(srv, poCookie, poCSRF, http.MethodPatch, "/api/team/members/owner@example.com/roles",
		`{"roles":["product_owner"]}`); response.Code != http.StatusForbidden {
		t.Fatalf("a PO stripped the administrator right: %d %s", response.Code, response.Body.String())
	}
	if response := teamRequest(srv, poCookie, poCSRF, http.MethodDelete, "/api/team/members/owner@example.com", ""); response.Code != http.StatusForbidden {
		t.Fatalf("a PO revoked an administrator: %d %s", response.Code, response.Body.String())
	}

	// The administrator can grant it; it is a system role, never a stage function.
	if response := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPatch, "/api/team/members/po2@example.com/roles",
		`{"roles":["product_owner","admin"]}`); response.Code != http.StatusOK {
		t.Fatalf("grant admin: %d %s", response.Code, response.Body.String())
	}
	if stale := teamRequest(srv, poCookie, poCSRF, http.MethodGet, "/api/auth/me", ""); stale.Code != http.StatusUnauthorized {
		t.Fatalf("the grant must end the old session: %d", stale.Code)
	}
	poCookie, poCSRF = cloudSessionForTest(t, srv, manager, "po2@example.com", cloudidentity.RoleProductOwner)
	me = teamRequest(srv, poCookie, poCSRF, http.MethodGet, "/api/auth/me", "")
	if !strings.Contains(me.Body.String(), `"admin"`) {
		t.Fatalf("admin granted but not visible: %s", me.Body.String())
	}
	if response := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPatch, "/api/team/members/po2@example.com/roles",
		`{"roles":["nobody"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("an unknown role must be rejected: %d %s", response.Code, response.Body.String())
	}
}

// m4: when the member to revoke cannot be read, the administrator-right check
// must not be skipped: the request fails closed and nothing is revoked.
func TestTeamRevokeFailsClosedWhenTheMemberCannotBeRead(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	ownerCookie, ownerCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	activateInvited(t, srv, teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPost, "/api/team/invitations",
		`{"email":"po2@example.com","roles":["product_owner"]}`))
	poCookie, poCSRF := cloudSessionForTest(t, srv, manager, "po2@example.com", cloudidentity.RoleProductOwner)

	srv.teamMemberLookup = func(string) (*store.TeamMember, error) { return nil, errors.New("registry unavailable") }
	if response := teamRequest(srv, poCookie, poCSRF, http.MethodDelete, "/api/team/members/owner@example.com", ""); response.Code != http.StatusInternalServerError {
		t.Fatalf("an unreadable member must fail closed: %d %s", response.Code, response.Body.String())
	}
	srv.teamMemberLookup = nil
	member, err := srv.store.TeamMember("owner@example.com")
	if err != nil || member == nil || member.Status != "active" {
		t.Fatalf("the administrator must stay: %+v %v", member, err)
	}
}
