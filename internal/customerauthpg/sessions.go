package customerauthpg

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SessionView is one live session as the customer's own security screen shows
// it.
//
// It carries no token and no CSRF secret. The IP is included because an address
// the customer does not recognise is the entire reason this screen exists; the
// user agent is included for the same reason and is passed through as recorded
// rather than parsed into a guess about the device.
type SessionView struct {
	ID           string
	Current      bool
	AuthMethod   string
	AuthProvider string
	IP           string
	UserAgent    string
	CreatedAt    time.Time
	LastSeenAt   time.Time
	ExpiresAt    time.Time
}

// ListSessions returns the customer's live sessions, most recently used first.
func (service *Service) ListSessions(
	ctx context.Context, customerID, currentSessionID string, limit int32,
) ([]SessionView, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := dbgen.New(service.pool).ListCustomerSessions(ctx, dbgen.ListCustomerSessionsParams{
		UserID: userID, PageSize: limit,
	})
	if err != nil {
		return nil, err
	}

	views := make([]SessionView, 0, len(rows))
	for _, row := range rows {
		id := uuidString(row.ID)
		views = append(views, SessionView{
			ID:           id,
			Current:      id == currentSessionID,
			AuthMethod:   row.AuthMethod,
			AuthProvider: row.AuthProvider.String,
			IP:           addressString(row.Ip),
			UserAgent:    row.UserAgent.String,
			CreatedAt:    row.CreatedAt.Time.UTC(),
			LastSeenAt:   row.LastSeenAt.Time.UTC(),
			ExpiresAt:    row.AbsoluteExpiresAt.Time.UTC(),
		})
	}
	return views, nil
}

// RevokeSession ends one of the customer's own sessions.
//
// The query is scoped by customer as well as by session, so learning another
// customer's session identifier is not enough to end it.
func (service *Service) RevokeSession(
	ctx context.Context, customerID, sessionID string, request RequestContext,
) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	target, err := parseUUID(sessionID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		_, revokeErr := queries.RevokeCustomerSession(ctx, dbgen.RevokeCustomerSessionParams{
			SessionID: target, UserID: userID, RevokedReason: pgtype.Text{String: "session_revoked", Valid: true},
		})
		if errors.Is(revokeErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if revokeErr != nil {
			return revokeErr
		}
		return service.appendSecurityEvent(ctx, queries, customerID, "session_revoked", request, nil)
	})
}

// SupersedeSession ends the session a browser was already holding when it
// signed in again.
//
// Re-authentication exists so a stale session can perform a sensitive action
// after a fresh sign-in; it is not a request for a second session. Leaving the
// old one live would list two devices for one browser on the security screen
// and keep a token alive that the customer believes they have just replaced.
// No security event is written: the new sign-in already recorded itself, and
// the old session's end is the same act.
func (service *Service) SupersedeSession(ctx context.Context, customerID, sessionID string) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	target, err := parseUUID(sessionID)
	if err != nil {
		return err
	}
	_, err = dbgen.New(service.pool).RevokeCustomerSession(ctx, dbgen.RevokeCustomerSessionParams{
		SessionID: target, UserID: userID, RevokedReason: pgtype.Text{String: "reauthenticated", Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// SignOut ends the calling session only.
func (service *Service) SignOut(
	ctx context.Context, customerID, sessionID string, request RequestContext,
) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	target, err := parseUUID(sessionID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, revokeErr := queries.RevokeCustomerSession(ctx, dbgen.RevokeCustomerSessionParams{
			SessionID: target, UserID: userID, RevokedReason: pgtype.Text{String: "logout", Valid: true},
		}); revokeErr != nil && !errors.Is(revokeErr, pgx.ErrNoRows) {
			return revokeErr
		}
		return service.appendSecurityEvent(ctx, queries, customerID, "signed_out", request, nil)
	})
}

// SignOutEverywhere ends every session the customer has.
//
// `keepCurrent` decides whether the caller's own session survives. Both
// behaviours are wanted: "sign out my other devices" leaves the customer where
// they are, while "sign out everywhere" after a suspected compromise must not
// leave the session that asked for it alive.
func (service *Service) SignOutEverywhere(
	ctx context.Context, customerID, sessionID string, keepCurrent bool, request RequestContext,
) (int, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return 0, err
	}
	except := pgtype.UUID{}
	if keepCurrent {
		if except, err = parseUUID(sessionID); err != nil {
			return 0, err
		}
	}

	revoked := 0
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		ids, revokeErr := queries.RevokeCustomerSessionsForUser(ctx, dbgen.RevokeCustomerSessionsForUserParams{
			UserID:          userID,
			RevokedReason:   pgtype.Text{String: "logout_all", Valid: true},
			ExceptSessionID: except,
		})
		if revokeErr != nil {
			return revokeErr
		}
		revoked = len(ids)
		return service.appendSecurityEvent(ctx, queries, customerID, "signed_out_all", request,
			map[string]any{"revoked": revoked})
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

// RevokeSessionsForProvider ends every session established through one OIDC
// provider.
//
// An operator disabling or deleting a provider expects its access to stop. If
// the sessions it minted stayed live, the provider would be off while the doors
// it opened remained open for up to the absolute session lifetime.
func (service *Service) RevokeSessionsForProvider(ctx context.Context, slug string) (int, error) {
	ids, err := dbgen.New(service.pool).RevokeCustomerSessionsForProvider(ctx, optionalText(slug))
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// PurgeExpired removes session, magic-link, and security-event rows that are
// past their retention window. It is called by the existing cleanup job.
func (service *Service) PurgeExpired(ctx context.Context) (sessions, links, events int64, err error) {
	queries := dbgen.New(service.pool)
	if sessions, err = queries.DeleteExpiredCustomerSessions(ctx, interval(SessionRetention)); err != nil {
		return 0, 0, 0, err
	}
	if links, err = queries.DeleteExpiredCustomerMagicLinks(
		ctx, interval(customerauth.MagicLinkRetention),
	); err != nil {
		return sessions, 0, 0, err
	}
	if events, err = queries.DeleteExpiredCustomerSecurityEvents(
		ctx, interval(SecurityEventRetention),
	); err != nil {
		return sessions, links, 0, err
	}
	return sessions, links, events, nil
}

// addressString renders a stored address for display, or the empty string when
// none was recorded.
func addressString(address *netip.Addr) string {
	if address == nil || !address.IsValid() {
		return ""
	}
	return address.String()
}
