package adminauthpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
)

// PasswordResetTTL bounds how long a reset link stays usable.
const PasswordResetTTL = time.Hour

// ListAccountsPage is one keyset page of operator accounts.
type ListAccountsPage struct {
	Accounts []Account
	// NextCursor is empty when the page is the last one.
	NextCursor string
}

// ListAccounts returns operator accounts newest first.
func (service *Service) ListAccounts(
	ctx context.Context, status, cursor string, pageSize int32,
) (ListAccountsPage, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 25
	}
	cursorAt, cursorID, err := decodeCursor(cursor)
	if err != nil {
		return ListAccountsPage{}, err
	}

	queries := dbgen.New(service.pool)
	// One extra row tells us whether a further page exists without a second
	// count query.
	rows, err := queries.ListAdminUsers(ctx, dbgen.ListAdminUsersParams{
		CursorCreatedAt: cursorAt,
		CursorID:        cursorID,
		Status:          optionalText(status),
		PageSize:        pageSize + 1,
	})
	if err != nil {
		return ListAccountsPage{}, err
	}

	page := ListAccountsPage{Accounts: make([]Account, 0, len(rows))}
	if len(rows) > int(pageSize) {
		last := rows[pageSize-1]
		page.NextCursor = encodeCursor(last.CreatedAt.Time, uuidString(last.ID))
		rows = rows[:pageSize]
	}

	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	rolesByUser, err := loadRolesForUsers(ctx, queries, ids)
	if err != nil {
		return ListAccountsPage{}, err
	}
	for _, row := range rows {
		page.Accounts = append(page.Accounts, service.accountFrom(row, rolesByUser[uuidString(row.ID)]))
	}
	return page, nil
}

// GetAccount reads one operator account.
func (service *Service) GetAccount(ctx context.Context, adminUserID string) (Account, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Account{}, err
	}
	queries := dbgen.New(service.pool)
	row, err := queries.GetAdminUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	roles, err := loadRoles(ctx, queries, id)
	if err != nil {
		return Account{}, err
	}
	return service.accountFrom(row, roles), nil
}

// CreateAccountInput describes a new operator account.
type CreateAccountInput struct {
	Email       string
	DisplayName string
	Password    string
	Locale      string
	Timezone    string
	Roles       []rbac.Role
}

// CreateAccount provisions an operator. Only an owner reaches this path, which
// transport enforces with rbac.PermissionAdminsWrite.
func (service *Service) CreateAccount(
	ctx context.Context, input CreateAccountInput, actorID string, request RequestContext,
) (Account, error) {
	if err := adminauth.ValidatePassword(input.Password); err != nil {
		return Account{}, err
	}
	normalized := NormalizeEmail(input.Email)
	if normalized == "" || strings.TrimSpace(input.DisplayName) == "" {
		return Account{}, errors.New("an email address and a display name are required")
	}
	hash, err := adminauth.HashPassword(input.Password, service.password)
	if err != nil {
		return Account{}, err
	}
	actor, err := parseUUID(actorID)
	if err != nil {
		return Account{}, err
	}

	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		created, txErr := queries.CreateAdminUser(ctx, dbgen.CreateAdminUserParams{
			Email:             normalizeDisplayEmail(input.Email),
			EmailNormalized:   normalized,
			DisplayName:       strings.TrimSpace(input.DisplayName),
			PasswordHash:      optionalText(hash),
			Locale:            normalizeLocale(input.Locale),
			Timezone:          normalizeTimezone(input.Timezone),
			PasswordChangedAt: timestamp(service.now()),
		})
		// The query is ON CONFLICT DO NOTHING, so an address already in use
		// returns no row rather than a driver-level constraint error.
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if txErr != nil {
			return txErr
		}

		granted := make([]string, 0, len(input.Roles))
		for _, role := range input.Roles {
			if txErr = queries.GrantAdminRole(ctx, dbgen.GrantAdminRoleParams{
				AdminUserID: created.ID, Role: string(role), GrantedBy: actor,
			}); txErr != nil {
				return txErr
			}
			granted = append(granted, string(role))
		}

		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actorID,
			Action: "admin.account.created", Category: "authorization",
			TargetType: "admin_user", TargetID: uuidString(created.ID),
			RequestID: request.RequestID,
			// The address is recorded because an operator account is not
			// customer data and the trail must identify who was provisioned.
			Metadata: map[string]any{"email": created.Email, "roles": granted},
		}); txErr != nil {
			return txErr
		}
		account = service.accountFrom(created, input.Roles)
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// SetRoles replaces an operator's role grants.
//
// Removing the last active owner is refused, so an installation can never be
// left with nobody able to administer it.
func (service *Service) SetRoles(
	ctx context.Context, adminUserID string, roles []rbac.Role, actor Principal, request RequestContext,
) (Account, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Account{}, err
	}
	actorID, err := parseUUID(actor.Account.ID)
	if err != nil {
		return Account{}, err
	}
	// Only an owner may create another owner; otherwise the owner-only
	// boundary could be crossed by anyone holding admins.roles.
	if slices.Contains(roles, rbac.RoleOwner) && !actor.Grant.HasRole(rbac.RoleOwner) {
		return Account{}, ErrForbidden
	}

	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		current, txErr := loadRoles(ctx, queries, id)
		if txErr != nil {
			return txErr
		}
		if slices.Contains(current, rbac.RoleOwner) && !slices.Contains(roles, rbac.RoleOwner) {
			remaining, countErr := queries.CountAdminOwners(ctx, id)
			if countErr != nil {
				return countErr
			}
			if remaining == 0 {
				return ErrLastOwner
			}
		}

		for _, role := range current {
			if !slices.Contains(roles, role) {
				if txErr = queries.RevokeAdminRole(ctx, dbgen.RevokeAdminRoleParams{
					AdminUserID: id, Role: string(role),
				}); txErr != nil {
					return txErr
				}
			}
		}
		for _, role := range roles {
			if txErr = queries.GrantAdminRole(ctx, dbgen.GrantAdminRoleParams{
				AdminUserID: id, Role: string(role), GrantedBy: actorID,
			}); txErr != nil {
				return txErr
			}
		}

		row, txErr := queries.GetAdminUser(ctx, id)
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actor.Account.ID,
			Action: "admin.account.roles_changed", Category: "authorization",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"before": roleStrings(current), "after": roleStrings(roles)},
		}); txErr != nil {
			return txErr
		}
		// Gaining the owner role is the largest privilege change the system
		// allows, so it is announced even though the change was authorized.
		if !slices.Contains(current, rbac.RoleOwner) && slices.Contains(roles, rbac.RoleOwner) {
			if txErr = notifySecurity(
				ctx, queries, "admin.owner_granted", adminUserID,
				service.now().UTC().Format(time.RFC3339), "role_change",
			); txErr != nil {
				return txErr
			}
		}
		account = service.accountFrom(row, roles)
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// SetStatus suspends or reactivates an operator, revoking live sessions when
// the account stops being active.
func (service *Service) SetStatus(
	ctx context.Context, adminUserID, status, actorID string, request RequestContext,
) (Account, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Account{}, err
	}
	if status != "active" && status != "suspended" && status != "disabled" {
		return Account{}, errors.New("status must be active, suspended, or disabled")
	}

	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if status != "active" {
			roles, txErr := loadRoles(ctx, queries, id)
			if txErr != nil {
				return txErr
			}
			// Suspending the last owner locks everyone out just as surely as
			// revoking the role does, so it is refused for the same reason.
			if slices.Contains(roles, rbac.RoleOwner) {
				remaining, countErr := queries.CountAdminOwners(ctx, id)
				if countErr != nil {
					return countErr
				}
				if remaining == 0 {
					return ErrLastOwner
				}
			}
		}

		row, txErr := queries.SetAdminUserStatus(ctx, dbgen.SetAdminUserStatusParams{
			AdminUserID: id, Status: status,
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}

		if status != "active" {
			if _, txErr = queries.RevokeAdminSessionsForUser(ctx, dbgen.RevokeAdminSessionsForUserParams{
				AdminUserID: id, RevokedReason: optionalText("admin_revoked"),
			}); txErr != nil {
				return txErr
			}
		}

		roles, txErr := loadRoles(ctx, queries, id)
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actorID,
			Action: "admin.account.status_changed", Category: "authorization",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"status": status},
		}); txErr != nil {
			return txErr
		}
		account = service.accountFrom(row, roles)
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// UpdateProfile changes an operator's own display name and preferences.
func (service *Service) UpdateProfile(
	ctx context.Context, adminUserID, displayName, locale, timezone string, request RequestContext,
) (Account, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Account{}, err
	}
	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpdateAdminUserProfile(ctx, dbgen.UpdateAdminUserProfileParams{
			AdminUserID: id,
			DisplayName: strings.TrimSpace(displayName),
			Locale:      normalizeLocale(locale),
			Timezone:    normalizeTimezone(timezone),
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		roles, txErr := loadRoles(ctx, queries, id)
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.account.profile_updated", Category: "configuration",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
		}); txErr != nil {
			return txErr
		}
		account = service.accountFrom(row, roles)
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// Preferences are the panel settings that follow an operator between browsers.
//
// The set is closed and every value is bounded, so a client cannot use this as
// arbitrary per-account storage or push a value the panel will choke on.
type Preferences struct {
	PageSize      int    `json:"pageSize,omitempty"`
	Density       string `json:"density,omitempty"`
	AuditSort     string `json:"auditSort,omitempty"`
	AuditCategory string `json:"auditCategory,omitempty"`
}

// SavePreferences merges a preference patch into the operator's stored set.
func (service *Service) SavePreferences(
	ctx context.Context, adminUserID string, patch Preferences,
) (Preferences, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Preferences{}, err
	}
	encoded, err := json.Marshal(sanitizePreferences(patch))
	if err != nil {
		return Preferences{}, err
	}
	row, err := dbgen.New(service.pool).UpdateAdminUserPreferences(ctx, dbgen.UpdateAdminUserPreferencesParams{
		AdminUserID: id, Preferences: encoded,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Preferences{}, ErrNotFound
	}
	if err != nil {
		return Preferences{}, err
	}
	return decodePreferences(row.Preferences), nil
}

// sanitizePreferences drops anything outside the supported range instead of
// storing it, so a bad value cannot be persisted and then break every later read.
func sanitizePreferences(patch Preferences) Preferences {
	clean := Preferences{}
	if patch.PageSize >= 10 && patch.PageSize <= 100 {
		clean.PageSize = patch.PageSize
	}
	if patch.Density == "compact" || patch.Density == "comfortable" {
		clean.Density = patch.Density
	}
	if patch.AuditSort == "asc" || patch.AuditSort == "desc" {
		clean.AuditSort = patch.AuditSort
	}
	if len(patch.AuditCategory) <= 32 {
		clean.AuditCategory = patch.AuditCategory
	}
	return clean
}

func decodePreferences(raw []byte) Preferences {
	preferences := Preferences{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &preferences)
	}
	return sanitizePreferences(preferences)
}

// VerifyPassword re-proves an operator's password without changing any state.
//
// It backs the step-up check in front of actions that weaken the account's own
// security, such as removing the second factor.
func (service *Service) VerifyPassword(ctx context.Context, adminUserID, password string) (bool, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return false, err
	}
	row, err := dbgen.New(service.pool).GetAdminUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if !row.PasswordHash.Valid {
		return false, nil
	}
	matched, err := adminauth.VerifyPassword(password, row.PasswordHash.String)
	if err != nil {
		// A malformed stored hash fails closed rather than authorizing.
		return false, nil
	}
	return matched, nil
}

// ChangePassword rotates an operator's own password after re-proving the
// current one, and ends every other session.
func (service *Service) ChangePassword(
	ctx context.Context, adminUserID, currentPassword, newPassword, keepSessionID string, request RequestContext,
) error {
	if err := adminauth.ValidatePassword(newPassword); err != nil {
		return err
	}
	id, err := parseUUID(adminUserID)
	if err != nil {
		return err
	}
	row, err := dbgen.New(service.pool).GetAdminUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !row.PasswordHash.Valid {
		return ErrInvalidCredentials
	}
	matched, err := adminauth.VerifyPassword(currentPassword, row.PasswordHash.String)
	if err != nil || !matched {
		return ErrInvalidCredentials
	}

	hash, err := adminauth.HashPassword(newPassword, service.password)
	if err != nil {
		return err
	}
	keep, keepErr := parseUUID(keepSessionID)
	keepSession := pgtype.UUID{}
	if keepErr == nil {
		keepSession = keep
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.SetAdminUserPassword(ctx, dbgen.SetAdminUserPasswordParams{
			AdminUserID: id, PasswordHash: optionalText(hash),
		}); txErr != nil {
			return txErr
		}
		// Any session established with the old password ends, except the one
		// making the change, so a compromised session does not survive the
		// remediation that was meant to end it.
		revoked, txErr := queries.RevokeAdminSessionsForUser(ctx, dbgen.RevokeAdminSessionsForUserParams{
			AdminUserID: id, RevokedReason: optionalText("password_change"), KeepSessionID: keepSession,
		})
		if txErr != nil {
			return txErr
		}
		if _, txErr = queries.InvalidateAdminPasswordResets(ctx, id); txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.password.changed", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"revokedSessions": revoked},
		}); txErr != nil {
			return txErr
		}
		// A password change the account holder did not make is the clearest
		// signal of a compromise, so it is announced rather than only logged.
		return notifySecurity(
			ctx, queries, "admin.password.changed", adminUserID,
			service.now().UTC().Format(time.RFC3339), "self",
		)
	})
}

// RequestPasswordReset creates a reset token when the address matches an
// account, and reports nothing either way.
//
// The caller must respond identically whether or not a token was produced: the
// returned token is empty for an unknown or inactive address, which is not an
// error condition.
func (service *Service) RequestPasswordReset(
	ctx context.Context, email string, request RequestContext,
) (token string, account Account, err error) {
	row, err := dbgen.New(service.pool).GetAdminUserByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", Account{}, nil
	}
	if err != nil {
		return "", Account{}, err
	}
	if row.Status != "active" {
		return "", Account{}, nil
	}

	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", Account{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))

	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// Outstanding tokens are retired first, so requesting a new link
		// invalidates one an attacker may have intercepted earlier.
		if _, txErr := queries.InvalidateAdminPasswordResets(ctx, row.ID); txErr != nil {
			return txErr
		}
		if _, txErr := queries.CreateAdminPasswordReset(ctx, dbgen.CreateAdminPasswordResetParams{
			AdminUserID: row.ID,
			TokenHash:   digest[:],
			RequestedIp: request.IP,
			Lifetime:    interval(PasswordResetTTL),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.password.reset_requested", Category: "authentication",
			TargetType: "admin_user", TargetID: uuidString(row.ID), RequestID: request.RequestID,
		})
	})
	if err != nil {
		return "", Account{}, err
	}
	return token, service.accountFrom(row, nil), nil
}

// CompletePasswordReset redeems a reset token and sets a new password.
func (service *Service) CompletePasswordReset(
	ctx context.Context, token, newPassword string, request RequestContext,
) error {
	if err := adminauth.ValidatePassword(newPassword); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(token))
	row, err := dbgen.New(service.pool).GetAdminPasswordReset(ctx, digest[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	if row.AdminUser.Status != "active" {
		return ErrAccountDisabled
	}

	hash, err := adminauth.HashPassword(newPassword, service.password)
	if err != nil {
		return err
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		// Consumption is guarded by `used_at IS NULL AND expires_at > now()`,
		// so a replayed or stale link updates no row and fails here.
		if _, txErr := queries.ConsumeAdminPasswordReset(ctx, row.AdminPasswordReset.ID); txErr != nil {
			if errors.Is(txErr, pgx.ErrNoRows) {
				return ErrInvalidCredentials
			}
			return txErr
		}
		if _, txErr := queries.SetAdminUserPassword(ctx, dbgen.SetAdminUserPasswordParams{
			AdminUserID: row.AdminUser.ID, PasswordHash: optionalText(hash),
		}); txErr != nil {
			return txErr
		}
		// Every session ends: a reset is the remediation for a suspected
		// compromise, so nothing established beforehand may survive it.
		revoked, txErr := queries.RevokeAdminSessionsForUser(ctx, dbgen.RevokeAdminSessionsForUserParams{
			AdminUserID: row.AdminUser.ID, RevokedReason: optionalText("password_change"),
		})
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.AdminUser.ID),
			Action: "admin.password.reset_completed", Category: "authentication",
			TargetType: "admin_user", TargetID: uuidString(row.AdminUser.ID),
			RequestID: request.RequestID,
			Metadata:  map[string]any{"revokedSessions": revoked},
		}); txErr != nil {
			return txErr
		}
		return notifySecurity(
			ctx, queries, "admin.password.reset_completed", uuidString(row.AdminUser.ID),
			uuidString(row.AdminPasswordReset.ID), "reset_token",
		)
	})
}

func loadRolesForUsers(
	ctx context.Context, queries *dbgen.Queries, ids []pgtype.UUID,
) (map[string][]rbac.Role, error) {
	grouped := make(map[string][]rbac.Role, len(ids))
	if len(ids) == 0 {
		return grouped, nil
	}
	rows, err := queries.ListAdminRolesForUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		role, parseErr := rbac.ParseRole(row.Role)
		if parseErr != nil {
			continue
		}
		key := uuidString(row.AdminUserID)
		grouped[key] = append(grouped[key], role)
	}
	return grouped, nil
}

func roleStrings(roles []rbac.Role) []string {
	values := make([]string, 0, len(roles))
	for _, role := range roles {
		values = append(values, string(role))
	}
	return values
}

// normalizeDisplayEmail keeps the operator's own casing for display while the
// normalized column carries the lookup key.
func normalizeDisplayEmail(email string) string { return strings.TrimSpace(email) }

func normalizeLocale(locale string) string {
	if locale == "ru" {
		return "ru"
	}
	return "en"
}

func normalizeTimezone(timezone string) string {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return "UTC"
	}
	// An unknown zone would be stored and then fail every time the panel tried
	// to render a timestamp with it, so it is rejected at the boundary.
	if _, err := time.LoadLocation(timezone); err != nil {
		return "UTC"
	}
	return timezone
}
