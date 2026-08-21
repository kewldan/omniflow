package customerauthpg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// RotationGrace is how long a superseded session token keeps resolving after
// the session rotated away from it.
//
// A browser opening a page fires several requests at once, all carrying the
// cookie it held when the page loaded. When that moment falls on the rotation
// boundary, one of them rotates the token and the rest arrive holding a digest
// the table no longer has. A minute covers every in-flight request from that
// page load with a wide margin and is far shorter than the rotation interval
// it protects, so a captured cookie still has a bounded life.
const RotationGrace = time.Minute

// rotationAssociatedData binds the sealed replacement token to its purpose, so
// a ciphertext lifted from the temporary store cannot be opened as any other
// field that shares the encryption key.
const rotationAssociatedData = "customer.session.rotation"

// RotationGraceStore holds the short-lived forwarding entry a rotation leaves
// behind: the superseded digest mapped to the session it belonged to and the
// sealed token that replaced it.
//
// Claim must be atomic and single-winner, because it is also what elects the
// one request that performs the rotation; everything else about the store may
// be lost at any time without losing a fact, which is why it is Valkey and not
// a column.
type RotationGraceStore interface {
	Claim(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Lookup(ctx context.Context, key string) (string, bool, error)
	Forget(ctx context.Context, key string) error
}

// SetRotationGrace attaches the store that makes token rotation safe under
// concurrent requests.
//
// Without one, rotation is skipped entirely rather than performed unsafely:
// an installation whose temporary store is unavailable keeps every session
// signed in on its original token until the absolute horizon, which is a
// weaker posture than rotating but a far better one than signing customers
// out at random once a day.
func (service *Service) SetRotationGrace(store RotationGraceStore) {
	service.rotationGrace = store
}

// rotateSessionToken swaps the token behind a live session, if this request
// is the one elected to do so.
//
// The election is the Claim on the superseded digest: exactly one of the
// concurrent requests wins it, writes the forwarding entry, and only then
// performs the compare-and-set in the table. Writing the entry first is what
// lets a request that misses in the table a moment later still find its way
// to the session. A request that loses the claim carries on with the token it
// came with, which the table still holds until the winner's swap lands and the
// forwarding entry covers after.
//
// It reports the new token, or "" when this request did not rotate — which is
// not an error: the session is valid either way.
func (service *Service) rotateSessionToken(
	ctx context.Context, queries *dbgen.Queries, row dbgen.GetCustomerSessionByTokenRow, currentDigest []byte,
) (string, error) {
	if service.rotationGrace == nil {
		return "", nil
	}
	token, digest, err := customerauth.NewSessionToken()
	if err != nil {
		return "", err
	}
	sealed, err := service.sealSecret(token, rotationAssociatedData)
	if err != nil {
		return "", err
	}
	key := rotationKey(currentDigest)
	won, err := service.rotationGrace.Claim(ctx, key, encodeRotationEntry(uuidString(row.ID), sealed), RotationGrace)
	if err != nil {
		// The store is unreachable. Rotating now would leave the losers of the
		// race with nothing to forward them, so the safe answer is not to
		// rotate; the next request tries again.
		return "", nil
	}
	if !won {
		return "", nil
	}
	_, err = queries.RotateCustomerSessionToken(ctx, dbgen.RotateCustomerSessionTokenParams{
		TokenHash: digest, IdleWindow: interval(service.sessions.IdleTimeout),
		SessionID: row.ID, CurrentTokenHash: currentDigest,
	})
	if err != nil {
		// The claim was won but the swap did not land — the row was revoked or
		// rotated through another path between the read and the write. The
		// entry must not outlive that, or it would forward to a token the table
		// never adopted.
		_ = service.rotationGrace.Forget(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return token, nil
}

// resolveSupersededToken is the grace path: a digest the table does not hold
// may be one that rotated away within the last minute.
//
// It answers ErrSessionInvalid only when it positively knows the token is
// dead — no forwarding entry, or an entry pointing at a session that is itself
// unusable. A store that cannot be asked is reported as an operational error
// instead, because clearing the browser's cookie on "could not tell" would
// turn every temporary-store outage into a wave of sign-outs.
func (service *Service) resolveSupersededToken(
	ctx context.Context, queries *dbgen.Queries, digest []byte,
) (Principal, error) {
	if service.rotationGrace == nil {
		return Principal{}, ErrSessionInvalid
	}
	raw, found, err := service.rotationGrace.Lookup(ctx, rotationKey(digest))
	if err != nil {
		return Principal{}, fmt.Errorf("session rotation grace lookup: %w", err)
	}
	if !found {
		return Principal{}, ErrSessionInvalid
	}
	sessionID, sealed, err := decodeRotationEntry(raw)
	if err != nil {
		return Principal{}, ErrSessionInvalid
	}
	replacement, err := service.openSecret(sealed, rotationAssociatedData)
	if err != nil {
		return Principal{}, ErrSessionInvalid
	}
	id, err := parseUUID(sessionID)
	if err != nil {
		return Principal{}, ErrSessionInvalid
	}
	row, err := queries.GetCustomerSessionByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrSessionInvalid
	}
	if err != nil {
		return Principal{}, err
	}
	// The entry is only trustworthy if the table actually adopted the token it
	// names. An entry whose claim was won but whose swap never landed forwards
	// to nothing, and must not reissue a cookie the table would refuse.
	if !bytesEqual(row.TokenHash, customerauth.HashSessionToken(replacement)) {
		return Principal{}, ErrSessionInvalid
	}

	principal, err := service.principalFromSession(sessionRow(row))
	if err != nil {
		return Principal{}, err
	}
	// The request came in on the old cookie; it leaves with the new one, so a
	// browser whose winning response was lost in transit still converges.
	principal.RotatedToken = replacement
	_, _ = queries.TouchCustomerSession(ctx, dbgen.TouchCustomerSessionParams{
		SessionID: row.ID, IdleWindow: interval(service.sessions.IdleTimeout),
	})
	return principal, nil
}

func rotationKey(digest []byte) string {
	return "customer-session-rotation:" + hex.EncodeToString(digest)
}

// encodeRotationEntry renders the forwarding entry as one string: the session
// identifier and the sealed replacement token, separated by a character
// neither contains.
func encodeRotationEntry(sessionID string, sealedToken []byte) string {
	return sessionID + ":" + base64.RawURLEncoding.EncodeToString(sealedToken)
}

func decodeRotationEntry(raw string) (sessionID string, sealedToken []byte, err error) {
	sessionID, encoded, ok := strings.Cut(raw, ":")
	if !ok || sessionID == "" || encoded == "" {
		return "", nil, errors.New("malformed rotation entry")
	}
	sealedToken, err = base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, err
	}
	return sessionID, sealedToken, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for index := range left {
		diff |= left[index] ^ right[index]
	}
	return diff == 0
}
