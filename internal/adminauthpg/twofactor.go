package adminauthpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// totpAssociatedData binds a sealed TOTP secret to its purpose, so a ciphertext
// lifted from this column cannot be replayed into another encrypted field that
// uses the same key.
const totpAssociatedData = "admin.totp"

// Enrolment is the one-time material shown while setting up an authenticator.
type Enrolment struct {
	Secret string
	URI    string
}

// BeginTOTPEnrolment issues a new secret and the otpauth URI for it.
//
// The secret is stored immediately but left unconfirmed, so it satisfies no
// login challenge until ConfirmTOTPEnrolment proves the operator's
// authenticator actually holds it. An abandoned enrolment therefore leaves the
// account exactly as it was.
func (service *Service) BeginTOTPEnrolment(
	ctx context.Context, adminUserID, issuer string, request RequestContext,
) (Enrolment, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return Enrolment{}, err
	}
	secret, err := adminauth.GenerateTOTPSecret()
	if err != nil {
		return Enrolment{}, err
	}
	sealed, err := service.sealSecret(secret, totpAssociatedData)
	if err != nil {
		return Enrolment{}, err
	}

	var account dbgen.AdminUser
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.SetAdminTOTPSecret(ctx, dbgen.SetAdminTOTPSecretParams{
			AdminUserID: id, TotpSecretCiphertext: sealed,
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		account = row
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.totp.enrolment_started", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
		})
	})
	if err != nil {
		return Enrolment{}, err
	}
	return Enrolment{Secret: secret, URI: adminauth.TOTPURI(secret, issuer, account.Email)}, nil
}

// ConfirmTOTPEnrolment verifies the first code and activates the second factor,
// returning the recovery codes. The plaintext codes are returned exactly once.
func (service *Service) ConfirmTOTPEnrolment(
	ctx context.Context, adminUserID, code string, request RequestContext,
) ([]string, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return nil, err
	}
	row, err := dbgen.New(service.pool).GetAdminUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(row.TotpSecretCiphertext) == 0 {
		return nil, ErrNotFound
	}

	secret, err := service.openSecret(row.TotpSecretCiphertext, totpAssociatedData)
	if err != nil {
		return nil, err
	}
	valid, err := adminauth.VerifyTOTP(secret, code, service.now())
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, ErrInvalidCredentials
	}

	codes, err := adminauth.GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}

	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.ConfirmAdminTOTP(ctx, id); txErr != nil {
			if errors.Is(txErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return txErr
		}
		// Confirming always issues a fresh set, so codes printed during an
		// earlier enrolment stop working the moment a new one completes.
		if txErr := queries.DeleteAdminRecoveryCodes(ctx, id); txErr != nil {
			return txErr
		}
		for _, plaintext := range codes {
			if txErr := queries.InsertAdminRecoveryCode(ctx, dbgen.InsertAdminRecoveryCodeParams{
				AdminUserID: id, CodeHash: adminauth.HashRecoveryCode(plaintext),
			}); txErr != nil {
				return txErr
			}
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.totp.enabled", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"recoveryCodes": len(codes)},
		})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP removes the second factor and every recovery code.
//
// The caller must have re-proven the password immediately beforehand; that
// check belongs to transport, which holds the plaintext.
func (service *Service) DisableTOTP(ctx context.Context, adminUserID, actorID string, request RequestContext) error {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.DisableAdminTOTP(ctx, id); txErr != nil {
			if errors.Is(txErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return txErr
		}
		if txErr := queries.DeleteAdminRecoveryCodes(ctx, id); txErr != nil {
			return txErr
		}
		if txErr := appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actorID,
			Action: "admin.totp.disabled", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
		}); txErr != nil {
			return txErr
		}
		// Removing a second factor weakens the account, so it is announced for
		// the same reason a password change is.
		return notifySecurity(
			ctx, queries, "admin.totp.disabled", adminUserID,
			service.now().UTC().Format(time.RFC3339), "password",
		)
	})
}

// RegenerateRecoveryCodes replaces the whole set, invalidating any unused code.
func (service *Service) RegenerateRecoveryCodes(
	ctx context.Context, adminUserID string, request RequestContext,
) ([]string, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return nil, err
	}
	codes, err := adminauth.GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.DeleteAdminRecoveryCodes(ctx, id); txErr != nil {
			return txErr
		}
		for _, plaintext := range codes {
			if txErr := queries.InsertAdminRecoveryCode(ctx, dbgen.InsertAdminRecoveryCodeParams{
				AdminUserID: id, CodeHash: adminauth.HashRecoveryCode(plaintext),
			}); txErr != nil {
				return txErr
			}
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.recovery_codes.regenerated", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"recoveryCodes": len(codes)},
		})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// RemainingRecoveryCodes reports how many unused codes an operator holds, so
// the panel can warn before they run out.
func (service *Service) RemainingRecoveryCodes(ctx context.Context, adminUserID string) (int, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return 0, err
	}
	count, err := dbgen.New(service.pool).CountUnusedAdminRecoveryCodes(ctx, id)
	return int(count), err
}
