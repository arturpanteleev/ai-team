package cloudidentity

import (
	"fmt"
	"testing"
)

func TestAuthorizeStageActionMatrix(t *testing.T) {
	allActions := []StageAction{StageActionTake, StageActionSubmit, StageActionBypass, StageActionConfirm}
	cases := []struct {
		name      string
		functions StageFunctions
		roles     []Role
		allowed   map[StageAction]bool
	}{
		{
			name:      "executor without approver may do everything",
			functions: StageFunctions{Executor: "developer"},
			roles:     []Role{RoleDeveloper},
			allowed:   map[StageAction]bool{StageActionTake: true, StageActionSubmit: true, StageActionBypass: true, StageActionConfirm: true},
		},
		{
			name:      "executor with separate approver cannot confirm",
			functions: StageFunctions{Executor: "developer", Approver: "reviewer"},
			roles:     []Role{RoleDeveloper},
			allowed:   map[StageAction]bool{StageActionTake: true, StageActionSubmit: true, StageActionBypass: true},
		},
		{
			name:      "approver may only confirm",
			functions: StageFunctions{Executor: "developer", Approver: "reviewer"},
			roles:     []Role{RoleReviewer},
			allowed:   map[StageAction]bool{StageActionConfirm: true},
		},
		{
			name:      "member with both functions may do everything",
			functions: StageFunctions{Executor: "developer", Approver: "reviewer"},
			roles:     []Role{RoleDeveloper, RoleReviewer},
			allowed:   map[StageAction]bool{StageActionTake: true, StageActionSubmit: true, StageActionBypass: true, StageActionConfirm: true},
		},
		{
			name:      "foreign function is denied everything",
			functions: StageFunctions{Executor: "developer", Approver: "reviewer"},
			roles:     []Role{RoleQA},
			allowed:   map[StageAction]bool{},
		},
		{
			name:      "function aliases resolve to canonical roles",
			functions: StageFunctions{Executor: "po", Approver: "deployer"},
			roles:     []Role{RoleProductOwner},
			allowed:   map[StageAction]bool{StageActionTake: true, StageActionSubmit: true, StageActionBypass: true},
		},
		{
			name:      "unknown stage function fails closed",
			functions: StageFunctions{Executor: "operator"},
			roles:     []Role{RoleProductOwner, RoleArchitect, RoleDeveloper, RoleReviewer, RoleQA, RoleReleaseManager},
			allowed:   map[StageAction]bool{},
		},
		{
			name:      "empty stage function fails closed",
			functions: StageFunctions{},
			roles:     []Role{RoleProductOwner, RoleArchitect, RoleDeveloper, RoleReviewer, RoleQA, RoleReleaseManager},
			allowed:   map[StageAction]bool{},
		},
	}
	for _, tc := range cases {
		principal, err := NewPrincipal("actor-1", tc.roles)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range allActions {
			t.Run(fmt.Sprintf("%s/%s", tc.name, action), func(t *testing.T) {
				role, err := AuthorizeStageAction(principal, action, tc.functions)
				if tc.allowed[action] {
					if err != nil || role == "" {
						t.Fatalf("expected allowed, got role=%q err=%v", role, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("expected denied, got role %q", role)
				}
			})
		}
	}
}

func TestAuthorizeStageActionRejectsUnknownAction(t *testing.T) {
	principal, err := NewPrincipal("actor-1", []Role{RoleDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeStageAction(principal, StageAction("delete"), StageFunctions{Executor: "developer"}); err == nil {
		t.Fatal("unknown stage action must be denied")
	}
}

func TestValidateStageFunctions(t *testing.T) {
	cases := []struct {
		name      string
		functions StageFunctions
		wantErr   bool
	}{
		{"approver omitted", StageFunctions{Executor: "developer"}, false},
		{"distinct approver", StageFunctions{Executor: "developer", Approver: "reviewer"}, false},
		{"same function", StageFunctions{Executor: "developer", Approver: "developer"}, true},
		{"alias of executor", StageFunctions{Executor: "po", Approver: "product_owner"}, true},
		{"case-insensitive duplicate of custom executor", StageFunctions{Executor: "Reviewer", Approver: "reviewer"}, true},
		{"unknown approver", StageFunctions{Executor: "developer", Approver: "operator"}, true},
		{"padded approver", StageFunctions{Executor: "developer", Approver: " reviewer"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStageFunctions(tc.functions)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestCheckSelfApproval(t *testing.T) {
	cases := []struct {
		name     string
		forbid   bool
		approver string
		authors  []string
		wantErr  bool
	}{
		{"policy disabled allows author", false, "dev@example.com", []string{"dev@example.com"}, false},
		{"other approver allowed", true, "rev@example.com", []string{"dev@example.com"}, false},
		{"author denied", true, "dev@example.com", []string{"dev@example.com"}, true},
		{"author match is case-insensitive", true, "Dev@Example.com", []string{"dev@example.com"}, true},
		{"no human authors allowed", true, "dev@example.com", nil, false},
		{"missing approver fails closed", true, " ", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSelfApproval(tc.forbid, tc.approver, tc.authors)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestParseFunctions(t *testing.T) {
	roles, err := ParseFunctions([]string{"PO", "developer", "deployer", "product_owner"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Role{RoleDeveloper, RoleProductOwner, RoleReleaseManager}
	if fmt.Sprint(roles) != fmt.Sprint(want) {
		t.Fatalf("roles=%v want %v", roles, want)
	}
	if _, err := ParseFunctions(nil); err == nil {
		t.Fatal("empty function list must be rejected")
	}
	if _, err := ParseFunctions([]string{"operator"}); err == nil {
		t.Fatal("unknown function must be rejected")
	}
}
