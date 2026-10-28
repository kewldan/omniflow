//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// ownerPrincipal signs the bootstrapped owner in and resolves the session, so
// a test acts with exactly the grant a real request would carry.
func ownerPrincipal(t *testing.T, ctx context.Context, service *adminauthpg.Service) adminauthpg.Principal {
	t.Helper()
	return signedInPrincipal(t, ctx, service, "owner@example.com", "correct horse battery staple")
}

func signedInPrincipal(
	t *testing.T, ctx context.Context, service *adminauthpg.Service, email, password string,
) adminauthpg.Principal {
	t.Helper()
	result, err := service.Authenticate(ctx, email, password, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("authenticate %s: %v", email, err)
	}
	principal, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("resolve %s: %v", email, err)
	}
	return principal
}

func countSecurityNotices(t *testing.T, ctx context.Context, harness *harness, event string) int {
	t.Helper()
	var count int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM operator_notifications WHERE kind = 'security' AND payload->>'event' = $1`,
		event,
	).Scan(&count); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	return count
}

func countAudit(t *testing.T, ctx context.Context, harness *harness, action string) int {
	t.Helper()
	var count int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action = $1`, action,
	).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return count
}

// The whole path a new support agent takes: invited with a role, redeems the
// link with their own password, and signs in holding exactly that role.
func TestAdminInvitationIsAcceptedAndTheInviteeSignsIn(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)
	owner := ownerPrincipal(t, ctx, service)

	issued, err := service.Invite(ctx, adminauthpg.InviteInput{
		Email: " Agent@Example.com ", Roles: []rbac.Role{rbac.RoleSupport},
	}, owner, adminauthpg.RequestContext{RequestID: "invite"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if issued.Token == "" || issued.Invitation.Email != "Agent@Example.com" {
		t.Fatalf("issued = %+v", issued.Invitation)
	}

	// Only the digest is stored: the token appears nowhere in the table.
	var stored int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_invitations WHERE token_hash = convert_to($1, 'UTF8')`, issued.Token,
	).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the plaintext token was stored: %d %v", stored, err)
	}

	open, err := service.ListInvitations(ctx)
	if err != nil || len(open) != 1 || open[0].InvitedByName != "Owner" {
		t.Fatalf("pending list = %+v, %v", open, err)
	}

	preview, err := service.PreviewInvitation(ctx, issued.Token)
	if err != nil || len(preview.Roles) != 1 || preview.Roles[0] != rbac.RoleSupport {
		t.Fatalf("preview = %+v, %v", preview, err)
	}

	account, err := service.AcceptInvitation(ctx, adminauthpg.AcceptInvitationInput{
		Token: issued.Token, DisplayName: "Agent", Password: "a long enough passphrase",
	}, adminauthpg.RequestContext{RequestID: "accept"})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	agent := signedInPrincipal(t, ctx, service, "agent@example.com", "a long enough passphrase")
	if agent.Account.ID != account.ID {
		t.Fatalf("signed in as %s, created %s", agent.Account.ID, account.ID)
	}
	if !agent.Grant.Allows(rbac.PermissionSupportWrite) || agent.Grant.Allows(rbac.PermissionAdminsWrite) {
		t.Fatalf("the invitee holds %v", agent.Grant.Permissions())
	}

	if open, _ = service.ListInvitations(ctx); len(open) != 0 {
		t.Fatalf("an accepted invitation is still listed: %+v", open)
	}
	if countAudit(t, ctx, harness, "admin.invitation.created") != 1 ||
		countAudit(t, ctx, harness, "admin.invitation.accepted") != 1 {
		t.Fatal("the invitation was not audited at both ends")
	}
	if countSecurityNotices(t, ctx, harness, "admin.invitation.accepted") != 1 {
		t.Fatal("acceptance queued no security notice")
	}
	// The notice carries only allowlisted fields: no address, no token.
	var payload string
	if err = harness.pool.QueryRow(ctx,
		`SELECT payload::text FROM operator_notifications WHERE payload->>'event' = 'admin.invitation.accepted'`,
	).Scan(&payload); err != nil {
		t.Fatalf("read notice: %v", err)
	}
	for _, leaked := range []string{"example.com", issued.Token} {
		if containsFold(payload, leaked) {
			t.Fatalf("the notice payload carries %q: %s", leaked, payload)
		}
	}
}

// A link is single-use: the second redemption is refused, even from a second
// browser, and creates nothing.
func TestAdminInvitationCannotBeReused(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)
	owner := ownerPrincipal(t, ctx, service)

	issued, err := service.Invite(ctx, adminauthpg.InviteInput{
		Email: "agent@example.com", Roles: []rbac.Role{rbac.RoleSupport},
	}, owner, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	input := adminauthpg.AcceptInvitationInput{
		Token: issued.Token, DisplayName: "Agent", Password: "a long enough passphrase",
	}
	if _, err = service.AcceptInvitation(ctx, input, adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	input.DisplayName = "Somebody else"
	if _, err = service.AcceptInvitation(ctx, input, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrInvitationInvalid) {
		t.Fatalf("a used link was accepted again: %v", err)
	}
	if _, err = service.PreviewInvitation(ctx, issued.Token); !errors.Is(err, adminauthpg.ErrInvitationInvalid) {
		t.Fatalf("a used link still previews: %v", err)
	}

	// Inviting an address that already has an account is refused: changing
	// an existing operator's access is a role change, not an invitation.
	if _, err = service.Invite(ctx, adminauthpg.InviteInput{
		Email: "AGENT@example.com", Roles: []rbac.Role{rbac.RoleFinance},
	}, owner, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrConflict) {
		t.Fatalf("an existing address was invited: %v", err)
	}
}

// An expired link is refused at preview and at acceptance; a reissued one
// retires its predecessor; a revoked one stops working.
func TestAdminInvitationExpiryReissueAndRevocation(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	now := time.Now()
	clock := func() time.Time { return now }
	service := newAdminService(t, harness, clock)
	bootstrapOwner(t, ctx, service)
	owner := ownerPrincipal(t, ctx, service)

	first, err := service.Invite(ctx, adminauthpg.InviteInput{
		Email: "late@example.com", Roles: []rbac.Role{rbac.RoleAuditor},
	}, owner, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	// Expiry is enforced against the database clock too, so age the row
	// itself rather than only the service clock.
	if _, err = harness.pool.Exec(ctx,
		`UPDATE admin_invitations SET created_at = now() - interval '4 days', expires_at = now() - interval '1 day'`,
	); err != nil {
		t.Fatalf("age invitation: %v", err)
	}
	if _, err = service.PreviewInvitation(ctx, first.Token); !errors.Is(err, adminauthpg.ErrInvitationInvalid) {
		t.Fatalf("an expired link previews: %v", err)
	}
	if _, err = service.AcceptInvitation(ctx, adminauthpg.AcceptInvitationInput{
		Token: first.Token, DisplayName: "Late", Password: "a long enough passphrase",
	}, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrInvitationInvalid) {
		t.Fatalf("an expired link was accepted: %v", err)
	}
	// Expired but not settled: it stays listed so the owner can see it lapsed.
	if open, _ := service.ListInvitations(ctx); len(open) != 1 {
		t.Fatalf("the expired invitation is not listed: %+v", open)
	}

	second, err := service.Invite(ctx, adminauthpg.InviteInput{
		Email: "late@example.com", Roles: []rbac.Role{rbac.RoleAuditor},
	}, owner, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if open, _ := service.ListInvitations(ctx); len(open) != 1 || open[0].ID != second.Invitation.ID {
		t.Fatalf("reissuing left %+v open", open)
	}

	if err = service.RevokeInvitation(ctx, second.Invitation.ID, owner, adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err = service.RevokeInvitation(ctx, second.Invitation.ID, owner, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrNotFound) {
		t.Fatalf("revoking twice returned %v", err)
	}
	if _, err = service.AcceptInvitation(ctx, adminauthpg.AcceptInvitationInput{
		Token: second.Token, DisplayName: "Late", Password: "a long enough passphrase",
	}, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrInvitationInvalid) {
		t.Fatalf("a revoked link was accepted: %v", err)
	}
	if countAudit(t, ctx, harness, "admin.invitation.revoked") != 1 {
		t.Fatal("revocation was not audited")
	}
}

// Nobody may invite somebody stronger than themselves. Only the owner holds
// admins.write today, so the guard is exercised with grants that hold it in
// combination with a narrower role set.
func TestAdminInvitationCannotEscalate(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	administrator := adminauthpg.Principal{Account: owner, Grant: rbac.NewGrant(rbac.RoleAdministrator)}
	for _, roles := range [][]rbac.Role{{rbac.RoleOwner}, {rbac.RoleSupport, rbac.RoleOwner}} {
		if _, err := service.Invite(ctx, adminauthpg.InviteInput{
			Email: "climber@example.com", Roles: roles,
		}, administrator, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrForbidden) {
			t.Fatalf("an administrator invited %v: %v", roles, err)
		}
	}
	support := adminauthpg.Principal{Account: owner, Grant: rbac.NewGrant(rbac.RoleSupport)}
	if _, err := service.Invite(ctx, adminauthpg.InviteInput{
		Email: "climber@example.com", Roles: []rbac.Role{rbac.RoleFinance},
	}, support, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrForbidden) {
		t.Fatalf("support invited finance: %v", err)
	}
	if _, err := service.Invite(ctx, adminauthpg.InviteInput{
		Email: "nobody", Roles: []rbac.Role{rbac.RoleSupport},
	}, adminauthpg.Principal{Account: owner, Grant: rbac.NewGrant(rbac.RoleOwner)},
		adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrValidation) {
		t.Fatalf("a malformed address was invited: %v", err)
	}
}

// The guards on changing an existing operator's roles: never your own, never
// beyond your own authority, and never leaving the installation without an
// owner. Every change is audited and announced.
func TestAdminRoleChangeGuards(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)
	owner := ownerPrincipal(t, ctx, service)

	agent, err := service.CreateAccount(ctx, adminauthpg.CreateAccountInput{
		Email: "agent@example.com", DisplayName: "Agent",
		Password: "a long enough passphrase", Roles: []rbac.Role{rbac.RoleSupport},
	}, owner.Account.ID, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	if _, err = service.SetRoles(ctx, owner.Account.ID, []rbac.Role{rbac.RoleOwner, rbac.RoleAuditor},
		owner, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrSelfRoleChange) {
		t.Fatalf("an owner edited their own roles: %v", err)
	}
	if _, err = service.SetRoles(ctx, agent.ID, nil, owner, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrValidation) {
		t.Fatalf("an empty role set was accepted: %v", err)
	}

	changed, err := service.SetRoles(ctx, agent.ID, []rbac.Role{rbac.RoleSupport, rbac.RoleFinance},
		owner, adminauthpg.RequestContext{RequestID: "roles"})
	if err != nil || len(changed.Roles) != 2 {
		t.Fatalf("owner widened agent: %+v %v", changed.Roles, err)
	}
	if countAudit(t, ctx, harness, "admin.account.roles_changed") != 1 ||
		countSecurityNotices(t, ctx, harness, "admin.roles_changed") != 1 {
		t.Fatal("the role change was not audited and announced")
	}
	// Saving the same set again is not a second event.
	if _, err = service.SetRoles(ctx, agent.ID, []rbac.Role{rbac.RoleFinance, rbac.RoleSupport},
		owner, adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	if countAudit(t, ctx, harness, "admin.account.roles_changed") != 1 {
		t.Fatal("an unchanged save was audited as a change")
	}

	// A grant that may manage roles but holds less than the role it touches is
	// refused in both directions: it cannot add the role, and it cannot take
	// it away from somebody who has it.
	narrow := adminauthpg.Principal{
		Account: owner.Account, Grant: rbac.NewGrant(rbac.RoleSupport),
	}
	narrow.Account.ID = "00000000-0000-0000-0000-0000000000aa"
	if _, err = service.SetRoles(ctx, agent.ID, []rbac.Role{rbac.RoleSupport, rbac.RoleFinance, rbac.RoleAuditor},
		narrow, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrForbidden) {
		t.Fatalf("a narrow grant added auditor: %v", err)
	}
	if _, err = service.SetRoles(ctx, agent.ID, []rbac.Role{rbac.RoleSupport},
		narrow, adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrForbidden) {
		t.Fatalf("a narrow grant removed finance: %v", err)
	}
}

func containsFold(haystack, needle string) bool {
	return needle != "" && strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
