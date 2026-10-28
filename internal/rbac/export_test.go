package rbac

import "testing"

// Downloading every customer with their Telegram ID is not what customers.read
// is for. Only the roles that administer the installation may do it; the
// auditor, despite reading everything, may not walk out with the customer base.
func TestOnlyAdministeringRolesMayExportCustomers(t *testing.T) {
	for role, want := range map[Role]bool{
		RoleOwner: true, RoleAdministrator: true,
		RoleSupport: false, RoleFinance: false, RoleMarketing: false, RoleAuditor: false,
	} {
		if got := NewGrant(role).Allows(PermissionCustomersExport); got != want {
			t.Errorf("%s may export customers = %v, want %v", role, got, want)
		}
	}
	if !Known(PermissionCustomersExport) {
		t.Fatal("customers.export is not in the catalogue")
	}
}
