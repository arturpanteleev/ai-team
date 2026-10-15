package cloudidentity

import (
	"strings"
	"testing"
	"time"
)

func TestTokenIssueVerifyAndTampering(t *testing.T) {
	manager, err := NewTokenManager([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	principal, err := NewPrincipal("architect-1", []Role{RoleArchitect, RoleReviewer})
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.Issue(principal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := manager.Verify(token)
	if err != nil || verified.ActorID != principal.ActorID || !verified.Has(RoleArchitect) {
		t.Fatalf("verify: principal=%+v err=%v", verified, err)
	}
	mutated := token[:len(token)-1] + "A"
	if _, err := manager.Verify(mutated); err == nil {
		t.Fatal("изменённая подпись должна быть отклонена")
	}
	manager.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := manager.Verify(token); err == nil {
		t.Fatal("истёкший token должен быть отклонён")
	}
}

func TestPrincipalRolesAndRBAC(t *testing.T) {
	principal, err := NewPrincipal("release-1", []Role{RoleReleaseManager})
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(principal, PermissionCancel, ""); err != nil {
		t.Fatal(err)
	}
	if err := Authorize(principal, PermissionDeliver, ""); err != nil {
		t.Fatalf("release manager should retry an approved delivery: %v", err)
	}
	if err := Authorize(principal, PermissionStart, ""); err == nil {
		t.Fatal("release manager не должен создавать run")
	}
	if err := Authorize(principal, PermissionTemplateEdit, ""); err == nil {
		t.Fatal("release manager не должен менять шаблон")
	}
	architect, err := NewPrincipal("architect-1", []Role{RoleArchitect})
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(architect, PermissionTemplateEdit, ""); err != nil {
		t.Fatalf("architect должен менять шаблон: %v", err)
	}
	if err := Authorize(principal, PermissionDecision, RoleQA); err == nil {
		t.Fatal("нельзя принимать решение от чужой роли")
	}
	reviewer, err := NewPrincipal("reviewer-1", []Role{RoleReviewer})
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(reviewer, PermissionDeliver, ""); err == nil {
		t.Fatal("reviewer must not execute Git delivery")
	}
	if _, err := NewPrincipal("x", []Role{"superuser"}); err == nil {
		t.Fatal("неизвестная роль должна быть отклонена")
	}
}

func TestParseRolesRequiresAtLeastOneRole(t *testing.T) {
	for _, values := range [][]string{nil, {}, {"", " "}} {
		if _, err := ParseRoles(values); err == nil {
			t.Fatalf("ParseRoles(%q) accepted an empty role list", values)
		}
	}
	roles, err := ParseRoles([]string{"qa"})
	if err != nil || len(roles) != 1 || roles[0] != RoleQA {
		t.Fatalf("ParseRoles valid role: roles=%v err=%v", roles, err)
	}
}

func TestFunctionRoleResolvesLegacyFunctionAliasesForAuthorization(t *testing.T) {
	for function, want := range map[string]Role{
		"bo": RoleProductOwner, "po": RoleProductOwner, "deployer": RoleReleaseManager,
		"product_owner": RoleProductOwner, "release_manager": RoleReleaseManager,
	} {
		got, ok := FunctionRole(function)
		if !ok || got != want {
			t.Errorf("FunctionRole(%q) = (%q, %v), want (%q, true)", function, got, ok, want)
		}
	}
	if _, ok := FunctionRole("unknown"); ok {
		t.Fatal("unknown stage function unexpectedly resolved to a cloud role")
	}
}

func TestSessionBindingDependsOnSecretAndLabel(t *testing.T) {
	a, _ := NewTokenManager([]byte("0123456789abcdef0123456789abcdef"))
	b, _ := NewTokenManager([]byte("0123456789abcdef0123456789abcdeX"))
	same, _ := NewTokenManager([]byte("0123456789abcdef0123456789abcdef"))
	if a.SessionBinding("token") != same.SessionBinding("token") {
		t.Fatal("binding must be deterministic")
	}
	if a.SessionBinding("token") == b.SessionBinding("token") || a.SessionBinding("token") == a.SessionBinding("oidc") {
		t.Fatal("binding must change with the secret and the label")
	}
	if strings.Contains(a.SessionBinding("token"), "0123456789abcdef") {
		t.Fatal("binding must not reveal the secret")
	}
}

func TestGateOverrideIsAdminOnlyAndNotImpliedByProductOwner(t *testing.T) {
	owner, err := NewPrincipal("po", []Role{RoleProductOwner, RoleArchitect, RoleReleaseManager})
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(owner, PermissionGateOverride, ""); err == nil {
		t.Fatal("product_owner must not get the gate override right")
	}
	admin, err := NewPrincipal("admin-1", []Role{RoleAdmin})
	if err != nil {
		t.Fatalf("admin is a valid system role: %v", err)
	}
	if err := Authorize(admin, PermissionGateOverride, ""); err != nil {
		t.Fatalf("admin must override: %v", err)
	}
	// The system right grants nothing else.
	if err := Authorize(admin, PermissionTeamManage, ""); err == nil {
		t.Fatal("admin alone must not manage the team")
	}
}

func TestAdminIsNotAProcessFunction(t *testing.T) {
	if _, ok := FunctionRole("admin"); ok {
		t.Fatal("admin must not be usable as a stage function")
	}
	if _, err := ParseFunctions([]string{"admin"}); err == nil {
		t.Fatal("ParseFunctions must reject admin")
	}
	roles, err := ParseTeamRoles([]string{"po", "ADMIN"})
	if err != nil || len(roles) != 2 || roles[0] != RoleAdmin || roles[1] != RoleProductOwner {
		t.Fatalf("ParseTeamRoles = %v, %v", roles, err)
	}
	if _, err := ParseTeamRoles([]string{"nobody"}); err == nil {
		t.Fatal("unknown role must be rejected")
	}
}
