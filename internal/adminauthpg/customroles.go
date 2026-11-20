package adminauthpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
)

// customRolesTTL is how long this process trusts its copy of the custom
// roles. Another api replica's edit reaches this one within it.
const customRolesTTL = 30 * time.Second

var customRolesRefresh struct {
	sync.Mutex
	at time.Time
}

// RefreshCustomRoles loads the operator-defined roles into rbac when the
// local copy is older than the TTL, or always when force is set.
func (service *Service) RefreshCustomRoles(ctx context.Context, force bool) error {
	customRolesRefresh.Lock()
	defer customRolesRefresh.Unlock()
	if !force && time.Since(customRolesRefresh.at) < customRolesTTL {
		return nil
	}
	rows, err := dbgen.New(service.pool).ListCustomRoles(ctx)
	if err != nil {
		return err
	}
	roles := make([]rbac.CustomRole, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, customRoleFrom(row))
	}
	rbac.SetCustomRoles(roles)
	customRolesRefresh.at = time.Now()
	return nil
}

func customRoleFrom(row dbgen.AdminCustomRole) rbac.CustomRole {
	permissions := make([]rbac.Permission, 0, len(row.Permissions))
	for _, permission := range row.Permissions {
		permissions = append(permissions, rbac.Permission(permission))
	}
	return rbac.CustomRole{Role: rbac.Role(row.Role), Name: row.Name, Permissions: permissions}
}

// ErrRoleNotHeld refuses a custom role carrying a permission its author lacks.
var ErrRoleNotHeld = errors.New("a role may carry only permissions its author holds")

// SaveCustomRole creates or changes a custom role. Its author must hold
// every permission in it, so nobody defines a role stronger than their own,
// and rbac refuses reserved permissions. Every save is audited.
func (service *Service) SaveCustomRole(ctx context.Context, actor Principal, role rbac.CustomRole, request RequestContext) error {
	role.Role = rbac.Role(strings.ToLower(strings.TrimSpace(string(role.Role))))
	role.Name = strings.TrimSpace(role.Name)
	if err := rbac.ValidateCustomRole(role); err != nil {
		return err
	}
	if !actor.Grant.AllowsAll(role.Permissions...) {
		return ErrRoleNotHeld
	}
	permissions := make([]string, 0, len(role.Permissions))
	for _, permission := range role.Permissions {
		permissions = append(permissions, string(permission))
	}
	updatedBy := pgtype.UUID{}
	_ = updatedBy.Scan(actor.Account.ID)
	if _, err := dbgen.New(service.pool).UpsertCustomRole(ctx, dbgen.UpsertCustomRoleParams{
		Role: string(role.Role), Name: role.Name, Permissions: permissions, UpdatedBy: updatedBy,
	}); err != nil {
		return err
	}
	_ = service.AppendAudit(ctx, AuditEntry{
		ActorType: "admin", ActorID: actor.Account.ID, Action: "admin.custom_role.saved", Category: "authorization",
		Outcome: "success", TargetType: "admin_role", TargetID: string(role.Role), RequestID: request.RequestID,
		Metadata: map[string]any{"permissions": permissions},
	})
	return service.RefreshCustomRoles(ctx, true)
}

// DeleteCustomRole removes a custom role and every grant of it.
func (service *Service) DeleteCustomRole(ctx context.Context, actor Principal, role string, request RequestContext) error {
	if !rbac.IsCustom(rbac.Role(role)) {
		return fmt.Errorf("%w: built-in roles cannot be deleted", rbac.ErrInvalidCustomRole)
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if err = queries.RevokeCustomRoleGrants(ctx, role); err != nil {
		return err
	}
	deleted, err := queries.DeleteCustomRole(ctx, role)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_ = service.AppendAudit(ctx, AuditEntry{
		ActorType: "admin", ActorID: actor.Account.ID, Action: "admin.custom_role.deleted", Category: "authorization",
		Outcome: "success", TargetType: "admin_role", TargetID: role, RequestID: request.RequestID,
	})
	return service.RefreshCustomRoles(ctx, true)
}
