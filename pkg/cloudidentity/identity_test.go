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
	if _, err := NewPrincipal("x", []Role{"admin"}); err == nil {
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
