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
)

var knownRoles = map[Role]bool{
	RoleProductOwner: true, RoleArchitect: true, RoleDeveloper: true,
	RoleReviewer: true, RoleQA: true, RoleReleaseManager: true,
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
	PermissionStart    Permission = "run:start"
	PermissionResume   Permission = "run:resume"
	PermissionCancel   Permission = "run:cancel"
	PermissionDecision Permission = "approval:decide"
)

func Authorize(principal Principal, permission Permission, selectedRole Role) error {
	allowed := false
	switch permission {
	case PermissionStart:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleArchitect)
	case PermissionResume:
		allowed = len(principal.Roles) > 0
	case PermissionCancel:
		allowed = principal.Has(RoleProductOwner) || principal.Has(RoleReleaseManager)
	case PermissionDecision:
		allowed = knownRoles[selectedRole] && principal.Has(selectedRole)
	default:
		return fmt.Errorf("cloud RBAC: неизвестное permission %q", permission)
	}
	if !allowed {
		return fmt.Errorf("cloud RBAC: actor %s не имеет permission %s", principal.ActorID, permission)
	}
	return nil
}
