package rbac

import (
	"slices"
	"testing"
)

// An owner may hand out any role, including owner.
func TestOwnerMayAssignEveryRole(t *testing.T) {
	owner := NewGrant(RoleOwner)
	for _, role := range AllRoles {
		if !owner.CanAssign(role) {
			t.Fatalf("an owner could not assign %s", role)
		}
	}
}

// No operator may create a role that outranks them, and owner is reserved to
// owners.
func TestNoRoleMayAssignBeyondItself(t *testing.T) {
	if NewGrant(RoleAdministrator).CanAssign(RoleOwner) {
		t.Fatal("an administrator may mint an owner")
	}
	support := NewGrant(RoleSupport)
	for _, role := range []Role{RoleAdministrator, RoleFinance, RoleMarketing, RoleAuditor} {
		if support.CanAssign(role) {
			t.Fatalf("support may assign %s, which carries permissions it does not hold", role)
		}
	}
	if !support.CanAssign(RoleSupport) {
		t.Fatal("support holds every permission of its own role")
	}
	if NewGrant().CanAssign(RoleAuditor) {
		t.Fatal("an empty grant may assign a role")
	}
	if NewGrant(RoleOwner).CanAssign(Role("superuser")) {
		t.Fatal("an unknown role was assignable")
	}
}

func TestRoleChangeIsTheSetDifference(t *testing.T) {
	added, removed := RoleChange(
		[]Role{RoleSupport, RoleFinance},
		[]Role{RoleFinance, RoleAuditor, RoleAuditor},
	)
	if !slices.Equal(added, []Role{RoleAuditor}) {
		t.Fatalf("added = %v", added)
	}
	if !slices.Equal(removed, []Role{RoleSupport}) {
		t.Fatalf("removed = %v", removed)
	}
	added, removed = RoleChange([]Role{RoleOwner}, []Role{RoleOwner})
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("an unchanged set reported %v / %v", added, removed)
	}
}
