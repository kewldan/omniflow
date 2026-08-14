//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// Passkeys, exercised against a real database and a real WebAuthn library.
//
// The ceremony is the part nobody can check by reading: an attestation is CBOR
// inside base64url inside JSON, and a mistake anywhere in that stack produces
// the same "credential not accepted" as a genuine forgery. So this drives it
// with a software authenticator that signs the way a real one does, which is
// what makes a passing test mean an operator can actually sign in.
//
// It is also the only place the sign-count rule meets a real stored counter.
// The unit test proves the comparison; this proves the value being compared is
// the one the authenticator sent and the one the database kept.

const (
	passkeyPublicURL = "https://panel.example.test"
	passkeyRPID      = "panel.example.test"
	passkeyOrigin    = "https://panel.example.test"
)

func newPasskeyService(t *testing.T, harness *harness) *adminauthpg.Service {
	t.Helper()
	service, err := adminauthpg.New(harness.pool, adminTestKey, adminauthpg.Options{
		PasswordParams: cheapPasswordParams,
		PublicURL:      passkeyPublicURL,
		ServiceName:    "Omniflow",
	})
	if err != nil {
		t.Fatalf("build admin service: %v", err)
	}
	if !service.PasskeysEnabled() {
		t.Fatal("a service given a public URL should have passkeys enabled")
	}
	return service
}

// authenticator is a software security key.
//
// It holds one credential, signs with ES256, and reports user verification —
// the three things the product requires of a real one. Its signature counter
// starts at one and advances, which is what a key that implements a counter
// does; `TestPasskeyCloneIsRefused` is what happens when it stops.
type authenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
	signCount    uint32
	// userVerified is what the authenticator claims it did. Turning it off is
	// how the test reproduces a key with no PIN and no fingerprint.
	userVerified bool
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id := make([]byte, 32)
	if _, err = rand.Read(id); err != nil {
		t.Fatalf("credential id: %v", err)
	}
	return &authenticator{key: key, credentialID: id, signCount: 1, userVerified: true}
}

// Authenticator data flags, from the WebAuthn specification.
const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
	flagAttestedData = 0x40
)

func (device *authenticator) flags(attested bool) byte {
	value := byte(flagUserPresent)
	if device.userVerified {
		value |= flagUserVerified
	}
	if attested {
		value |= flagAttestedData
	}
	return value
}

// coseKey encodes the public half as COSE_Key, which is what an attestation
// carries and what the server stores.
func (device *authenticator) coseKey() []byte {
	x := make([]byte, 32)
	y := make([]byte, 32)
	device.key.PublicKey.X.FillBytes(x)
	device.key.PublicKey.Y.FillBytes(y)
	// Keys are the COSE labels: 1 kty (2 = EC2), 3 alg (-7 = ES256), -1 crv
	// (1 = P-256), -2 x, -3 y. cbor's canonical encoding orders them the way
	// the specification requires.
	encoded, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		panic(err)
	}
	return encoded
}

func (device *authenticator) authenticatorData(attested bool) []byte {
	rpHash := sha256.Sum256([]byte(passkeyRPID))
	data := make([]byte, 0, 128)
	data = append(data, rpHash[:]...)
	data = append(data, device.flags(attested))
	data = binary.BigEndian.AppendUint32(data, device.signCount)
	if !attested {
		return data
	}
	// Attested credential data: AAGUID, then the credential id with its length,
	// then the public key.
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(device.credentialID)))
	data = append(data, device.credentialID...)
	return append(data, device.coseKey()...)
}

func clientData(t *testing.T, ceremony string, challenge protocol.URLEncodedBase64) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"challenge":   base64.RawURLEncoding.EncodeToString(challenge),
		"crossOrigin": false,
		"origin":      passkeyOrigin,
		"type":        ceremony,
	})
	if err != nil {
		t.Fatalf("client data: %v", err)
	}
	return encoded
}

func urlB64(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

// create answers a registration challenge with an attestation of format
// "none", which is what a platform authenticator sends when the relying party
// asks for no attestation.
func (device *authenticator) create(
	t *testing.T, options protocol.PublicKeyCredentialCreationOptions,
) *protocol.ParsedCredentialCreationData {
	t.Helper()
	clientDataJSON := clientData(t, "webauthn.create", options.Challenge)
	attestation, err := cbor.Marshal(map[string]any{
		"attStmt":  map[string]any{},
		"authData": device.authenticatorData(true),
		"fmt":      "none",
	})
	if err != nil {
		t.Fatalf("attestation object: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"clientExtensionResults": map[string]any{},
		"id":                     urlB64(device.credentialID),
		"rawId":                  urlB64(device.credentialID),
		"response": map[string]any{
			"attestationObject": urlB64(attestation),
			"clientDataJSON":    urlB64(clientDataJSON),
			"transports":        []string{"internal"},
		},
		"type": "public-key",
	})
	if err != nil {
		t.Fatalf("credential body: %v", err)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse creation response: %v", err)
	}
	return parsed
}

// get answers a sign-in challenge, signing over the authenticator data and the
// hash of the client data exactly as the specification prescribes.
func (device *authenticator) get(
	t *testing.T, options protocol.PublicKeyCredentialRequestOptions, userHandle []byte,
) *protocol.ParsedCredentialAssertionData {
	t.Helper()
	clientDataJSON := clientData(t, "webauthn.get", options.Challenge)
	authData := device.authenticatorData(false)

	clientHash := sha256.Sum256(clientDataJSON)
	signed := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, device.key, signed[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"clientExtensionResults": map[string]any{},
		"id":                     urlB64(device.credentialID),
		"rawId":                  urlB64(device.credentialID),
		"response": map[string]any{
			"authenticatorData": urlB64(authData),
			"clientDataJSON":    urlB64(clientDataJSON),
			"signature":         urlB64(signature),
			"userHandle":        urlB64(userHandle),
		},
		"type": "public-key",
	})
	if err != nil {
		t.Fatalf("assertion body: %v", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse assertion response: %v", err)
	}
	return parsed
}

// registerPasskey runs the whole registration ceremony.
func registerPasskey(
	t *testing.T, ctx context.Context, service *adminauthpg.Service,
	device *authenticator, accountID, label string,
) (adminauthpg.Passkey, error) {
	t.Helper()
	challenge, err := service.BeginPasskeyRegistration(ctx, accountID)
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	options, ok := challenge.Options.(protocol.PublicKeyCredentialCreationOptions)
	if !ok {
		t.Fatalf("registration options are %T, want creation options", challenge.Options)
	}
	return service.FinishPasskeyRegistration(
		ctx, accountID, label, challenge.State, device.create(t, options),
		adminauthpg.RequestContext{RequestID: "register"},
	)
}

// signIn runs the whole sign-in ceremony.
func signIn(
	t *testing.T, ctx context.Context, service *adminauthpg.Service,
	device *authenticator, userHandle []byte,
) (adminauthpg.LoginResult, error) {
	t.Helper()
	challenge, err := service.BeginPasskeyLogin()
	if err != nil {
		t.Fatalf("begin login: %v", err)
	}
	options, ok := challenge.Options.(protocol.PublicKeyCredentialRequestOptions)
	if !ok {
		t.Fatalf("login options are %T, want request options", challenge.Options)
	}
	return service.FinishPasskeyLogin(
		ctx, challenge.State, device.get(t, options, userHandle),
		adminauthpg.RequestContext{RequestID: "passkey-login"},
	)
}

// accountHandle is the user handle an authenticator stores and returns, which
// is the account's UUID bytes rather than anything a person could change.
func accountHandle(t *testing.T, id string) []byte {
	t.Helper()
	var value pgtype.UUID
	if err := value.Scan(id); err != nil {
		t.Fatalf("account handle: %v", err)
	}
	return value.Bytes[:]
}

// createOperator provisions a second account, so the ownership tests have
// somebody real to be refused rather than a made-up identifier.
func createOperator(
	t *testing.T, ctx context.Context, service *adminauthpg.Service,
	owner adminauthpg.Account, email string,
) adminauthpg.Account {
	t.Helper()
	account, err := service.CreateAccount(ctx, adminauthpg.CreateAccountInput{
		DisplayName: "Second", Email: email, Locale: "en",
		Password: "correct horse battery staple", Roles: []rbac.Role{rbac.RoleSupport},
	}, owner.ID, adminauthpg.RequestContext{RequestID: "create-operator"})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return account
}

func TestPasskeySignsInWithoutAPasswordOrASecondFactor(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newPasskeyService(t, harness)
	owner := bootstrapOwner(t, ctx, service)
	device := newAuthenticator(t)

	stored, err := registerPasskey(t, ctx, service, device, owner.ID, "Work laptop")
	if err != nil {
		t.Fatalf("register passkey: %v", err)
	}
	if stored.Label != "Work laptop" || !stored.Discoverable {
		t.Fatalf("stored passkey = %+v", stored)
	}

	// The counter advances the way a real key's does between ceremonies.
	device.signCount++
	result, err := signIn(t, ctx, service, device, accountHandle(t, owner.ID))
	if err != nil {
		t.Fatalf("passkey sign-in: %v", err)
	}

	// The point of the whole feature: a complete session, not a pending one.
	if result.ChallengeRequired {
		t.Fatal("a passkey assertion should not ask for a second factor")
	}
	if result.Token == "" || result.CSRFToken == "" {
		t.Fatal("a passkey sign-in should issue a usable session")
	}
	if result.Account.ID != owner.ID {
		t.Fatalf("signed in as %s, want %s", result.Account.ID, owner.ID)
	}
	if !result.ExpiresAt.After(time.Now()) {
		t.Fatalf("session expires at %s, which is not in the future", result.ExpiresAt)
	}

	// Resolving the token is the strongest form of the same claim: a session
	// still waiting on a second factor answers ErrChallengeRequired here, so
	// this failing would mean the passkey signed nobody in.
	principal, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("the token a passkey issued does not authorise anything: %v", err)
	}

	// The session records how it was established, so an audit reader can tell a
	// passkey sign-in from a password one.
	sessions, err := service.ListSessions(ctx, owner.ID, principal.SessionID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	var found bool
	for _, session := range sessions {
		if session.Current {
			found = true
			if len(session.Methods) != 1 || session.Methods[0] != "passkey" {
				t.Fatalf("session methods = %v, want [passkey]", session.Methods)
			}
		}
	}
	if !found {
		t.Fatal("the passkey session is not in its own session list")
	}

	// And the key now reports when it was last used, which is the only signal an
	// operator has for deciding a key is stale enough to revoke.
	keys, err := service.Passkeys(ctx, owner.ID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("passkeys = %v, %v", keys, err)
	}
	if keys[0].LastUsedAt.IsZero() {
		t.Fatal("a passkey that just signed in has no last-used time")
	}
}

func TestPasskeyCloneIsRefused(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newPasskeyService(t, harness)
	owner := bootstrapOwner(t, ctx, service)
	device := newAuthenticator(t)
	handle := accountHandle(t, owner.ID)

	if _, err := registerPasskey(t, ctx, service, device, owner.ID, "Key"); err != nil {
		t.Fatalf("register passkey: %v", err)
	}
	device.signCount++
	if _, err := signIn(t, ctx, service, device, handle); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	// A copy of the key has the same private half and the same credential id,
	// but its counter is whatever it was when the copy was taken. Replaying at
	// or below the stored value is exactly what a cloned key looks like.
	clone := &authenticator{
		key: device.key, credentialID: device.credentialID,
		signCount: device.signCount, userVerified: true,
	}
	_, err := signIn(t, ctx, service, clone, handle)
	if !errors.Is(err, adminauth.ErrPasskeyCloned) {
		t.Fatalf("a replayed counter gave %v, want ErrPasskeyCloned", err)
	}

	// The original still works, because refusing the clone must not have moved
	// the stored counter backwards or locked the credential.
	device.signCount++
	if _, err = signIn(t, ctx, service, device, handle); err != nil {
		t.Fatalf("the original key stopped working after a clone was refused: %v", err)
	}
}

func TestPasskeyWithoutUserVerificationIsRefusedAtRegistration(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newPasskeyService(t, harness)
	owner := bootstrapOwner(t, ctx, service)

	// A key that only proves possession. Storing it would make a passkey one
	// factor, and this product signs in with one credential because it is two.
	device := newAuthenticator(t)
	device.userVerified = false

	_, err := registerPasskey(t, ctx, service, device, owner.ID, "PIN-less key")
	if err == nil {
		t.Fatal("an unverified authenticator was registered")
	}
	keys, listErr := service.Passkeys(ctx, owner.ID)
	if listErr != nil || len(keys) != 0 {
		t.Fatalf("a refused registration left %v behind (%v)", keys, listErr)
	}
}

func TestPasskeyRenameAndRevokeAreScopedToTheirOwner(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newPasskeyService(t, harness)
	owner := bootstrapOwner(t, ctx, service)
	other := createOperator(t, ctx, service, owner, "second@example.com")

	stored, err := registerPasskey(t, ctx, service, newAuthenticator(t), owner.ID, "Owner key")
	if err != nil {
		t.Fatalf("register passkey: %v", err)
	}

	// Another operator holds a valid session and a valid passkey id. Neither is
	// authority over somebody else's credential.
	if _, err = service.RenamePasskey(
		ctx, other.ID, stored.ID, "Stolen", adminauthpg.RequestContext{},
	); !errors.Is(err, adminauthpg.ErrNotFound) {
		t.Fatalf("renaming another operator's passkey gave %v, want ErrNotFound", err)
	}
	if err = service.DeletePasskey(
		ctx, other.ID, stored.ID, adminauthpg.RequestContext{},
	); !errors.Is(err, adminauthpg.ErrNotFound) {
		t.Fatalf("revoking another operator's passkey gave %v, want ErrNotFound", err)
	}

	renamed, err := service.RenamePasskey(
		ctx, owner.ID, stored.ID, "  Desk key  ", adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("rename own passkey: %v", err)
	}
	if renamed.Label != "Desk key" {
		t.Fatalf("label = %q, want the trimmed form", renamed.Label)
	}

	if err = service.DeletePasskey(
		ctx, owner.ID, stored.ID, adminauthpg.RequestContext{},
	); err != nil {
		t.Fatalf("revoke own passkey: %v", err)
	}
	keys, err := service.Passkeys(ctx, owner.ID)
	if err != nil || len(keys) != 0 {
		t.Fatalf("passkeys after revocation = %v (%v)", keys, err)
	}
}

func TestPasskeysAreOffWithoutAPublicURL(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	// No public URL, so there is no origin to bind a credential to.
	service := newAdminService(t, harness, time.Now)
	if service.PasskeysEnabled() {
		t.Fatal("passkeys should be off without a public URL")
	}
	if _, err := service.BeginPasskeyLogin(); !errors.Is(err, adminauthpg.ErrPasskeysUnavailable) {
		t.Fatalf("begin login gave %v, want ErrPasskeysUnavailable", err)
	}
	if _, err := service.BeginPasskeyRegistration(
		ctx, "00000000-0000-0000-0000-000000000000",
	); !errors.Is(err, adminauthpg.ErrPasskeysUnavailable) {
		t.Fatalf("begin registration gave %v, want ErrPasskeysUnavailable", err)
	}
}
