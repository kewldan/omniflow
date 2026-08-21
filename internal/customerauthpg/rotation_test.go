package customerauthpg

import (
	"bytes"
	"testing"
)

func TestRotationEntryRoundTrips(t *testing.T) {
	sealed := []byte{0, 1, 2, 250, 251, 252}
	raw := encodeRotationEntry("2f1c0c2e-0000-4000-8000-000000000000", sealed)
	sessionID, got, err := decodeRotationEntry(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sessionID != "2f1c0c2e-0000-4000-8000-000000000000" {
		t.Fatalf("session id = %q", sessionID)
	}
	if !bytes.Equal(got, sealed) {
		t.Fatalf("sealed token = %v, want %v", got, sealed)
	}
}

func TestRotationEntryRefusesMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "no-separator", ":", "id:", ":sealed", "id:not*base64*"} {
		if _, _, err := decodeRotationEntry(raw); err == nil {
			t.Fatalf("%q decoded without error", raw)
		}
	}
}

// The sealed replacement token must open only under its own associated data,
// so a ciphertext copied from the temporary store cannot be presented as, say,
// an OIDC client secret or a flow cookie.
func TestRotationEntryTokenIsBoundToItsPurpose(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	sealed, err := service.sealSecret("token-value", rotationAssociatedData)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if opened, openErr := service.openSecret(sealed, rotationAssociatedData); openErr != nil || opened != "token-value" {
		t.Fatalf("open under the right purpose: %q, %v", opened, openErr)
	}
	if _, openErr := service.openSecret(sealed, flowAssociatedData); openErr == nil {
		t.Fatal("a rotation entry opened as a flow cookie")
	}
}

// Without a grace store the service must not rotate at all: rotating without
// a forwarding entry is exactly the race that signed customers out.
func TestRotationIsSkippedWithoutAGraceStore(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	// queries is nil: a rotation attempt would dereference it, so reaching the
	// database here would panic rather than pass.
	rotated, err := service.rotateSessionToken(t.Context(), nil, sessionTokenRow(), []byte("digest"))
	if err != nil {
		t.Fatalf("rotate without a store: %v", err)
	}
	if rotated != "" {
		t.Fatalf("a token was rotated with no grace store: %q", rotated)
	}
}

// A store that cannot be reached is treated the same way: skip, do not break.
func TestRotationIsSkippedWhenTheGraceStoreIsUnreachable(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	service.SetRotationGrace(failingGraceStore{})
	rotated, err := service.rotateSessionToken(t.Context(), nil, sessionTokenRow(), []byte("digest"))
	if err != nil {
		t.Fatalf("rotate with an unreachable store: %v", err)
	}
	if rotated != "" {
		t.Fatalf("a token was rotated with an unreachable grace store: %q", rotated)
	}
}

// A request that loses the claim must not touch the table either: the winner
// is the only writer, which is what makes the swap single-winner.
func TestRotationLosersDoNotWriteTheTable(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	service.SetRotationGrace(alreadyClaimedGraceStore{})
	rotated, err := service.rotateSessionToken(t.Context(), nil, sessionTokenRow(), []byte("digest"))
	if err != nil {
		t.Fatalf("rotate as the loser: %v", err)
	}
	if rotated != "" {
		t.Fatalf("a losing request rotated: %q", rotated)
	}
}

// A digest missing from the table, with no store to consult, is simply invalid.
func TestSupersededLookupWithoutAStoreIsInvalid(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	if _, err = service.resolveSupersededToken(t.Context(), nil, []byte("digest")); err != ErrSessionInvalid {
		t.Fatalf("err = %v, want ErrSessionInvalid", err)
	}
}

// An unreachable store on the grace path is an operational error, not a dead
// session: the middleware keeps the cookie for those, and clears it for these.
func TestSupersededLookupReportsAnUnreachableStoreAsAnError(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	service.SetRotationGrace(failingGraceStore{})
	_, err = service.resolveSupersededToken(t.Context(), nil, []byte("digest"))
	if err == nil || err == ErrSessionInvalid {
		t.Fatalf("err = %v, want an operational error distinct from ErrSessionInvalid", err)
	}
}

func TestSupersededLookupMissIsInvalid(t *testing.T) {
	service, err := New(newUndialledPool(t), bytes.Repeat([]byte{9}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	service.SetRotationGrace(alreadyClaimedGraceStore{})
	if _, err = service.resolveSupersededToken(t.Context(), nil, []byte("digest")); err != ErrSessionInvalid {
		t.Fatalf("err = %v, want ErrSessionInvalid", err)
	}
}
