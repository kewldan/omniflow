package rbac

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseRoleAcceptsBuiltInRoles(t *testing.T) {
	for _, role := range AllRoles {
		parsed, err := ParseRole(strings.ToUpper(string(role)))
		if err != nil {
			t.Fatalf("ParseRole(%q) returned %v", role, err)
		}
		if parsed != role {
			t.Fatalf("ParseRole(%q) = %q", role, parsed)
		}
	}
}

func TestParseRoleRejectsUnknownRole(t *testing.T) {
	if _, err := ParseRole("superuser"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("expected ErrUnknownRole, got %v", err)
	}
}

func TestOwnerHoldsEveryPermission(t *testing.T) {
	grant := NewGrant(RoleOwner)
	for _, permission := range AllPermissions {
		if !grant.Allows(permission) {
			t.Fatalf("owner is missing %q", permission)
		}
	}
}

// Account and role management is the privilege-escalation path that matters, so
// it is owner-only by design. This test fails loudly if a later change widens it.
func TestOnlyOwnerManagesOperatorAccounts(t *testing.T) {
	for _, role := range AllRoles {
		grant := NewGrant(role)
		wantAllowed := role == RoleOwner
		if got := grant.Allows(PermissionAdminsWrite); got != wantAllowed {
			t.Fatalf("role %q: admins.write = %v, want %v", role, got, wantAllowed)
		}
		if got := grant.Allows(PermissionAdminsRoles); got != wantAllowed {
			t.Fatalf("role %q: admins.roles = %v, want %v", role, got, wantAllowed)
		}
	}
}

func TestAuditorIsReadOnly(t *testing.T) {
	grant := NewGrant(RoleAuditor)
	for _, permission := range grant.Permissions() {
		if permission == PermissionAuditExport {
			continue
		}
		if !strings.HasSuffix(string(permission), ".read") {
			t.Fatalf("auditor holds non-read permission %q", permission)
		}
	}
	if !grant.Allows(PermissionAuditExport) {
		t.Fatal("auditor cannot export the audit trail")
	}
}

// Every non-owner role must be a strict subset of the catalogue; a role that
// accidentally lists an unknown permission would silently grant nothing.
func TestRolePermissionsAreDrawnFromTheCatalogue(t *testing.T) {
	for role, permissions := range rolePermissions {
		for _, permission := range permissions {
			if !slices.Contains(AllPermissions, permission) {
				t.Fatalf("role %q grants %q, which is not in AllPermissions", role, permission)
			}
		}
	}
}

// The risk surfaces produce evidence an operator acts on elsewhere, so reading
// them must never imply the ability to decide. A role that could decide without
// being able to see a customer record would be deciding blind.
func TestRiskDecisionsRequireCustomerVisibility(t *testing.T) {
	for _, role := range AllRoles {
		grant := NewGrant(role)
		if grant.Allows(PermissionRiskWrite) && !grant.Allows(PermissionCustomersRead) {
			t.Fatalf("role %q may decide a risk match without being able to read the customer", role)
		}
		if grant.Allows(PermissionRiskWrite) && !grant.Allows(PermissionRiskRead) {
			t.Fatalf("role %q may write risk decisions it cannot read", role)
		}
	}
}

// Every write permission implies its read counterpart. Without this a role could
// change something it cannot see afterwards, which makes its own work
// unreviewable.
func TestWritePermissionsImplyTheirReads(t *testing.T) {
	for _, role := range AllRoles {
		grant := NewGrant(role)
		for _, permission := range grant.Permissions() {
			name := string(permission)
			if !strings.HasSuffix(name, ".write") {
				continue
			}
			read := Permission(strings.TrimSuffix(name, ".write") + ".read")
			if !slices.Contains(AllPermissions, read) {
				continue
			}
			if !grant.Allows(read) {
				t.Fatalf("role %q holds %q without %q", role, permission, read)
			}
		}
	}
}

func TestGrantUnionsRoles(t *testing.T) {
	grant := NewGrant(RoleSupport, RoleFinance)
	if !grant.Allows(PermissionSupportWrite) {
		t.Fatal("union lost support.write")
	}
	if !grant.Allows(PermissionFinanceWrite) {
		t.Fatal("union lost finance.write")
	}
	if grant.Allows(PermissionAdminsWrite) {
		t.Fatal("union invented admins.write")
	}
}

func TestGrantIgnoresUnknownRolesWithoutLosingKnownOnes(t *testing.T) {
	grant := NewGrant(RoleSupport, Role("retired-role"))
	if !grant.Allows(PermissionSupportRead) {
		t.Fatal("an unknown role discarded the valid one")
	}
	if roles := grant.Roles(); len(roles) != 1 || roles[0] != RoleSupport {
		t.Fatalf("Roles() = %v, want [support]", roles)
	}
}

func TestAllowsAllRequiresEveryPermission(t *testing.T) {
	grant := NewGrant(RoleSupport)
	if !grant.AllowsAll(PermissionCustomersRead, PermissionSupportRead) {
		t.Fatal("support should hold both read permissions")
	}
	if grant.AllowsAll(PermissionCustomersRead, PermissionFinanceWrite) {
		t.Fatal("support must not hold finance.write")
	}
	if !grant.AllowsAll() {
		t.Fatal("an empty requirement must be satisfied")
	}
}

func TestPermissionsForReturnsACopy(t *testing.T) {
	permissions := PermissionsFor(RoleOwner)
	if len(permissions) == 0 {
		t.Fatal("owner permissions are empty")
	}
	permissions[0] = Permission("tampered")
	if slices.Contains(PermissionsFor(RoleOwner), Permission("tampered")) {
		t.Fatal("PermissionsFor exposed the compiled-in catalogue")
	}
}

func TestHasRoleDistinguishesIdentityFromCapability(t *testing.T) {
	grant := NewGrant(RoleAdministrator)
	if grant.HasRole(RoleOwner) {
		t.Fatal("administrator must not report the owner role")
	}
	if !grant.HasRole(RoleAdministrator) {
		t.Fatal("administrator lost its own role")
	}
}
