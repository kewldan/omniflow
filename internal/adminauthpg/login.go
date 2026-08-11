package adminauthpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
)

// LoginResult is the outcome of a successful password step.
type LoginResult struct {
	// Token is the session cookie value. It is returned exactly once.
	Token string
	// ChallengeRequired is true when the account has TOTP enrolled, in which
	// case the session authorizes nothing until VerifyChallenge succeeds.
	ChallengeRequired bool
	CSRFToken         string
	ExpiresAt         time.Time
	Account           Account
}

// Authenticate performs the password factor.
//
// Every failure path returns ErrInvalidCredentials, ErrAccountLocked, or
// ErrAccountDisabled with no further detail, and an unknown address is verified
// against a decoy hash so it costs the same as a real one. The endpoint
// therefore leaks neither which addresses are registered nor which of them are
// locked.
func (service *Service) Authenticate(
	ctx context.Context, email, password string, request RequestContext,
) (LoginResult, error) {
	now := service.now()
	normalized := NormalizeEmail(email)

	row, err := dbgen.New(service.pool).GetAdminUserByEmail(ctx, normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		// Spend the same work a real verification would, then fail.
		_, _ = adminauth.VerifyPassword(password, service.decoyHash)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}

	// A locked account is not verified against at all, so repeated guessing
	// during a lockout cannot be used to time the password check.
	if lockedUntil := timePointer(row.LockedUntil); adminauth.Locked(lockedUntil, now) {
		_ = service.auditLoginFailure(ctx, row, "account_locked", request)
		return LoginResult{}, ErrAccountLocked
	}
	if row.Status != "active" {
		_ = service.auditLoginFailure(ctx, row, "account_inactive", request)
		return LoginResult{}, ErrAccountDisabled
	}
	if !row.PasswordHash.Valid {
		// An OIDC-only operator has no password to verify. Costing the same as
		// a real check keeps that fact from being measurable.
		_, _ = adminauth.VerifyPassword(password, service.decoyHash)
		_ = service.auditLoginFailure(ctx, row, "password_not_set", request)
		return LoginResult{}, ErrInvalidCredentials
	}

	matched, verifyErr := adminauth.VerifyPassword(password, row.PasswordHash.String)
	if verifyErr != nil || !matched {
		if failErr := service.registerFailure(ctx, row, now, request); failErr != nil {
			return LoginResult{}, failErr
		}
		return LoginResult{}, ErrInvalidCredentials
	}

	return service.startSession(ctx, row, password, now, request)
}

// registerFailure increments the failure counter and applies the backoff.
func (service *Service) registerFailure(
	ctx context.Context, row dbgen.AdminUser, now time.Time, request RequestContext,
) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The stored counter is the source of truth, so two concurrent failed
		// attempts both count rather than racing to the same value.
		lockedUntil := service.lockout.LockoutUntil(int(row.FailedLoginCount)+1, now)
		updated, err := queries.RecordAdminLoginFailure(ctx, dbgen.RecordAdminLoginFailureParams{
			AdminUserID: row.ID,
			LockedUntil: optionalTimestamp(lockedUntil),
		})
		if err != nil {
			return err
		}
		metadata := map[string]any{"failedAttempts": updated.FailedLoginCount}
		if lockedUntil != nil {
			metadata["lockedUntil"] = lockedUntil.UTC().Format(time.RFC3339)
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.login", Category: "authentication", Outcome: "failure",
			TargetType: "admin_user", TargetID: uuidString(row.ID),
			Reason: "invalid_password", RequestID: request.RequestID,
			Metadata: metadata,
		})
	})
}

// auditLoginFailure records a denial that does not change credential state.
func (service *Service) auditLoginFailure(
	ctx context.Context, row dbgen.AdminUser, reason string, request RequestContext,
) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.login", Category: "authentication", Outcome: "denied",
			TargetType: "admin_user", TargetID: uuidString(row.ID),
			Reason: reason, RequestID: request.RequestID,
		})
	})
}

// startSession clears the failure state and issues a session. The verified
// plaintext is passed in solely so a hash below current cost can be upgraded
// here, which is the only moment it is available.
func (service *Service) startSession(
	ctx context.Context, row dbgen.AdminUser, verifiedPassword string, now time.Time, request RequestContext,
) (LoginResult, error) {
	token, digest, err := adminauth.NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrfSecret, err := adminauth.NewCSRFSecret()
	if err != nil {
		return LoginResult{}, err
	}

	challengeRequired := row.TotpConfirmedAt.Valid
	idle, absolute := service.sessions.SessionDeadlines(now, challengeRequired)

	methods := []string{"password"}
	var result LoginResult
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// Re-hashing on sign-in is the only moment the plaintext is available,
		// so a cost increase rolls out as operators sign in rather than
		// requiring a reset.
		if adminauth.NeedsRehash(row.PasswordHash.String, service.password) {
			rehashed, hashErr := adminauth.HashPassword(verifiedPassword, service.password)
			// A rehash failure must never block an otherwise valid sign-in: the
			// operator proved the password, and the stored hash still verifies.
			if hashErr == nil {
				if _, updateErr := queries.SetAdminUserPassword(ctx, dbgen.SetAdminUserPasswordParams{
					AdminUserID: row.ID, PasswordHash: optionalText(rehashed),
				}); updateErr != nil {
					return updateErr
				}
			}
		}

		fresh, err := queries.RecordAdminLoginSuccess(ctx, row.ID)
		if err != nil {
			return err
		}

		session, err := queries.CreateAdminSession(ctx, dbgen.CreateAdminSessionParams{
			AdminUserID:       row.ID,
			TokenHash:         digest,
			CsrfSecret:        csrfSecret,
			PendingTotp:       challengeRequired,
			AuthMethods:       methods,
			Ip:                request.IP,
			UserAgent:         optionalText(truncate(request.UserAgent, 400)),
			IdleExpiresAt:     timestamp(idle),
			AbsoluteExpiresAt: timestamp(absolute),
		})
		if err != nil {
			return err
		}

		roles, err := loadRoles(ctx, queries, row.ID)
		if err != nil {
			return err
		}

		outcome := "success"
		if challengeRequired {
			// The sign-in is not complete until the second factor is proven, so
			// the trail must not yet claim it was.
			outcome = "pending"
		}
		if err = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.login", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID),
			RequestID: request.RequestID,
			Metadata: map[string]any{
				"sessionId":         uuidString(session.ID),
				"challengeRequired": challengeRequired,
				"step":              outcome,
			},
		}); err != nil {
			return err
		}

		// A notice for every sign-in would be ignored within a week. One for a
		// sign-in from an address this account has not used before is the
		// signal an operator actually wants.
		if request.IP != nil {
			seen, countErr := queries.CountAdminSessionsFromAddress(ctx, dbgen.CountAdminSessionsFromAddressParams{
				AdminUserID: row.ID, Ip: request.IP, ExcludingSessionID: session.ID,
			})
			if countErr != nil {
				return countErr
			}
			if seen == 0 {
				if err = notifySecurity(
					ctx, queries, "admin.login.new_location",
					uuidString(row.ID), uuidString(session.ID), "password",
				); err != nil {
					return err
				}
			}
		}

		result = LoginResult{
			Token:             token,
			ChallengeRequired: challengeRequired,
			CSRFToken:         adminauth.CSRFToken(csrfSecret),
			ExpiresAt:         idle,
			Account:           service.accountFrom(fresh, roles),
		}
		return nil
	})
	if err != nil {
		return LoginResult{}, err
	}
	return result, nil
}

// VerifyChallenge completes the second factor for a pending session using a
// TOTP code or a single-use recovery code.
//
// The session token is rotated on success, so the cookie that existed during
// the challenge cannot be replayed as a fully authenticated one.
func (service *Service) VerifyChallenge(
	ctx context.Context, sessionToken, code string, request RequestContext,
) (LoginResult, error) {
	now := service.now()
	row, err := dbgen.New(service.pool).GetAdminSessionByToken(ctx, adminauth.HashSessionToken(sessionToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, ErrSessionInvalid
	}
	if err != nil {
		return LoginResult{}, err
	}
	if !row.AdminSession.PendingTotp {
		return LoginResult{}, ErrSessionInvalid
	}
	if err = service.sessions.Validate(sessionState(row.AdminSession), now); err != nil {
		return LoginResult{}, ErrSessionInvalid
	}
	if row.AdminUser.Status != "active" {
		return LoginResult{}, ErrAccountDisabled
	}

	method, err := service.proveSecondFactor(ctx, row.AdminUser, code, now)
	if err != nil {
		if !errors.Is(err, ErrInvalidCredentials) {
			return LoginResult{}, err
		}
		_ = service.inTx(ctx, func(queries *dbgen.Queries) error {
			return appendAudit(ctx, queries, AuditEntry{
				ActorType: "admin", ActorID: uuidString(row.AdminUser.ID),
				Action: "admin.login.challenge", Category: "authentication", Outcome: "failure",
				TargetType: "admin_user", TargetID: uuidString(row.AdminUser.ID),
				Reason: "invalid_second_factor", RequestID: request.RequestID,
			})
		})
		return LoginResult{}, ErrInvalidCredentials
	}

	token, digest, err := adminauth.NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	idle := service.sessions.NextIdleDeadline(now, row.AdminSession.AbsoluteExpiresAt.Time)

	var result LoginResult
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		session, err := queries.CompleteAdminSessionChallenge(ctx, dbgen.CompleteAdminSessionChallengeParams{
			SessionID:     row.AdminSession.ID,
			AuthMethods:   []string{"password", method},
			TokenHash:     digest,
			IdleExpiresAt: timestamp(idle),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionInvalid
		}
		if err != nil {
			return err
		}
		roles, err := loadRoles(ctx, queries, row.AdminUser.ID)
		if err != nil {
			return err
		}
		if err = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.AdminUser.ID),
			Action: "admin.login.challenge", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.AdminUser.ID),
			RequestID: request.RequestID,
			Metadata:  map[string]any{"sessionId": uuidString(session.ID), "method": method},
		}); err != nil {
			return err
		}
		result = LoginResult{
			Token:     token,
			CSRFToken: adminauth.CSRFToken(session.CsrfSecret),
			ExpiresAt: idle,
			Account:   service.accountFrom(row.AdminUser, roles),
		}
		return nil
	})
	if err != nil {
		return LoginResult{}, err
	}
	return result, nil
}

// proveSecondFactor accepts either a TOTP code or a recovery code and reports
// which method succeeded.
func (service *Service) proveSecondFactor(
	ctx context.Context, row dbgen.AdminUser, code string, now time.Time,
) (string, error) {
	if len(row.TotpSecretCiphertext) > 0 {
		secret, err := service.openSecret(row.TotpSecretCiphertext, totpAssociatedData)
		if err != nil {
			return "", err
		}
		valid, err := adminauth.VerifyTOTP(secret, code, now)
		if err == nil && valid {
			return "totp", nil
		}
	}

	// A recovery code is consumed atomically; the `used_at IS NULL` predicate
	// in the query means a replay updates no row.
	consumed := false
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		_, err := queries.ConsumeAdminRecoveryCode(ctx, dbgen.ConsumeAdminRecoveryCodeParams{
			AdminUserID: row.ID,
			CodeHash:    adminauth.HashRecoveryCode(code),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		consumed = true
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.recovery_code.used", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID),
			Reason: "second factor satisfied with a recovery code",
		})
	})
	if err != nil {
		return "", err
	}
	if consumed {
		return "recovery_code", nil
	}
	return "", ErrInvalidCredentials
}

// Resolve turns a session token into a request principal, sliding the idle
// window and rotating the token when it is due.
func (service *Service) Resolve(ctx context.Context, sessionToken string) (Principal, error) {
	now := service.now()
	row, err := dbgen.New(service.pool).GetAdminSessionByToken(ctx, adminauth.HashSessionToken(sessionToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrSessionInvalid
	}
	if err != nil {
		return Principal{}, err
	}
	if err = service.sessions.Validate(sessionState(row.AdminSession), now); err != nil {
		return Principal{}, ErrSessionInvalid
	}
	if row.AdminUser.Status != "active" {
		// A suspended operator's live sessions stop working immediately rather
		// than lingering until they expire.
		return Principal{}, ErrAccountDisabled
	}
	if row.AdminSession.PendingTotp {
		return Principal{}, ErrChallengeRequired
	}

	idle := service.sessions.NextIdleDeadline(now, row.AdminSession.AbsoluteExpiresAt.Time)
	rotate := service.sessions.ShouldRotate(sessionState(row.AdminSession), now)

	var rotatedToken string
	var digest []byte
	if rotate {
		if rotatedToken, digest, err = adminauth.NewSessionToken(); err != nil {
			return Principal{}, err
		}
	}

	var principal Principal
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		var session dbgen.AdminSession
		var txErr error
		if rotate {
			session, txErr = queries.RotateAdminSessionToken(ctx, dbgen.RotateAdminSessionTokenParams{
				SessionID: row.AdminSession.ID, TokenHash: digest, IdleExpiresAt: timestamp(idle),
			})
		} else {
			session, txErr = queries.TouchAdminSession(ctx, dbgen.TouchAdminSessionParams{
				SessionID: row.AdminSession.ID, IdleExpiresAt: timestamp(idle),
			})
		}
		if errors.Is(txErr, pgx.ErrNoRows) {
			// The session was revoked between the read and this write.
			return ErrSessionInvalid
		}
		if txErr != nil {
			return txErr
		}

		roles, txErr := loadRoles(ctx, queries, row.AdminUser.ID)
		if txErr != nil {
			return txErr
		}
		principal = Principal{
			Account:      service.accountFrom(row.AdminUser, roles),
			Grant:        rbac.NewGrant(roles...),
			SessionID:    uuidString(session.ID),
			CSRFToken:    adminauth.CSRFToken(session.CsrfSecret),
			RotatedToken: rotatedToken,
			ExpiresAt:    idle,
		}
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func sessionState(session dbgen.AdminSession) adminauth.SessionState {
	return adminauth.SessionState{
		PendingChallenge: session.PendingTotp,
		RotatedAt:        session.RotatedAt.Time,
		IdleExpiresAt:    session.IdleExpiresAt.Time,
		AbsoluteExpires:  session.AbsoluteExpiresAt.Time,
		RevokedAt:        timePointer(session.RevokedAt),
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
