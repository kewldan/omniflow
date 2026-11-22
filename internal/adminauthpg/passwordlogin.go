package adminauthpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// passwordLoginClosed reports whether a correct password must still be
// refused: the owner turned password sign-in off, and no reopening from the
// command line is running.
func passwordLoginClosed(row dbgen.AdminUser, now time.Time) bool {
	if !row.PasswordLoginDisabledAt.Valid {
		return false
	}
	return !row.PasswordLoginAllowedUntil.Valid || !row.PasswordLoginAllowedUntil.Time.After(now)
}

// SetPasswordLogin turns password sign-in on or off for the operator's own
// account. Off needs a passkey of theirs that has already signed in: a key
// that was registered and never worked is exactly the one that locks its
// owner out.
func (service *Service) SetPasswordLogin(
	ctx context.Context, adminUserID string, enabled bool, request RequestContext,
) (Account, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Account{}, err
	}
	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if !enabled {
			used, countErr := queries.CountUsedAdminPasskeys(ctx, id)
			if countErr != nil {
				return countErr
			}
			if used == 0 {
				return ErrNoWorkingPasskey
			}
		}
		row, updateErr := queries.SetAdminPasswordLoginDisabled(ctx, dbgen.SetAdminPasswordLoginDisabledParams{
			Disabled: !enabled, AdminUserID: id,
		})
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if updateErr != nil {
			return updateErr
		}
		account, updateErr = service.accountFromRow(ctx, queries, row)
		if updateErr != nil {
			return updateErr
		}
		action := "admin.password_login.disabled"
		if enabled {
			action = "admin.password_login.enabled"
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: action, Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
		})
	})
	return account, err
}

// ReopenPasswordLogin is the server-side way back in for an operator whose
// passkey is gone: password sign-in works again for the given time, or for
// good when it is zero. It is reached only from the api's command line, by
// somebody who can already run commands on the server.
func (service *Service) ReopenPasswordLogin(
	ctx context.Context, email string, duration time.Duration,
) (Account, error) {
	var account Account
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.GetAdminUserByEmail(ctx, NormalizeEmail(email))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if duration <= 0 {
			row, err = queries.SetAdminPasswordLoginDisabled(ctx, dbgen.SetAdminPasswordLoginDisabledParams{
				Disabled: false, AdminUserID: row.ID,
			})
		} else {
			row, err = queries.AllowAdminPasswordLoginUntil(ctx, dbgen.AllowAdminPasswordLoginUntilParams{
				AllowedUntil: pgtype.Timestamptz{Time: service.now().Add(duration), Valid: true},
				AdminUserID:  row.ID,
			})
		}
		if err != nil {
			return err
		}
		if account, err = service.accountFromRow(ctx, queries, row); err != nil {
			return err
		}
		metadata := map[string]any{"permanent": duration <= 0}
		if duration > 0 {
			metadata["until"] = service.now().Add(duration).UTC().Format(time.RFC3339)
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "system", Action: "admin.password_login.reopened",
			Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID),
			Reason: "server command line", Metadata: metadata,
		})
	})
	return account, err
}

// UnlockAccount clears the failed-attempt counters and any lockout, from the
// server's command line.
func (service *Service) UnlockAccount(ctx context.Context, email string) (Account, error) {
	var account Account
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.GetAdminUserByEmail(ctx, NormalizeEmail(email))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row, err = queries.UnlockAdminUser(ctx, row.ID); err != nil {
			return err
		}
		if account, err = service.accountFromRow(ctx, queries, row); err != nil {
			return err
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "system", Action: "admin.account.unlocked",
			Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID), Reason: "server command line",
		})
	})
	return account, err
}

// RevokeAllSessions ends every session of an account, from the server's
// command line: a laptop left signed in, a session nobody recognises.
func (service *Service) RevokeAllSessions(ctx context.Context, email string) (int64, error) {
	var revoked int64
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.GetAdminUserByEmail(ctx, NormalizeEmail(email))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if revoked, err = queries.RevokeAdminSessionsForUser(ctx, dbgen.RevokeAdminSessionsForUserParams{
			AdminUserID: row.ID, RevokedReason: pgtype.Text{String: "server command line", Valid: true},
		}); err != nil {
			return err
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "system", Action: "admin.sessions.revoked",
			Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID), Reason: "server command line",
			Metadata: map[string]any{"revoked": revoked},
		})
	})
	return revoked, err
}

// accountFromRow is accountFrom with the roles loaded in the same transaction.
func (service *Service) accountFromRow(ctx context.Context, queries *dbgen.Queries, row dbgen.AdminUser) (Account, error) {
	roles, err := loadRoles(ctx, queries, row.ID)
	if err != nil {
		return Account{}, err
	}
	return service.accountFrom(row, roles), nil
}
