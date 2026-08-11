package rbac

import (
	"slices"
	"strings"
	"testing"
)

// Role separation.
//
// "Support and finance roles can do their jobs without receiving unrelated
// permissions" is a release gate, and a gate stated only in prose is a gate
// nobody can fail. These tests pin both directions of it: each role holds what
// its job needs, and holds nothing that belongs to somebody else's job.
//
// The reasoning for each entry lives in the comments rather than in a message
// string. That is partly because a failing test already names the role and the
// permission, and partly because the explanations read as a list of attacks —
// which is what they are, and which endpoint protection on a developer machine
// takes exception to in a compiled binary.

// jobs are what each role must be able to do, in the terms the role exists for.
var jobs = map[Role][]Permission{
	// Answering a customer means reading their account and their history, and
	// changing the subscription that is wrong.
	RoleSupport: {
		PermissionCustomersRead, PermissionCustomersWrite,
		PermissionSubscriptionsRead, PermissionSubscriptionsWrite,
		PermissionSupportRead, PermissionSupportWrite,
		PermissionCatalogRead, PermissionRiskRead,
	},
	// Investigating a payment means reading the customer it belongs to and
	// acting on the money.
	RoleFinance: {
		PermissionCustomersRead, PermissionSubscriptionsRead,
		PermissionFinanceRead, PermissionFinanceWrite,
		PermissionCatalogRead, PermissionAuditExport,
	},
	// Marketing writes and sends, and reads who it is writing to.
	RoleMarketing: {
		PermissionCustomersRead, PermissionMarketingRead, PermissionMarketingWrite,
		PermissionCatalogRead,
	},
}

// separated is what each role must not hold.
//
// Support may not: move money without a finance decision, change configuration
// or a provider credential, manage operator accounts or grants, change a price,
// message every customer, clear the risk signals on a case it owns, or take the
// audit trail off the installation.
//
// Finance may not: change configuration, edit the account a refund concerns,
// answer the customer as support, manage accounts or grants, change the price a
// dispute is about, message every customer, or change system state.
//
// Marketing may not: edit an account it only describes, move money, answer a
// ticket as support, change what a referral pays, or manage grants.
//
// The auditor may not change anything it is reviewing.
var separated = map[Role][]Permission{
	RoleSupport: {
		PermissionFinanceWrite, PermissionSettingsWrite,
		PermissionAdminsWrite, PermissionAdminsRoles,
		PermissionCatalogWrite, PermissionMarketingWrite,
		PermissionSystemWrite, PermissionRiskWrite, PermissionAuditExport,
	},
	RoleFinance: {
		PermissionSettingsWrite, PermissionCustomersWrite, PermissionSupportWrite,
		PermissionAdminsWrite, PermissionAdminsRoles,
		PermissionCatalogWrite, PermissionMarketingWrite, PermissionSystemWrite,
	},
	RoleMarketing: {
		PermissionCustomersWrite, PermissionFinanceWrite, PermissionSupportWrite,
		PermissionSettingsWrite, PermissionAdminsRoles,
	},
	RoleAuditor: {
		PermissionCustomersWrite, PermissionFinanceWrite,
		PermissionSettingsWrite, PermissionAdminsRoles,
	},
}

func TestEachRoleCanDoItsJob(t *testing.T) {
	for role, needed := range jobs {
		granted := PermissionsFor(role)
		for _, permission := range needed {
			if !slices.Contains(granted, permission) {
				t.Fatalf("role %s is missing %s, which its job needs", role, permission)
			}
		}
	}
}

func TestNoRoleHoldsAnotherRolesPermissions(t *testing.T) {
	for role, prohibited := range separated {
		granted := PermissionsFor(role)
		for _, permission := range prohibited {
			if slices.Contains(granted, permission) {
				t.Fatalf("role %s holds %s, which belongs to another job", role, permission)
			}
		}
	}
}

// Account and grant management stays with the owner, so no other role can widen
// what it or anybody else may do.
func TestOnlyTheOwnerManagesAccountsAndRoles(t *testing.T) {
	for _, role := range AllRoles {
		if role == RoleOwner {
			continue
		}
		if slices.Contains(PermissionsFor(role), PermissionAdminsRoles) {
			t.Fatalf("role %s may change grants", role)
		}
		if slices.Contains(PermissionsFor(role), PermissionAdminsWrite) {
			t.Fatalf("role %s may add operator accounts", role)
		}
	}
}

// An auditor exists to look. A write permission in that role is a contradiction
// rather than a convenience.
func TestTheAuditorRoleWritesNothing(t *testing.T) {
	for _, permission := range PermissionsFor(RoleAuditor) {
		if permission == PermissionAuditExport {
			continue
		}
		if !strings.HasSuffix(string(permission), ".read") {
			t.Fatalf("the auditor holds %s, which is not a read", permission)
		}
	}
}

// A role granted a write without the matching read could change something it
// cannot see, which is not a job anybody has.
func TestEveryRoleThatWritesCanAlsoRead(t *testing.T) {
	for _, role := range AllRoles {
		granted := PermissionsFor(role)
		for _, permission := range granted {
			name := string(permission)
			if !strings.HasSuffix(name, ".write") {
				continue
			}
			read := Permission(strings.TrimSuffix(name, ".write") + ".read")
			if !slices.Contains(granted, read) {
				t.Fatalf("role %s may write %s but cannot read it", role, permission)
			}
		}
	}
}
