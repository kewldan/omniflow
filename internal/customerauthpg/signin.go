package customerauthpg

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SignInWithTelegram establishes a session from a Login Widget callback.
//
// The widget is the primary route because it needs nothing the customer does not
// already have: whoever controls the Telegram account the bot has been talking
// to signs in, and lands on that same account rather than a second one.
func (service *Service) SignInWithTelegram(
	ctx context.Context, values url.Values, request RequestContext,
) (SignInResult, error) {
	if service.botToken == "" {
		return SignInResult{}, ErrTelegramUnset
	}
	identity, err := customerauth.VerifyLoginWidget(
		values, service.botToken, service.now(), customerauth.TelegramMaxAge,
	)
	if err != nil {
		return SignInResult{}, ErrSignInRejected
	}
	return service.signInWithTelegramIdentity(ctx, identity, request)
}

// SignInWithMiniApp establishes a session from the `initData` a Telegram Mini
// App hands to its own page.
//
// It exists because the customer panel is opened both ways: as an ordinary web
// page, where the widget applies, and inside Telegram, where there is no widget
// but the surrounding client has already signed the customer's identity.
func (service *Service) SignInWithMiniApp(
	ctx context.Context, initData string, request RequestContext,
) (SignInResult, error) {
	if service.botToken == "" {
		return SignInResult{}, ErrTelegramUnset
	}
	identity, err := customerauth.VerifyMiniAppInitData(
		initData, service.botToken, service.now(), customerauth.TelegramMaxAge,
	)
	if err != nil {
		return SignInResult{}, ErrSignInRejected
	}
	return service.signInWithTelegramIdentity(ctx, identity, request)
}

// signInWithTelegramIdentity resolves a verified Telegram identity to a customer
// and opens a session for them.
//
// A subject with no identity row is provisioned rather than refused. That is not
// a relaxation of the OIDC rule below it: the Telegram subject is not an address
// somebody else can assert, so there is no account here to take over — the
// customer either controls that Telegram account or they do not.
func (service *Service) signInWithTelegramIdentity(
	ctx context.Context, identity customerauth.TelegramIdentity, request RequestContext,
) (SignInResult, error) {
	var result SignInResult
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		existing, err := queries.GetActiveIdentityBySubject(ctx, dbgen.GetActiveIdentityBySubjectParams{
			Provider: customerauth.ProviderTelegram, ProviderSubject: identity.Subject(),
		})
		switch {
		case err == nil:
			if existing.UserStatus != "active" {
				return ErrAccountInactive
			}
			result.Customer = Customer{
				ID:       uuidString(existing.UserID),
				Status:   existing.UserStatus,
				Locale:   existing.UserLocale,
				Timezone: existing.UserTimezone,
			}
		case errors.Is(err, pgx.ErrNoRows):
			locale := "en"
			if identity.LanguageCode == "ru" {
				locale = "ru"
			}
			created, createErr := queries.CreateCustomerForSignIn(ctx, dbgen.CreateCustomerForSignInParams{
				Locale: locale, Timezone: "UTC",
			})
			if createErr != nil {
				return createErr
			}
			if _, createErr = queries.LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{
				UserID:          created.ID,
				Provider:        customerauth.ProviderTelegram,
				ProviderSubject: identity.Subject(),
				VerifiedAt:      pgtype.Timestamptz{Time: service.now(), Valid: true},
				Metadata:        []byte("{}"),
			}); createErr != nil {
				return createErr
			}
			result.Customer = customerFromUser(created)
		default:
			return err
		}

		session, sessionErr := service.openSession(
			ctx, queries, result.Customer.ID, "telegram", "", request,
		)
		if sessionErr != nil {
			return sessionErr
		}
		result.Token, result.ExpiresAt, result.SessionID = session.token, session.expiresAt, session.id
		return service.appendSecurityEvent(ctx, queries, result.Customer.ID, "signed_in", request,
			map[string]any{"method": "telegram"})
	})
	if err != nil {
		return SignInResult{}, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Magic links
// ---------------------------------------------------------------------------

// IssueMagicLink mints a one-time sign-in URL for a customer the caller has
// already identified.
//
// It is called by the bot, not by the web sign-in form, and that is the whole
// design. A form would have to take some identifier — a username, an address —
// and every answer it could give would tell a stranger whether that identifier
// belongs to a customer here. Starting the flow from inside a chat the customer
// has already authenticated to removes the question entirely, and means the link
// can only ever be delivered to somebody who already controls the account.
func (service *Service) IssueMagicLink(
	ctx context.Context, customerID string, request RequestContext,
) (string, error) {
	if !service.magicLinkEnabled {
		return "", customerauth.ErrMagicLinkUnavailable
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return "", err
	}

	queries := dbgen.New(service.pool)
	recent, err := queries.CountRecentCustomerMagicLinks(ctx, dbgen.CountRecentCustomerMagicLinksParams{
		UserID: userID, Lookback: interval(customerauth.MagicLinkRequestWindow),
	})
	if err != nil {
		return "", err
	}
	if recent >= customerauth.MagicLinkRequestLimit {
		return "", customerauth.ErrMagicLinkThrottled
	}

	token, digest, err := customerauth.NewMagicLinkToken()
	if err != nil {
		return "", err
	}
	err = service.inTx(ctx, func(tx *dbgen.Queries) error {
		if _, createErr := tx.CreateCustomerMagicLink(ctx, dbgen.CreateCustomerMagicLinkParams{
			UserID: userID, TokenHash: digest, RequestedIp: request.IP,
			Lifetime: interval(customerauth.MagicLinkLifetime),
		}); createErr != nil {
			return createErr
		}
		return service.appendSecurityEvent(ctx, tx, customerID, "magic_link_requested", request, nil)
	})
	if err != nil {
		return "", err
	}
	// The redemption route, not a page that would have to forward to it.
	//
	// This is the address of the handler that consumes the token and sets the
	// cookie. Pointing at a page instead costs a redirect and, until the page
	// exists, hands the customer a 404 after the one-time credential has already
	// been spent — the failure lands after delivery, where there is no way back.
	return service.publicURL + "/v1/account/auth/link?token=" + url.QueryEscape(token), nil
}

// CompleteMagicLink redeems a delivered link.
//
// Consumption and session creation share one transaction, so a link can never be
// marked spent without producing the session it was spent on.
func (service *Service) CompleteMagicLink(
	ctx context.Context, token string, request RequestContext,
) (SignInResult, error) {
	if !service.magicLinkEnabled {
		return SignInResult{}, customerauth.ErrMagicLinkUnavailable
	}
	var result SignInResult
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		link, consumeErr := queries.ConsumeCustomerMagicLink(ctx, dbgen.ConsumeCustomerMagicLinkParams{
			TokenHash: customerauth.HashMagicLinkToken(token), ConsumedIp: request.IP,
		})
		if errors.Is(consumeErr, pgx.ErrNoRows) {
			return customerauth.ErrMagicLinkInvalid
		}
		if consumeErr != nil {
			return consumeErr
		}

		user, userErr := queries.GetCustomer(ctx, link.UserID)
		if userErr != nil {
			return userErr
		}
		if user.Status != "active" {
			return ErrAccountInactive
		}
		result.Customer = customerFromUser(user)

		session, sessionErr := service.openSession(
			ctx, queries, result.Customer.ID, "magic_link", "", request,
		)
		if sessionErr != nil {
			return sessionErr
		}
		result.Token, result.ExpiresAt, result.SessionID = session.token, session.expiresAt, session.id
		return service.appendSecurityEvent(ctx, queries, result.Customer.ID, "signed_in", request,
			map[string]any{"method": "magic_link"})
	})
	if err != nil {
		return SignInResult{}, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

type openedSession struct {
	id        string
	token     string
	expiresAt time.Time
}

// openSession creates the session row inside the caller's transaction.
//
// Only the digest of the token is stored; the token itself is returned once, to
// be put in a cookie and never read back from the database.
func (service *Service) openSession(
	ctx context.Context, queries *dbgen.Queries,
	customerID, method, provider string, request RequestContext,
) (openedSession, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return openedSession{}, err
	}
	token, digest, err := customerauth.NewSessionToken()
	if err != nil {
		return openedSession{}, err
	}
	csrfSecret, err := customerauth.NewCSRFSecret()
	if err != nil {
		return openedSession{}, err
	}

	row, err := queries.CreateCustomerSession(ctx, dbgen.CreateCustomerSessionParams{
		UserID: userID, TokenHash: digest, CsrfSecret: csrfSecret,
		AuthMethod: method, AuthProvider: optionalText(provider),
		Ip: request.IP, UserAgent: optionalText(request.UserAgent),
		IdleWindow:     interval(service.sessions.IdleTimeout),
		AbsoluteWindow: interval(service.sessions.AbsoluteTimeout),
	})
	if err != nil {
		return openedSession{}, err
	}
	return openedSession{
		id: uuidString(row.ID), token: token, expiresAt: row.AbsoluteExpiresAt.Time.UTC(),
	}, nil
}

// Resolve turns a session cookie into a request principal.
//
// It also performs the two upkeep steps that must not be scattered across
// handlers: sliding the inactivity deadline forward, and rotating the token once
// it is due. A rotated token is returned so transport can reissue the cookie.
//
// A digest the table does not hold is not yet a dead session: it may have
// rotated away a moment ago under a concurrent request from the same browser,
// and the grace path in rotation.go checks for that before refusing.
func (service *Service) Resolve(ctx context.Context, sessionToken string) (Principal, error) {
	queries := dbgen.New(service.pool)
	digest := customerauth.HashSessionToken(sessionToken)
	row, err := queries.GetCustomerSessionByToken(ctx, digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.resolveSupersededToken(ctx, queries, digest)
	}
	if err != nil {
		return Principal{}, err
	}

	principal, err := service.principalFromSession(sessionRow(row))
	if err != nil {
		return Principal{}, err
	}

	state := sessionState(sessionRow(row))
	if service.sessions.ShouldRotate(state, service.now()) {
		rotated, rotateErr := service.rotateSessionToken(ctx, queries, row, digest)
		if rotateErr != nil {
			return Principal{}, rotateErr
		}
		if rotated != "" {
			principal.RotatedToken = rotated
			return principal, nil
		}
		// Another request is rotating, or the grace store is unavailable. The
		// session is valid on the token it came with; only the swap waits.
	}

	// Sliding the deadline is best-effort: a failure here must not turn a valid
	// request into an authentication error, and the next request retries it.
	_, _ = queries.TouchCustomerSession(ctx, dbgen.TouchCustomerSessionParams{
		SessionID: row.ID, IdleWindow: interval(service.sessions.IdleTimeout),
	})
	return principal, nil
}

// sessionRow is the shape both session lookups return; the two generated row
// types are field-for-field identical, so either converts directly.
type sessionRow dbgen.GetCustomerSessionByIDRow

func sessionState(row sessionRow) customerauth.SessionState {
	return customerauth.SessionState{
		CreatedAt:       row.CreatedAt.Time.UTC(),
		RotatedAt:       row.RotatedAt.Time.UTC(),
		IdleExpiresAt:   row.IdleExpiresAt.Time.UTC(),
		AbsoluteExpires: row.AbsoluteExpiresAt.Time.UTC(),
		RevokedAt:       timePointer(row.RevokedAt),
	}
}

// principalFromSession judges a session row against the policy and the
// account behind it, and builds the principal for a request that passes.
func (service *Service) principalFromSession(row sessionRow) (Principal, error) {
	now := service.now()
	state := sessionState(row)
	if err := service.sessions.Validate(state, now); err != nil {
		return Principal{}, ErrSessionInvalid
	}
	// A suspended or deleted customer holding a live cookie is refused here
	// rather than at each handler, so no surface can forget the check.
	if row.UserStatus != "active" {
		return Principal{}, ErrAccountInactive
	}
	return Principal{
		Customer: Customer{
			ID: uuidString(row.UserID), Status: row.UserStatus,
			Locale: row.UserLocale, Timezone: row.UserTimezone,
		},
		SessionID:                uuidString(row.ID),
		AuthMethod:               row.AuthMethod,
		AuthProvider:             row.AuthProvider.String,
		CSRFToken:                customerauth.CSRFToken(row.CsrfSecret),
		ExpiresAt:                state.AbsoluteExpires,
		ReauthenticationRequired: service.sessions.RequiresReauthentication(state, now),
	}, nil
}
