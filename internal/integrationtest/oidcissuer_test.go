//go:build integration

package integrationtest

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// fakeIssuer is a minimal OpenID provider: discovery, a JWKS, and a token
// endpoint. The authorization code it is handed is "<subject>|<nonce>", so a
// test decides which subject the issuer asserts without a browser round trip —
// which is exactly the power an attacker who controls an issuer has.
type fakeIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newFakeIssuer(t *testing.T, clientID string) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	issuer := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(writer http.ResponseWriter, _ *http.Request) {
		base := issuer.server.URL
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/authorize",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		subject, nonce, _ := strings.Cut(request.PostForm.Get("code"), "|")
		now := time.Now()
		claims := map[string]any{
			"iss": issuer.server.URL, "sub": subject, "aud": clientID,
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
			"email": subject + "@example.com", "email_verified": true,
		}
		if nonce != "" {
			claims["nonce"] = nonce
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"access_token": "opaque", "token_type": "Bearer", "expires_in": 300,
			"id_token": issuer.sign(t, claims),
		})
	})
	issuer.server = httptest.NewTLSServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (issuer *fakeIssuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, issuer.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// trustingClient trusts the self-signed certificates of every fake issuer.
func trustingClient(issuers ...*fakeIssuer) *http.Client {
	roots := x509.NewCertPool()
	for _, issuer := range issuers {
		roots.AddCert(issuer.server.Certificate())
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
	}
}

// An administrator who could repoint an operator OIDC provider at an issuer
// they control could assert the owner's subject and walk in without a second
// factor. The binding now records the issuer it was made at, so the repointed
// provider matches nobody — and the save path refuses the repoint outright.
func TestAdminOIDCBindingIsPinnedToItsIssuer(t *testing.T) {
	harness := newHarness(t)
	service := newAdminService(t, harness, time.Now)
	legitimate := newFakeIssuer(t, "omniflow-panel")
	attacker := newFakeIssuer(t, "omniflow-panel")
	ctx := oidc.ClientContext(context.Background(), trustingClient(legitimate, attacker))
	owner := bootstrapOwner(t, ctx, service)
	request := adminauthpg.RequestContext{RequestID: "oidc-pin"}
	const redirect = "https://panel.test/v1/panel/auth/oidc/corp/callback"

	save := func(issuer string) error {
		_, err := service.SaveOIDCProvider(ctx, adminauthpg.SaveOIDCProviderInput{
			Slug: "corp", DisplayName: "Corp", Issuer: issuer, DiscoveryURL: issuer,
			ClientID: "omniflow-panel", ClientSecret: "client-secret", Enabled: true,
			RequireVerifiedEmail: true,
		}, owner.ID, request)
		return err
	}
	if err := save(legitimate.server.URL); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if err := service.LinkOIDCIdentity(ctx, owner.ID, "corp", "owner-subject", request); err != nil {
		t.Fatalf("link owner: %v", err)
	}
	if _, err := service.CompleteOIDC(ctx, "corp", "owner-subject|", "verifier", redirect, request); err != nil {
		t.Fatalf("legitimate sign-in: %v", err)
	}

	// The save path refuses to move a provider that has bindings.
	if err := save(attacker.server.URL); !errors.Is(err, adminauthpg.ErrOIDCProviderBound) {
		t.Fatalf("repointing a bound provider: err = %v, want ErrOIDCProviderBound", err)
	}

	// Even if the row is repointed behind the service's back, the pinned
	// binding does not match a subject the new issuer asserts.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE admin_oidc_providers SET issuer = $1, discovery_url = $1 WHERE slug = 'corp'`,
		attacker.server.URL); err != nil {
		t.Fatalf("repoint provider: %v", err)
	}
	if _, err := service.CompleteOIDC(ctx, "corp", "owner-subject|", "verifier", redirect, request); !errors.Is(err, adminauthpg.ErrOIDCNoAccount) {
		t.Fatalf("sign-in through a repointed issuer: err = %v, want ErrOIDCNoAccount", err)
	}
}

// Customer OIDC providers are editable by administrators, so the same pin has
// to hold there: a subject linked at one issuer is not the same customer when
// another issuer asserts it.
func TestCustomerOIDCBindingIsPinnedToItsIssuer(t *testing.T) {
	harness := newHarness(t)
	legitimate := newFakeIssuer(t, "omniflow-account")
	attacker := newFakeIssuer(t, "omniflow-account")
	client := trustingClient(legitimate, attacker)
	service, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{
		PublicURL: "https://vpn.test", HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("build customer service: %v", err)
	}
	ctx := context.Background()
	const redirect = "https://vpn.test/v1/account/auth/oidc/corp/callback"
	save := func(issuer string) error {
		_, err := service.SaveOIDCProvider(ctx, customerauthpg.SaveOIDCProviderInput{
			Slug: "corp", DisplayName: "Corp", Issuer: issuer, DiscoveryURL: issuer,
			ClientID: "omniflow-account", ClientSecret: "client-secret", Enabled: true,
			RequireVerifiedEmail: true, AllowAutoProvision: true,
		})
		return err
	}
	if err = save(legitimate.server.URL); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	first, err := service.CompleteOIDCSignIn(
		ctx, "corp", "victim|n1", "verifier", "n1", redirect, customerauthpg.RequestContext{},
	)
	if err != nil {
		t.Fatalf("legitimate sign-in: %v", err)
	}
	again, err := service.CompleteOIDCSignIn(
		ctx, "corp", "victim|n2", "verifier", "n2", redirect, customerauthpg.RequestContext{},
	)
	if err != nil || again.Customer.ID != first.Customer.ID {
		t.Fatalf("second legitimate sign-in: %v (customer %s, want %s)", err, again.Customer.ID, first.Customer.ID)
	}

	if err = save(attacker.server.URL); !errors.Is(err, customerauthpg.ErrOIDCProviderBound) {
		t.Fatalf("repointing a bound provider: err = %v, want ErrOIDCProviderBound", err)
	}

	if _, err = harness.pool.Exec(ctx,
		`UPDATE customer_oidc_providers SET issuer = $1, discovery_url = $1 WHERE slug = 'corp'`,
		attacker.server.URL); err != nil {
		t.Fatalf("repoint provider: %v", err)
	}
	if _, err = service.CompleteOIDCSignIn(
		ctx, "corp", "victim|n3", "verifier", "n3", redirect, customerauthpg.RequestContext{},
	); !errors.Is(err, customerauthpg.ErrSignInRejected) {
		t.Fatalf("sign-in through a repointed issuer: err = %v, want ErrSignInRejected", err)
	}
}
