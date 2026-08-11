package adminauthpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
)

// SetupTokenTTL bounds how long an unused bootstrap token stays redeemable. A
// token that leaks into a log or a screenshot therefore stops being useful
// quickly even if the installation is never completed.
const SetupTokenTTL = 30 * time.Minute

// BootstrapState tells transport whether the first-owner flow is still open.
type BootstrapState struct {
	// Required is true when no operator account exists yet.
	Required bool
	// TokenIssued is true when a live, unconsumed setup token exists.
	TokenIssued bool
}

// BootstrapStatus reports whether an installation still needs its first owner.
func (service *Service) BootstrapStatus(ctx context.Context) (BootstrapState, error) {
	queries := dbgen.New(service.pool)
	count, err := queries.CountAdminUsers(ctx)
	if err != nil {
		return BootstrapState{}, err
	}
	return BootstrapState{Required: count == 0}, nil
}

// IssueSetupToken mints the one-time token that authorizes creating the first
// owner, returning the plaintext exactly once for the caller to print.
//
// It refuses once any operator account exists, so the bootstrap path closes
// permanently the moment the installation has an owner — a token that leaked
// before that cannot be redeemed afterwards.
func (service *Service) IssueSetupToken(ctx context.Context) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		count, txErr := queries.CountAdminUsers(ctx)
		if txErr != nil {
			return txErr
		}
		if count > 0 {
			return ErrBootstrapClosed
		}
		// Only the newest token may be redeemed, so re-running setup does not
		// leave a trail of usable tokens behind.
		if _, txErr = queries.ExpireAdminSetupTokens(ctx); txErr != nil {
			return txErr
		}
		_, txErr = queries.CreateAdminSetupToken(ctx, dbgen.CreateAdminSetupTokenParams{
			TokenHash: digest[:],
			Lifetime:  interval(SetupTokenTTL),
		})
		return txErr
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// CompleteBootstrap redeems a setup token and creates the first owner.
//
// The whole operation is one transaction guarded by a re-check that no operator
// exists, so two concurrent redemptions cannot both create an owner.
func (service *Service) CompleteBootstrap(
	ctx context.Context, setupToken, email, displayName, password, locale string, request RequestContext,
) (Account, error) {
	if err := adminauth.ValidatePassword(password); err != nil {
		return Account{}, err
	}
	normalized := NormalizeEmail(email)
	if normalized == "" {
		return Account{}, errors.New("an email address is required")
	}
	hash, err := adminauth.HashPassword(password, service.password)
	if err != nil {
		return Account{}, err
	}
	digest := sha256.Sum256([]byte(setupToken))

	var account Account
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		count, txErr := queries.CountAdminUsers(ctx)
		if txErr != nil {
			return txErr
		}
		if count > 0 {
			return ErrBootstrapClosed
		}

		stored, txErr := queries.GetAdminSetupToken(ctx, digest[:])
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrBootstrapClosed
		}
		if txErr != nil {
			return txErr
		}

		created, txErr := queries.CreateAdminUser(ctx, dbgen.CreateAdminUserParams{
			Email:             normalizeDisplayEmail(email),
			EmailNormalized:   normalized,
			DisplayName:       displayName,
			PasswordHash:      optionalText(hash),
			Locale:            normalizeLocale(locale),
			Timezone:          "UTC",
			PasswordChangedAt: timestamp(service.now()),
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if txErr != nil {
			return txErr
		}

		// Consuming the token last means a failure anywhere above rolls the
		// consumption back and leaves the token usable for a retry.
		if _, txErr = queries.ConsumeAdminSetupToken(ctx, dbgen.ConsumeAdminSetupTokenParams{
			SetupTokenID: stored.ID, ConsumedBy: created.ID,
		}); errors.Is(txErr, pgx.ErrNoRows) {
			// Already consumed, or past its expiry.
			return ErrBootstrapClosed
		} else if txErr != nil {
			return txErr
		}

		if txErr = queries.GrantAdminRole(ctx, dbgen.GrantAdminRoleParams{
			AdminUserID: created.ID, Role: string(rbac.RoleOwner),
		}); txErr != nil {
			return txErr
		}

		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(created.ID),
			Action: "admin.bootstrap.completed", Category: "authentication",
			TargetType: "admin_user", TargetID: uuidString(created.ID),
			Reason: "first owner created", RequestID: request.RequestID,
		}); txErr != nil {
			return txErr
		}

		account = service.accountFrom(created, []rbac.Role{rbac.RoleOwner})
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}
