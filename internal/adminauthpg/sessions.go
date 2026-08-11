package adminauthpg

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SessionSummary is one live session as the device list presents it.
type SessionSummary struct {
	ID         string
	Current    bool
	IP         *netip.Addr
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	Methods    []string
}

// ListSessions returns an operator's live sessions, newest activity first.
func (service *Service) ListSessions(
	ctx context.Context, adminUserID, currentSessionID string,
) ([]SessionSummary, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(service.pool).ListAdminSessions(ctx, dbgen.ListAdminSessionsParams{
		AdminUserID: id, PageSize: 50,
	})
	if err != nil {
		return nil, err
	}
	summaries := make([]SessionSummary, 0, len(rows))
	for _, row := range rows {
		summaries = append(summaries, SessionSummary{
			ID:         uuidString(row.ID),
			Current:    uuidString(row.ID) == currentSessionID,
			IP:         row.Ip,
			UserAgent:  row.UserAgent.String,
			CreatedAt:  row.CreatedAt.Time,
			LastSeenAt: row.LastSeenAt.Time,
			ExpiresAt:  row.IdleExpiresAt.Time,
			Methods:    row.AuthMethods,
		})
	}
	return summaries, nil
}

// Logout ends the session that made the request.
func (service *Service) Logout(ctx context.Context, sessionToken string, request RequestContext) error {
	row, err := dbgen.New(service.pool).GetAdminSessionByToken(ctx, adminauth.HashSessionToken(sessionToken))
	if errors.Is(err, pgx.ErrNoRows) {
		// Logging out of an unknown session is not an error: the caller's
		// intent, that the session no longer works, already holds.
		return nil
	}
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.RevokeAdminSession(ctx, dbgen.RevokeAdminSessionParams{
			SessionID: row.AdminSession.ID, RevokedReason: optionalText("logout"),
		}); txErr != nil && !errors.Is(txErr, pgx.ErrNoRows) {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.AdminUser.ID),
			Action: "admin.logout", Category: "authentication",
			TargetType: "admin_user", TargetID: uuidString(row.AdminUser.ID),
			RequestID: request.RequestID,
			Metadata:  map[string]any{"sessionId": uuidString(row.AdminSession.ID)},
		})
	})
}

// RevokeSession ends one named session belonging to the operator.
//
// The session must belong to the caller: without that check, any operator
// could end any other operator's session by guessing an identifier.
func (service *Service) RevokeSession(
	ctx context.Context, adminUserID, sessionID string, request RequestContext,
) error {
	owner, err := parseUUID(adminUserID)
	if err != nil {
		return err
	}
	target, err := parseUUID(sessionID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		sessions, txErr := queries.ListAdminSessions(ctx, dbgen.ListAdminSessionsParams{
			AdminUserID: owner, PageSize: 200,
		})
		if txErr != nil {
			return txErr
		}
		owned := false
		for _, session := range sessions {
			if session.ID == target {
				owned = true
				break
			}
		}
		if !owned {
			return ErrNotFound
		}
		if _, txErr = queries.RevokeAdminSession(ctx, dbgen.RevokeAdminSessionParams{
			SessionID: target, RevokedReason: optionalText("logout"),
		}); txErr != nil && !errors.Is(txErr, pgx.ErrNoRows) {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.session.revoked", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"sessionId": sessionID},
		})
	})
}

// LogoutAll ends every session for an operator, optionally sparing the current
// one so the operator is not signed out of the browser they are using.
func (service *Service) LogoutAll(
	ctx context.Context, adminUserID, keepSessionID string, request RequestContext,
) (int64, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return 0, err
	}
	keepSession := pgtype.UUID{}
	if keep, keepErr := parseUUID(keepSessionID); keepErr == nil {
		keepSession = keep
	}

	var revoked int64
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		count, txErr := queries.RevokeAdminSessionsForUser(ctx, dbgen.RevokeAdminSessionsForUserParams{
			AdminUserID: id, RevokedReason: optionalText("logout_all"), KeepSessionID: keepSession,
		})
		if txErr != nil {
			return txErr
		}
		revoked = count
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.logout_all", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"revokedSessions": count},
		})
	})
	return revoked, err
}

// PurgeExpiredSessions removes sessions that ended before the cutoff. It is
// driven by the worker; expiry itself is enforced on every request, so this
// only reclaims storage.
func (service *Service) PurgeExpiredSessions(ctx context.Context, retention time.Duration) (int64, error) {
	return dbgen.New(service.pool).PurgeExpiredAdminSessions(ctx, timestamp(service.now().Add(-retention)))
}

// ---------------------------------------------------------------------------
// Keyset cursors
// ---------------------------------------------------------------------------

// Cursors are opaque to the client. Encoding the sort key rather than an offset
// keeps a page boundary stable while rows are being inserted, which an OFFSET
// cannot promise.
func encodeCursor(at time.Time, id string) string {
	if id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(cursor string) (pgtype.Timestamptz, pgtype.UUID, error) {
	if strings.TrimSpace(cursor) == "" {
		return pgtype.Timestamptz{}, pgtype.UUID{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, ErrNotFound
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return pgtype.Timestamptz{}, pgtype.UUID{}, ErrNotFound
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, ErrNotFound
	}
	id, err := parseUUID(parts[1])
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, ErrNotFound
	}
	return timestamp(at), id, nil
}
