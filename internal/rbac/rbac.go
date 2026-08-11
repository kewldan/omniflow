// Package rbac holds the operator permission catalogue and the built-in role
// definitions for the admin panel.
//
// Authorization lives here, in a package with no transport, database, or
// provider imports, for two reasons. It stays unit-testable without a fixture
// database, and there is exactly one place that answers "may this operator do
// this", so the Go API and the Next.js server boundary cannot drift apart: the
// panel renders from the same permission set the API enforces.
//
// The database records only which roles an operator holds. The mapping from a
// role to its permissions is compiled in, so a row edit can never silently
// widen access.
package rbac

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Permission is a single, granular capability. Names read `<domain>.<verb>`
// and are stable identifiers: they appear in audit metadata and in the session
// payload the panel uses to decide what to render.
type Permission string

const (
	// Operator accounts and their role grants.
	PermissionAdminsRead  Permission = "admins.read"
	PermissionAdminsWrite Permission = "admins.write"
	PermissionAdminsRoles Permission = "admins.roles"

	// The append-only audit trail.
	PermissionAuditRead   Permission = "audit.read"
	PermissionAuditExport Permission = "audit.export"

	// Customer records and their subscriptions.
	PermissionCustomersRead      Permission = "customers.read"
	PermissionCustomersWrite     Permission = "customers.write"
	PermissionSubscriptionsRead  Permission = "subscriptions.read"
	PermissionSubscriptionsWrite Permission = "subscriptions.write"

	// Money: orders, payments, refunds, and wallet adjustments.
	PermissionFinanceRead  Permission = "finance.read"
	PermissionFinanceWrite Permission = "finance.write"

	// Plans, pricing, and promotions.
	PermissionCatalogRead  Permission = "catalog.read"
	PermissionCatalogWrite Permission = "catalog.write"

	// Support desk.
	PermissionSupportRead  Permission = "support.read"
	PermissionSupportWrite Permission = "support.write"

	// Campaigns, news, and referral programme configuration.
	PermissionMarketingRead  Permission = "marketing.read"
	PermissionMarketingWrite Permission = "marketing.write"

	// Installation settings, including authentication policy and OIDC.
	PermissionSettingsRead  Permission = "settings.read"
	PermissionSettingsWrite Permission = "settings.write"

	// Health, jobs, maintenance mode, and backups.
	PermissionSystemRead  Permission = "system.read"
	PermissionSystemWrite Permission = "system.write"

	// Blocklist adjudication and anomaly review.
	//
	// Risk is its own capability rather than part of customer administration
	// because the two answer different questions. Reading a customer record is
	// routine support work; deciding that an external list's opinion of them is
	// correct is a judgement with consequences, and an installation should be
	// able to grant one without the other.
	PermissionRiskRead  Permission = "risk.read"
	PermissionRiskWrite Permission = "risk.write"

	// The gift register: revocation and refund of unredeemed gifts.
	PermissionGiftsRead  Permission = "gifts.read"
	PermissionGiftsWrite Permission = "gifts.write"

	// The digital-goods shop: catalogue, provider credentials, and orders.
	//
	// Separate from `catalog` because a goods provider holds the operator's own
	// money. Someone who may price a VPN plan should not automatically be able
	// to rotate the credential that spends from a funded balance.
	PermissionGoodsRead  Permission = "goods.read"
	PermissionGoodsWrite Permission = "goods.write"
)

// AllPermissions is the complete catalogue, ordered for stable rendering and
// for deterministic test assertions.
var AllPermissions = []Permission{
	PermissionAdminsRead, PermissionAdminsWrite, PermissionAdminsRoles,
	PermissionAuditRead, PermissionAuditExport,
	PermissionCustomersRead, PermissionCustomersWrite,
	PermissionSubscriptionsRead, PermissionSubscriptionsWrite,
	PermissionFinanceRead, PermissionFinanceWrite,
	PermissionCatalogRead, PermissionCatalogWrite,
	PermissionSupportRead, PermissionSupportWrite,
	PermissionMarketingRead, PermissionMarketingWrite,
	PermissionSettingsRead, PermissionSettingsWrite,
	PermissionSystemRead, PermissionSystemWrite,
	PermissionRiskRead, PermissionRiskWrite,
	PermissionGiftsRead, PermissionGiftsWrite,
	PermissionGoodsRead, PermissionGoodsWrite,
}

// Known reports whether a string names a permission this build defines.
//
// It exists because permissions are now referenced from data as well as from
// code: an owner maps an MCP tool to one, and a typo there would produce a tool
// nobody can invoke, which reads as a bug rather than as a mistake. Checking at
// the point of configuration turns it into a message about the field.
func Known(candidate Permission) bool {
	return slices.Contains(AllPermissions, candidate)
}

// Role is a built-in operator role. The set is fixed in v0.6; operator-defined
// roles are planned for a later version and will extend this catalogue rather
// than replace it.
type Role string

const (
	RoleOwner         Role = "owner"
	RoleAdministrator Role = "administrator"
	RoleSupport       Role = "support"
	RoleFinance       Role = "finance"
	RoleMarketing     Role = "marketing"
	RoleAuditor       Role = "auditor"
)

// AllRoles lists every built-in role, most privileged first.
var AllRoles = []Role{
	RoleOwner, RoleAdministrator, RoleSupport, RoleFinance, RoleMarketing, RoleAuditor,
}

// ErrUnknownRole reports a role string that is not part of the built-in set.
var ErrUnknownRole = errors.New("unknown role")

// readOnlyPermissions is every capability that only observes state. The auditor
// role is defined as exactly this set plus export, so a permission added later
// is not accidentally granted to auditors by an omission here.
var readOnlyPermissions = []Permission{
	PermissionAdminsRead, PermissionAuditRead,
	PermissionCustomersRead, PermissionSubscriptionsRead,
	PermissionFinanceRead, PermissionCatalogRead,
	PermissionSupportRead, PermissionMarketingRead,
	PermissionSettingsRead, PermissionSystemRead,
	PermissionRiskRead, PermissionGiftsRead, PermissionGoodsRead,
}

// rolePermissions maps each built-in role to the permissions it grants.
//
// Owner is the only role that may manage operator accounts and role grants.
// Keeping that owner-only means a compromised administrator session cannot
// mint itself a second account or widen its own privileges, which is the
// escalation path that matters most in a panel of this kind.
var rolePermissions = map[Role][]Permission{
	RoleOwner: AllPermissions,

	RoleAdministrator: {
		PermissionAdminsRead,
		PermissionAuditRead, PermissionAuditExport,
		PermissionCustomersRead, PermissionCustomersWrite,
		PermissionSubscriptionsRead, PermissionSubscriptionsWrite,
		PermissionFinanceRead, PermissionFinanceWrite,
		PermissionCatalogRead, PermissionCatalogWrite,
		PermissionSupportRead, PermissionSupportWrite,
		PermissionMarketingRead, PermissionMarketingWrite,
		PermissionSettingsRead, PermissionSettingsWrite,
		PermissionSystemRead, PermissionSystemWrite,
		PermissionRiskRead, PermissionRiskWrite,
		PermissionGiftsRead, PermissionGiftsWrite,
		PermissionGoodsRead, PermissionGoodsWrite,
	},

	// Support reads the risk, gift, and shop surfaces because those are the
	// questions customers actually ask — "why was I refused", "where is the
	// gift I sent", "my Stars have not arrived" — and answering them needs the
	// record, not the ability to change it.
	RoleSupport: {
		PermissionAuditRead,
		PermissionCustomersRead, PermissionCustomersWrite,
		PermissionSubscriptionsRead, PermissionSubscriptionsWrite,
		PermissionSupportRead, PermissionSupportWrite,
		PermissionCatalogRead,
		PermissionRiskRead, PermissionGiftsRead, PermissionGoodsRead,
	},

	// Finance may revoke and refund an unredeemed gift, because that is a
	// refund decision. It may read the shop, because a shop order is money, and
	// it may not rotate a provider credential, because that is configuration.
	RoleFinance: {
		PermissionAuditRead, PermissionAuditExport,
		PermissionCustomersRead, PermissionSubscriptionsRead,
		PermissionFinanceRead, PermissionFinanceWrite,
		PermissionCatalogRead,
		PermissionRiskRead,
		PermissionGiftsRead, PermissionGiftsWrite,
		PermissionGoodsRead,
	},

	RoleMarketing: {
		PermissionAuditRead,
		PermissionCustomersRead,
		PermissionCatalogRead,
		PermissionMarketingRead, PermissionMarketingWrite,
	},

	RoleAuditor: append(slices.Clone(readOnlyPermissions), PermissionAuditExport),
}

// ParseRole validates a role string coming from the database or an API request.
func ParseRole(value string) (Role, error) {
	role := Role(strings.TrimSpace(strings.ToLower(value)))
	if !slices.Contains(AllRoles, role) {
		return "", fmt.Errorf("%w: %q", ErrUnknownRole, value)
	}
	return role, nil
}

// PermissionsFor returns the permissions a single role grants. The returned
// slice is a copy, so a caller cannot mutate the compiled-in catalogue.
func PermissionsFor(role Role) []Permission {
	return slices.Clone(rolePermissions[role])
}

// Grant is the effective authority of one operator: the union of every role
// they hold. Construct it with NewGrant and ask it questions with Allows.
type Grant struct {
	roles       []Role
	permissions map[Permission]struct{}
}

// NewGrant builds the effective permission set for a set of roles. Unknown
// roles are ignored rather than rejected: a role removed from the catalogue in
// a later version must not lock an operator out of the panel entirely, and the
// database keeps its own CHECK constraint on the column.
func NewGrant(roles ...Role) Grant {
	grant := Grant{
		roles:       make([]Role, 0, len(roles)),
		permissions: make(map[Permission]struct{}, len(AllPermissions)),
	}
	for _, role := range roles {
		permissions, known := rolePermissions[role]
		if !known {
			continue
		}
		grant.roles = append(grant.roles, role)
		for _, permission := range permissions {
			grant.permissions[permission] = struct{}{}
		}
	}
	slices.Sort(grant.roles)
	return grant
}

// Allows reports whether the operator holds a permission.
func (grant Grant) Allows(permission Permission) bool {
	_, ok := grant.permissions[permission]
	return ok
}

// AllowsAll reports whether the operator holds every listed permission. An
// empty list is allowed, which makes an endpoint with no declared requirement
// reachable by any authenticated operator.
func (grant Grant) AllowsAll(permissions ...Permission) bool {
	for _, permission := range permissions {
		if !grant.Allows(permission) {
			return false
		}
	}
	return true
}

// HasRole reports whether the operator holds a specific role. Reserved for the
// few decisions that are genuinely about identity rather than capability, such
// as "only an owner may grant the owner role".
func (grant Grant) HasRole(role Role) bool {
	return slices.Contains(grant.roles, role)
}

// Roles returns the operator's roles, sorted.
func (grant Grant) Roles() []Role { return slices.Clone(grant.roles) }

// Permissions returns the effective permission set in catalogue order. The
// panel receives this list and renders from it, so a hidden route is never the
// only thing standing between an operator and an action they cannot perform.
func (grant Grant) Permissions() []Permission {
	permissions := make([]Permission, 0, len(grant.permissions))
	for _, permission := range AllPermissions {
		if grant.Allows(permission) {
			permissions = append(permissions, permission)
		}
	}
	return permissions
}
