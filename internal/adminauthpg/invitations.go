package adminauthpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
)

// InvitationTTL bounds how long an invitation link stays redeemable.
//
// Three days covers a weekend between the owner sending the link and the new
// operator opening it, and is still short enough that a link pasted into a chat
// history stops being a way into the panel soon after.
const InvitationTTL = 72 * time.Hour

// Invitation errors returned to transport.
var (
	// ErrInvitationInvalid covers an unknown, expired, revoked, or already
	// accepted link with one answer, so the public preview cannot be used to
	// learn which of those a guessed token is.
	ErrInvitationInvalid = errors.New("invitation is not valid")
	// ErrSelfRoleChange refuses an operator editing their own grants. Nobody
	// should be able to widen their own access, and narrowing it by accident is
	// a lock-out that only another owner can undo.
	ErrSelfRoleChange = errors.New("an operator cannot change their own roles")
)

// Invitation is a pending invitation as the panel lists it. The token is never
// part of it: it exists in one response and nowhere else.
type Invitation struct {
	ID            string
	Email         string
	Roles         []rbac.Role
	CreatedAt     time.Time
	ExpiresAt     time.Time
	InvitedBy     string
	InvitedByName string
}

// IssuedInvitation is the result of inviting somebody: the stored invitation
// and the one copy of the token that will ever exist.
type IssuedInvitation struct {
	Invitation Invitation
	Token      string
}

// InvitationPreview is what the public acceptance screen may show before the
// invitee has proven anything beyond holding the link.
type InvitationPreview struct {
	Email     string
	Roles     []rbac.Role
	ExpiresAt time.Time
}

// InviteInput describes a new invitation.
type InviteInput struct {
	Email string
	Roles []rbac.Role
}

// Invite issues a single-use link that creates an operator account with the
// listed roles.
//
// The actor's grant is checked here, not only at the route: the route proves
// the actor may manage accounts at all, and this proves the account they are
// creating is no stronger than they are. An address that already has an
// account is refused, because an invitation is for a new operator — changing
// what an existing one may do is SetRoles.
func (service *Service) Invite(
	ctx context.Context, input InviteInput, actor Principal, request RequestContext,
) (IssuedInvitation, error) {
	normalized := NormalizeEmail(input.Email)
	if !plausibleEmail(normalized) {
		return IssuedInvitation{}, ErrValidation
	}
	roles := uniqueRoles(input.Roles)
	if len(roles) == 0 {
		return IssuedInvitation{}, ErrValidation
	}
	if !actor.Grant.CanAssign(roles...) {
		return IssuedInvitation{}, ErrForbidden
	}
	actorID, err := parseUUID(actor.Account.ID)
	if err != nil {
		return IssuedInvitation{}, err
	}

	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return IssuedInvitation{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))

	var issued IssuedInvitation
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.GetAdminUserByEmail(ctx, normalized); txErr == nil {
			return ErrConflict
		} else if !errors.Is(txErr, pgx.ErrNoRows) {
			return txErr
		}
		retired, txErr := queries.RetireOpenAdminInvitations(ctx, dbgen.RetireOpenAdminInvitationsParams{
			RevokedBy: actorID, EmailNormalized: normalized,
		})
		if txErr != nil {
			return txErr
		}
		row, txErr := queries.CreateAdminInvitation(ctx, dbgen.CreateAdminInvitationParams{
			Email:           strings.TrimSpace(input.Email),
			EmailNormalized: normalized,
			Roles:           roleStrings(roles),
			TokenHash:       digest[:],
			InvitedBy:       actorID,
			Lifetime:        interval(InvitationTTL),
		})
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actor.Account.ID,
			Action: "admin.invitation.created", Category: "authorization",
			TargetType: "admin_invitation", TargetID: uuidString(row.ID),
			RequestID: request.RequestID,
			// The address is recorded for the same reason account creation
			// records it: an operator is not customer data, and the trail must
			// say who was let in. The token is not, in any form.
			Metadata: map[string]any{
				"email": row.Email, "roles": row.Roles, "replaced": retired,
				"expiresAt": row.ExpiresAt.Time.UTC().Format(time.RFC3339),
			},
		}); txErr != nil {
			return txErr
		}
		issued = IssuedInvitation{
			Invitation: Invitation{
				ID: uuidString(row.ID), Email: row.Email, Roles: roles,
				CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time,
				InvitedBy: actor.Account.ID, InvitedByName: actor.Account.DisplayName,
			},
			Token: token,
		}
		return nil
	})
	if err != nil {
		return IssuedInvitation{}, err
	}
	return issued, nil
}

// ListInvitations returns the open invitations, newest first. Expired ones are
// included; the caller compares ExpiresAt with the clock.
func (service *Service) ListInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := dbgen.New(service.pool).ListOpenAdminInvitations(ctx, 100)
	if err != nil {
		return nil, err
	}
	invitations := make([]Invitation, 0, len(rows))
	for _, row := range rows {
		invitations = append(invitations, Invitation{
			ID: uuidString(row.ID), Email: row.Email, Roles: parseRoleStrings(row.Roles),
			CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time,
			InvitedBy: uuidString(row.InvitedBy), InvitedByName: row.InvitedByName,
		})
	}
	return invitations, nil
}

// RevokeInvitation retires an open invitation so its link stops working.
func (service *Service) RevokeInvitation(
	ctx context.Context, invitationID string, actor Principal, request RequestContext,
) error {
	id, err := parseUUID(invitationID)
	if err != nil {
		return err
	}
	actorID, err := parseUUID(actor.Account.ID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.RevokeAdminInvitation(ctx, dbgen.RevokeAdminInvitationParams{
			RevokedBy: actorID, InvitationID: id,
		})
		// Already accepted, already revoked, or never existed: in each case
		// there is no open invitation by that identifier.
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actor.Account.ID,
			Action: "admin.invitation.revoked", Category: "authorization",
			TargetType: "admin_invitation", TargetID: uuidString(row.ID),
			RequestID: request.RequestID,
			Metadata:  map[string]any{"email": row.Email},
		})
	})
}

// PreviewInvitation reads what an invitation offers, for the acceptance
// screen. Every failure is ErrInvitationInvalid.
func (service *Service) PreviewInvitation(ctx context.Context, token string) (InvitationPreview, error) {
	if strings.TrimSpace(token) == "" {
		return InvitationPreview{}, ErrInvitationInvalid
	}
	digest := sha256.Sum256([]byte(token))
	row, err := dbgen.New(service.pool).GetAdminInvitationByToken(ctx, digest[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return InvitationPreview{}, ErrInvitationInvalid
	}
	if err != nil {
		return InvitationPreview{}, err
	}
	if !invitationOpen(row, service.now()) {
		return InvitationPreview{}, ErrInvitationInvalid
	}
	return InvitationPreview{
		Email: row.Email, Roles: parseRoleStrings(row.Roles), ExpiresAt: row.ExpiresAt.Time,
	}, nil
}

// AcceptInvitationInput is what the invitee supplies.
type AcceptInvitationInput struct {
	Token       string
	DisplayName string
	Password    string
	Locale      string
	Timezone    string
}

// AcceptInvitation redeems a link and creates the operator account it
// describes, with the roles the inviter chose.
//
// The invitation row is locked first, so a double submission creates one
// account; the consuming UPDATE is additionally conditional on the link still
// being open, so an invitation that expired or was revoked between the preview
// and the submit is refused here rather than honoured. The account, its roles,
// the consumption, the audit event, and the security notice commit together.
func (service *Service) AcceptInvitation(
	ctx context.Context, input AcceptInvitationInput, request RequestContext,
) (Account, error) {
	if err := adminauth.ValidatePassword(input.Password); err != nil {
		return Account{}, err
	}
	name := strings.TrimSpace(input.DisplayName)
	if name == "" || len([]rune(name)) > 80 {
		return Account{}, ErrValidation
	}
	if strings.TrimSpace(input.Token) == "" {
		return Account{}, ErrInvitationInvalid
	}
	hash, err := adminauth.HashPassword(input.Password, service.password)
	if err != nil {
		return Account{}, err
	}
	digest := sha256.Sum256([]byte(input.Token))

	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		invitation, txErr := queries.LockAdminInvitationByToken(ctx, digest[:])
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrInvitationInvalid
		}
		if txErr != nil {
			return txErr
		}
		if !invitationOpen(invitation, service.now()) {
			return ErrInvitationInvalid
		}

		created, txErr := queries.CreateAdminUser(ctx, dbgen.CreateAdminUserParams{
			Email:             invitation.Email,
			EmailNormalized:   invitation.EmailNormalized,
			DisplayName:       name,
			PasswordHash:      optionalText(hash),
			Locale:            normalizeLocale(input.Locale),
			Timezone:          normalizeTimezone(input.Timezone),
			PasswordChangedAt: timestamp(service.now()),
		})
		// Somebody created an account for this address after the invitation
		// was issued. The invitation cannot be honoured without merging two
		// people's access, so it is refused and left for the owner to revoke.
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if txErr != nil {
			return txErr
		}

		roles := parseRoleStrings(invitation.Roles)
		for _, role := range roles {
			if txErr = queries.GrantAdminRole(ctx, dbgen.GrantAdminRoleParams{
				AdminUserID: created.ID, Role: string(role), GrantedBy: invitation.InvitedBy,
			}); txErr != nil {
				return txErr
			}
		}
		if _, txErr = queries.AcceptAdminInvitation(ctx, dbgen.AcceptAdminInvitationParams{
			AdminUserID: created.ID, InvitationID: invitation.ID,
		}); txErr != nil {
			if errors.Is(txErr, pgx.ErrNoRows) {
				return ErrInvitationInvalid
			}
			return txErr
		}

		createdID := uuidString(created.ID)
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: createdID,
			Action: "admin.invitation.accepted", Category: "authorization",
			TargetType: "admin_user", TargetID: createdID, RequestID: request.RequestID,
			Metadata: map[string]any{
				"invitationId": uuidString(invitation.ID),
				"invitedBy":    uuidString(invitation.InvitedBy),
				"roles":        invitation.Roles,
				"email":        invitation.Email,
			},
		}); txErr != nil {
			return txErr
		}
		// A new person can now sign in to the panel. That is announced to the
		// operator group whether or not anything looks wrong, because the
		// group is where somebody notices an invitation they did not expect.
		if txErr = notifySecurity(
			ctx, queries, "admin.invitation.accepted", createdID,
			uuidString(invitation.ID), "invitation",
		); txErr != nil {
			return txErr
		}
		account = service.accountFrom(created, roles)
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// ErrValidation reports input that is malformed rather than refused.
var ErrValidation = errors.New("input is not valid")

func invitationOpen(row dbgen.AdminInvitation, now time.Time) bool {
	return !row.AcceptedAt.Valid && !row.RevokedAt.Valid && row.ExpiresAt.Time.After(now)
}

func parseRoleStrings(values []string) []rbac.Role {
	roles := make([]rbac.Role, 0, len(values))
	for _, value := range values {
		role, err := rbac.ParseRole(value)
		if err != nil {
			continue
		}
		roles = append(roles, role)
	}
	return roles
}

func uniqueRoles(roles []rbac.Role) []rbac.Role {
	unique := make([]rbac.Role, 0, len(roles))
	for _, role := range rbac.AllRoles {
		for _, candidate := range roles {
			if candidate == role {
				unique = append(unique, role)
				break
			}
		}
	}
	return unique
}

// plausibleEmail is a shape check, not validation: the address is only ever
// shown back to people and matched against at sign-in, so it needs to be one
// line with an @ in it and fit the column.
func plausibleEmail(normalized string) bool {
	if len(normalized) < 3 || len(normalized) > 254 {
		return false
	}
	at := strings.IndexByte(normalized, '@')
	return at > 0 && at < len(normalized)-1 && !strings.ContainsAny(normalized, " \t\r\n")
}
