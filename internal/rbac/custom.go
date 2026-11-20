package rbac

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync/atomic"
)

// CustomRole is an operator-defined role: a name for a set of permissions
// taken from the compiled-in catalogue. It adds no capability the catalogue
// does not define, and this package still decides what every role may do —
// a custom role is a named subset, validated here before it can be granted.
type CustomRole struct {
	Role        Role
	Name        string
	Permissions []Permission
}

// customRolePattern is the shape of a custom role key. The prefix keeps a
// custom role from ever being mistaken for — or shadowing — a built-in one.
var customRolePattern = regexp.MustCompile(`^custom-[a-z0-9-]{2,32}$`)

// reservedPermissions are never part of a custom role. Managing operator
// accounts and their grants stays with the owner role, because a role that
// could grant roles is the escalation path the built-in set is designed to
// close.
var reservedPermissions = []Permission{PermissionAdminsWrite, PermissionAdminsRoles}

// ErrInvalidCustomRole reports a custom role this package refuses.
var ErrInvalidCustomRole = errors.New("invalid custom role")

// ValidateCustomRole checks a definition: a well-formed key, a name, and at
// least one known, non-reserved permission.
func ValidateCustomRole(role CustomRole) error {
	if !customRolePattern.MatchString(string(role.Role)) {
		return fmt.Errorf("%w: the key must look like custom-<name>", ErrInvalidCustomRole)
	}
	if role.Name == "" || len(role.Name) > 80 {
		return fmt.Errorf("%w: a name is required", ErrInvalidCustomRole)
	}
	if len(role.Permissions) == 0 {
		return fmt.Errorf("%w: a role needs at least one permission", ErrInvalidCustomRole)
	}
	for _, permission := range role.Permissions {
		if !Known(permission) {
			return fmt.Errorf("%w: unknown permission %q", ErrInvalidCustomRole, permission)
		}
		if slices.Contains(reservedPermissions, permission) {
			return fmt.Errorf("%w: %s is reserved to the owner role", ErrInvalidCustomRole, permission)
		}
	}
	return nil
}

var customRoles atomic.Pointer[map[Role]CustomRole]

// SetCustomRoles replaces the custom roles this process knows. Invalid
// definitions are dropped rather than granted, so a row edited by hand can
// never widen access past what ValidateCustomRole allows.
func SetCustomRoles(roles []CustomRole) {
	next := make(map[Role]CustomRole, len(roles))
	for _, role := range roles {
		if ValidateCustomRole(role) == nil {
			role.Permissions = slices.Clone(role.Permissions)
			next[role.Role] = role
		}
	}
	customRoles.Store(&next)
}

// CustomRoles lists the custom roles this process knows, by key.
func CustomRoles() []CustomRole {
	current := customRoles.Load()
	if current == nil {
		return nil
	}
	out := make([]CustomRole, 0, len(*current))
	for _, role := range *current {
		out = append(out, role)
	}
	slices.SortFunc(out, func(left, right CustomRole) int {
		if left.Role < right.Role {
			return -1
		}
		return 1
	})
	return out
}

// permissionsOf answers a built-in or custom role's permissions.
func permissionsOf(role Role) ([]Permission, bool) {
	if permissions, builtIn := rolePermissions[role]; builtIn {
		return permissions, true
	}
	if current := customRoles.Load(); current != nil {
		if custom, known := (*current)[role]; known {
			return custom.Permissions, true
		}
	}
	return nil, false
}

// IsCustom reports whether a role is an operator-defined one.
func IsCustom(role Role) bool { return customRolePattern.MatchString(string(role)) }
