//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// A custom role is saved only within its author's own permissions, becomes
// grantable, and is revoked from its holders when deleted.
func TestCustomRoles(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	t.Cleanup(func() { rbac.SetCustomRoles(nil) })
	service, err := adminauthpg.New(harness.pool, operationsKey, adminauthpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	owner := harness.operator(ctx, t, "owner@example.test")
	holder := harness.operator(ctx, t, "holder@example.test")
	author := adminauthpg.Principal{Account: adminauthpg.Account{ID: owner.AdminID}, Grant: rbac.NewGrant(rbac.RoleOwner)}
	desk := rbac.CustomRole{Role: "custom-desk", Name: "Desk", Permissions: []rbac.Permission{rbac.PermissionSupportRead, rbac.PermissionSupportWrite}}

	weak := adminauthpg.Principal{Account: author.Account, Grant: rbac.NewGrant(rbac.RoleMarketing)}
	if err = service.SaveCustomRole(ctx, weak, desk, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrRoleNotHeld) {
		t.Fatalf("a role stronger than its author: %v", err)
	}
	if err = service.SaveCustomRole(ctx, author, desk, adminauthpg.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err = rbac.ParseRole("custom-desk"); err != nil {
		t.Fatalf("a saved role must be grantable: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO admin_user_roles (admin_user_id, role) VALUES ($1::uuid, 'custom-desk')`, holder.AdminID); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO admin_user_roles (admin_user_id, role) VALUES ($1::uuid, 'superuser')`, holder.AdminID); err == nil {
		t.Fatal("an unknown role name must be refused by the table")
	}
	if err = service.DeleteCustomRole(ctx, author, "custom-desk", adminauthpg.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var held int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM admin_user_roles WHERE role = 'custom-desk'`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("grants after delete = %d, %v", held, err)
	}
	if _, err = rbac.ParseRole("custom-desk"); err == nil {
		t.Fatal("a deleted role must not stay grantable")
	}
}
