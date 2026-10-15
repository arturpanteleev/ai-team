// Package cloudidentity определяет доверенную identity и RBAC облачного control plane.
package cloudidentity

import (
	"fmt"
	"sort"
	"strings"
)

type Role string

const (
	RoleProductOwner   Role = "product_owner"
	RoleArchitect      Role = "architect"
	RoleDeveloper      Role = "developer"
	RoleReviewer       Role = "reviewer"
	RoleQA             Role = "qa"
	RoleReleaseManager Role = "release_manager"
	// RoleAdmin is the system administrator right (S06). It is not a process
	// function: a template stage cannot name it as `function` or
	// `approver_function`, and holding product_owner never implies it. It
	// grants only PermissionGateOverride (passing a task on after a required
	// gate failed) and the right to grant or revoke itself in the team.
	RoleAdmin Role = "admin"
)

var knownRoles = map[Role]bool{
	RoleProductOwner: true, RoleArchitect: true, RoleDeveloper: true,
	RoleReviewer: true, RoleQA: true, RoleReleaseManager: true,
	RoleAdmin: true,
}

type Principal struct {
	ActorID string `json:"actor_id"`
	Roles   []Role `json:"roles"`
}

func NewPrincipal(actorID string, roles []Role) (Principal, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" || len(actorID) > 200 || strings.ContainsAny(actorID, "\r\n\x00") {
		return Principal{}, fmt.Errorf("cloud identity: недопустимый actor ID")
	}
	unique := make(map[Role]bool, len(roles))
	for _, role := range roles {
		if !knownRoles[role] {
			return Principal{}, fmt.Errorf("cloud identity: неизвестная роль %q", role)
		}
		unique[role] = true
	}
	if len(unique) == 0 {
		return Principal{}, fmt.Errorf("cloud identity: требуется хотя бы одна роль")
	}
	normalized := make([]Role, 0, len(unique))
	for role := range unique {
		normalized = append(normalized, role)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return Principal{ActorID: actorID, Roles: normalized}, nil
}

func ParseRoles(values []string) ([]Role, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("cloud identity: требуется хотя бы одна роль")
	}
	roles := make([]Role, 0, len(values))
	for _, value := range values {
		role := Role(strings.TrimSpace(strings.ToLower(value)))
		if !knownRoles[role] {
			return nil, fmt.Errorf("cloud identity: неизвестная роль %q", value)
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// FunctionRole maps process-template function aliases to the canonical cloud
// identity role used for authorization. Approval records continue to retain
// the configured function string so workflow requirements remain stable.
func FunctionRole(function string) (Role, bool) {
	value := strings.TrimSpace(strings.ToLower(function))
	switch value {
	case "bo", "po", "business_owner":
		return RoleProductOwner, true
	case "deployer":
		return RoleReleaseManager, true
	case string(RoleAdmin):
		// A system right, not a process function (see RoleAdmin).
		return "", false
	default:
		roles, err := ParseRoles([]string{value})
		if err != nil {
			return "", false
		}
		return roles[0], true
	}
}

func (p Principal) Has(role Role) bool {
	for _, current := range p.Roles {
		if current == role {
			return true
		}
	}
	return false
}

type Permission string

const (
	PermissionStart        Permission = "run:start"
	PermissionResume       Permission = "run:resume"
	PermissionCancel       Permission = "run:cancel"
	PermissionDeliver      Permission = "run:deliver"
	PermissionDecision     Permission = "approval:decide"
	PermissionArtifactEdit Permission = "artifact:edit"
	PermissionTemplateEdit Permission = "template:edit"
	PermissionTeamManage   Permission = "team:manage"
	// PermissionProjectManage — создание, правка и архивирование проектов.
	PermissionProjectManage Permission = "project:manage"
	// PermissionTrackerLink — связь задачи с карточкой трекера и повтор доставки.
	PermissionTrackerLink Permission = "tracker:link"
	// PermissionGateOverride — провести задачу дальше после провала
	// обязательного гейта (S06). Принадлежит только системной роли admin;
	// product_owner его не получает.
	PermissionGateOverride Permission = "gate:override"
)

func Authorize(principal Principal, permission Permission, selectedRole Role) error {
	allowed := false
	switch permission {
	case PermissionStart:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleArchitect)
	case PermissionResume:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleArchitect) ||
			principal.Has(RoleDeveloper) || principal.Has(RoleQA)
	case PermissionCancel:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleReleaseManager)
	case PermissionDeliver:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleReleaseManager)
	case PermissionDecision:
		allowed = knownRoles[selectedRole] && principal.Has(selectedRole)
	case PermissionArtifactEdit:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleArchitect) ||
			principal.Has(RoleReviewer) || principal.Has(RoleQA)
	case PermissionTemplateEdit:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleArchitect)
	case PermissionTeamManage:
		allowed = principal.Has(RoleProductOwner)
	case PermissionProjectManage, PermissionTrackerLink:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleArchitect)
	case PermissionGateOverride:
		allowed = principal.Has(RoleAdmin)
	default:
		return fmt.Errorf("cloud RBAC: неизвестное permission %q", permission)
	}
	if !allowed {
		return fmt.Errorf("cloud RBAC: actor %s не имеет permission %s", principal.ActorID, permission)
	}
	return nil
}
