package rbac

import (
	"errors"
	"testing"
)

func TestCustomRolesAreNamedSubsetsOfTheCatalogue(t *testing.T) {
	t.Cleanup(func() { SetCustomRoles(nil) })
	for _, invalid := range []CustomRole{
		{Role: "moderator", Name: "No prefix", Permissions: []Permission{PermissionCustomersRead}},
		{Role: "custom-x", Name: "Too short key", Permissions: []Permission{PermissionCustomersRead}},
		{Role: "custom-root", Name: "Escalation", Permissions: []Permission{PermissionAdminsRoles}},
		{Role: "custom-ghost", Name: "Unknown", Permissions: []Permission{"everything.do"}},
		{Role: "custom-empty", Name: "Empty"},
	} {
		if err := ValidateCustomRole(invalid); !errors.Is(err, ErrInvalidCustomRole) {
			t.Errorf("%s accepted: %v", invalid.Role, err)
		}
	}
	SetCustomRoles([]CustomRole{
		{Role: "custom-desk", Name: "Desk", Permissions: []Permission{PermissionSupportRead, PermissionSupportWrite, PermissionCustomersRead}},
		{Role: "custom-root", Name: "Escalation", Permissions: []Permission{PermissionAdminsRoles}},
	})
	if _, err := ParseRole("custom-root"); err == nil {
		t.Fatal("an invalid definition must never become grantable")
	}
	role, err := ParseRole("custom-desk")
	if err != nil {
		t.Fatal(err)
	}
	grant := NewGrant(role)
	if !grant.Allows(PermissionSupportWrite) || grant.Allows(PermissionFinanceRead) {
		t.Fatalf("grant = %v", grant.Permissions())
	}
	if !NewGrant(RoleAdministrator).CanAssign(role) {
		t.Fatal("an administrator holds every permission of the custom role")
	}
	if NewGrant(RoleMarketing).CanAssign(role) {
		t.Fatal("marketing does not hold support.write")
	}
}
