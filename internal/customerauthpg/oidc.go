package customerauthpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"golang.org/x/oauth2"
)

// Customer OIDC is generic in exactly the way the operator panel's is: an
// operator supplies a discovery document and client credentials, and no
// provider-specific branch exists anywhere here. The difference is plurality —
// several providers may be enabled at once and the sign-in screen renders them
// all — and that difference is data, not code.

const (
	// oidcFlowBytes is the entropy in the state, verifier, and nonce values.
	oidcFlowBytes = 32
	// OIDCFlowTTL bounds how long an authorization round trip may take.
	OIDCFlowTTL = 10 * time.Minute
)

// OIDCProvider is a configured provider as the API presents it. The client
// secret is never included: the panel renders and re-saves this shape, and
// echoing the secret back would put it in a browser response for no reason.
type OIDCProvider struct {
	Slug                 string
	DisplayName          string
	Issuer               string
	DiscoveryURL         string
	ClientID             string
	Scopes               []string
	Enabled              bool
	Icon                 string
	SortOrder            int32
	RequireVerifiedEmail bool
	AllowAutoProvision   bool
	HasClientSecret      bool
}

// OIDCButton is the minimum the sign-in screen needs to render one provider.
type OIDCButton struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Icon        string `json:"icon,omitempty"`
}

// OIDCFlow is the state a caller carries across the authorization round trip.
type OIDCFlow struct {
	AuthorizationURL string
	// State, Verifier, and Nonce are held by transport in a short-lived sealed
	// cookie and replayed on callback, so an abandoned sign-in leaves nothing
	// behind to clean up.
	State    string
	Verifier string
	Nonce    string
}

// ListEnabledOIDCButtons returns what the sign-in screen should offer.
func (service *Service) ListEnabledOIDCButtons(ctx context.Context) ([]OIDCButton, error) {
	rows, err := dbgen.New(service.pool).ListEnabledCustomerOIDCProviders(ctx)
	if err != nil {
		return nil, err
	}
	buttons := make([]OIDCButton, 0, len(rows))
	for _, row := range rows {
		buttons = append(buttons, OIDCButton{
			Slug: row.Slug, DisplayName: row.DisplayName, Icon: row.Icon.String,
		})
	}
	return buttons, nil
}

// ListOIDCProviders returns every configured provider for the operator panel.
func (service *Service) ListOIDCProviders(ctx context.Context) ([]OIDCProvider, error) {
	rows, err := dbgen.New(service.pool).ListCustomerOIDCProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]OIDCProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, oidcProviderFrom(row))
	}
	return providers, nil
}

// SaveOIDCProviderInput is one provider as an operator submits it.
type SaveOIDCProviderInput struct {
	Slug                 string
	DisplayName          string
	Issuer               string
	DiscoveryURL         string
	ClientID             string
	ClientSecret         string
	Scopes               []string
	Enabled              bool
	Icon                 string
	SortOrder            int32
	RequireVerifiedEmail bool
	AllowAutoProvision   bool
}

// SaveOIDCProvider creates or updates a provider.
//
// Disabling one revokes the sessions it established in the same call. Leaving
// them alive would mean an operator who has just turned a provider off still has
// its customers signed in for up to the absolute session lifetime, which is not
// what "disabled" means to the person clicking it.
func (service *Service) SaveOIDCProvider(
	ctx context.Context, input SaveOIDCProviderInput,
) (OIDCProvider, error) {
	if input.Slug = strings.ToLower(strings.TrimSpace(input.Slug)); input.Slug == "" {
		return OIDCProvider{}, errors.New("a provider slug is required")
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		return OIDCProvider{}, errors.New("a display name is required")
	}
	if !strings.HasPrefix(input.Issuer, "https://") || !strings.HasPrefix(input.DiscoveryURL, "https://") {
		return OIDCProvider{}, errors.New("issuer and discovery URL must be https")
	}
	if len(input.Scopes) == 0 {
		input.Scopes = []string{"openid", "email", "profile"}
	}

	var sealed []byte
	if strings.TrimSpace(input.ClientSecret) != "" {
		var err error
		if sealed, err = service.sealSecret(input.ClientSecret, secretAssociatedData); err != nil {
			return OIDCProvider{}, err
		}
	}

	row, err := dbgen.New(service.pool).UpsertCustomerOIDCProvider(ctx, dbgen.UpsertCustomerOIDCProviderParams{
		Slug: input.Slug, DisplayName: input.DisplayName, Issuer: input.Issuer,
		DiscoveryUrl: input.DiscoveryURL, ClientID: input.ClientID,
		ClientSecretCiphertext: sealed, Scopes: input.Scopes, Enabled: input.Enabled,
		Icon: optionalText(input.Icon), SortOrder: input.SortOrder,
		RequireVerifiedEmail: input.RequireVerifiedEmail,
		AllowAutoProvision:   input.AllowAutoProvision,
	})
	if err != nil {
		return OIDCProvider{}, err
	}
	if !input.Enabled {
		if _, revokeErr := service.RevokeSessionsForProvider(ctx, input.Slug); revokeErr != nil {
			return OIDCProvider{}, revokeErr
		}
	}
	return oidcProviderFrom(row), nil
}

// DeleteOIDCProvider removes a provider and ends the sessions it created.
//
// The linked `identities` rows are deliberately left alone. They are the record
// that a customer once signed in that way, and deleting them would silently take
// away a sign-in method from customers who may have no other one — the unlink
// guard exists precisely to stop that happening by accident.
func (service *Service) DeleteOIDCProvider(ctx context.Context, slug string) error {
	removed, err := dbgen.New(service.pool).DeleteCustomerOIDCProvider(ctx, slug)
	if err != nil {
		return err
	}
	if removed == 0 {
		return ErrNotFound
	}
	_, err = service.RevokeSessionsForProvider(ctx, slug)
	return err
}

// BeginOIDC starts an authorization code flow with PKCE and a nonce.
//
// PKCE binds the authorization code to this flow, so a code intercepted at the
// redirect cannot be redeemed by anyone else. The nonce binds the resulting ID
// token to the same flow, which PKCE alone does not do: without it, a token
// obtained elsewhere for this client could be replayed into this callback.
func (service *Service) BeginOIDC(ctx context.Context, slug, redirectURL string) (OIDCFlow, error) {
	config, _, _, err := service.oauthConfig(ctx, slug, redirectURL)
	if err != nil {
		return OIDCFlow{}, err
	}

	state, err := randomToken(oidcFlowBytes)
	if err != nil {
		return OIDCFlow{}, err
	}
	verifier, err := randomToken(oidcFlowBytes)
	if err != nil {
		return OIDCFlow{}, err
	}
	nonce, err := randomToken(oidcFlowBytes)
	if err != nil {
		return OIDCFlow{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))

	authorizationURL := config.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	return OIDCFlow{
		AuthorizationURL: authorizationURL, State: state, Verifier: verifier, Nonce: nonce,
	}, nil
}

// CompleteOIDCSignIn redeems an authorization code and opens a session.
func (service *Service) CompleteOIDCSignIn(
	ctx context.Context, slug, code, verifier, nonce, redirectURL string, request RequestContext,
) (SignInResult, error) {
	stored, claims, err := service.exchangeOIDC(ctx, slug, code, verifier, nonce, redirectURL)
	if err != nil {
		return SignInResult{}, err
	}

	provider := customerauth.OIDCProvider(stored.Slug)
	var result SignInResult
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		existing, lookupErr := queries.GetActiveIdentityBySubject(ctx, dbgen.GetActiveIdentityBySubjectParams{
			Provider: provider, ProviderSubject: claims.Subject,
		})
		switch {
		case lookupErr == nil:
			if existing.UserStatus != "active" {
				return ErrAccountInactive
			}
			result.Customer = Customer{
				ID:       uuidString(existing.UserID),
				Status:   existing.UserStatus,
				Locale:   existing.UserLocale,
				Timezone: existing.UserTimezone,
			}
		case errors.Is(lookupErr, pgx.ErrNoRows):
			// A subject nobody has linked never adopts an existing customer by
			// matching its email address. Doing so would let anyone who can make
			// a provider assert an address take over that customer's account, and
			// it is why an existing customer must link a provider from inside a
			// session they already hold rather than by signing in with it.
			if !stored.AllowAutoProvision {
				return ErrSignInRejected
			}
			locale := "en"
			if strings.HasPrefix(strings.ToLower(claims.Locale), "ru") {
				locale = "ru"
			}
			created, createErr := queries.CreateCustomerForSignIn(ctx, dbgen.CreateCustomerForSignInParams{
				Locale: locale, Timezone: "UTC",
			})
			if createErr != nil {
				return createErr
			}
			if _, createErr = queries.LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{
				UserID: created.ID, Provider: provider, ProviderSubject: claims.Subject,
				VerifiedAt: pgtype.Timestamptz{Time: service.now(), Valid: true},
				Metadata:   []byte("{}"),
			}); createErr != nil {
				return createErr
			}
			result.Customer = customerFromUser(created)
		default:
			return lookupErr
		}

		session, sessionErr := service.openSession(
			ctx, queries, result.Customer.ID, "oidc", stored.Slug, request,
		)
		if sessionErr != nil {
			return sessionErr
		}
		result.Token, result.ExpiresAt, result.SessionID = session.token, session.expiresAt, session.id
		return service.appendSecurityEvent(ctx, queries, result.Customer.ID, "signed_in", request,
			map[string]any{"method": "oidc", "provider": stored.Slug})
	})
	if err != nil {
		return SignInResult{}, err
	}
	return result, nil
}

// LinkOIDCIdentity attaches an external identity to the signed-in customer.
//
// This is the only route by which an existing customer gains an OIDC method, and
// it runs inside a session they already hold — which is what makes it safe. If
// the subject already belongs to somebody else the attempt is refused as a
// conflict rather than merged: two customers are two customers, and quietly
// joining them would move one person's orders, wallet, and subscription into
// another person's account with no way back.
func (service *Service) LinkOIDCIdentity(
	ctx context.Context, customerID, slug, code, verifier, nonce, redirectURL string,
	request RequestContext,
) error {
	stored, claims, err := service.exchangeOIDC(ctx, slug, code, verifier, nonce, redirectURL)
	if err != nil {
		return err
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	provider := customerauth.OIDCProvider(stored.Slug)

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		existing, lookupErr := queries.GetActiveIdentityBySubject(ctx, dbgen.GetActiveIdentityBySubjectParams{
			Provider: provider, ProviderSubject: claims.Subject,
		})
		switch {
		case lookupErr == nil:
			if uuidString(existing.UserID) == customerID {
				// Already linked to this customer: linking again is a no-op
				// rather than an error, so a double submission is harmless.
				return nil
			}
			return customerauth.ErrIdentityTaken
		case !errors.Is(lookupErr, pgx.ErrNoRows):
			return lookupErr
		}

		if _, linkErr := queries.LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{
			UserID: userID, Provider: provider, ProviderSubject: claims.Subject,
			VerifiedAt: pgtype.Timestamptz{Time: service.now(), Valid: true},
			Metadata:   []byte("{}"),
		}); linkErr != nil {
			return linkErr
		}
		return service.appendSecurityEvent(ctx, queries, customerID, "identity_linked", request,
			map[string]any{"provider": stored.Slug})
	})
}

// exchangeOIDC performs the token exchange and returns the verified claims.
func (service *Service) exchangeOIDC(
	ctx context.Context, slug, code, verifier, nonce, redirectURL string,
) (dbgen.CustomerOidcProvider, customerauth.Claims, error) {
	config, discovered, stored, err := service.oauthConfig(ctx, slug, redirectURL)
	if err != nil {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, err
	}

	token, err := config.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, ErrSignInRejected
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, ErrSignInRejected
	}

	// The verifier checks the signature against the provider's published JWKS,
	// the issuer, the audience, and expiry. Skipping any of these would let a
	// token minted for another application authenticate here.
	idToken, err := discovered.Verifier(&oidc.Config{ClientID: stored.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, ErrSignInRejected
	}
	// Compared in constant time and against the value this flow issued, so a
	// token obtained through some other flow cannot be replayed into this one.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, ErrSignInRejected
	}

	var raw struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Locale        string `json:"locale"`
	}
	if err = idToken.Claims(&raw); err != nil {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, ErrSignInRejected
	}

	claims, err := customerauth.ResolveClaims(customerauth.Claims{
		Subject: idToken.Subject, Email: raw.Email, EmailVerified: raw.EmailVerified,
		Name: raw.Name, Locale: raw.Locale,
	}, stored.RequireVerifiedEmail)
	if err != nil {
		return dbgen.CustomerOidcProvider{}, customerauth.Claims{}, err
	}
	return stored, claims, nil
}

// oauthConfig resolves a provider's discovery document and builds the exchange
// configuration. Discovery is fetched per flow rather than cached, so a rotated
// JWKS or a moved endpoint does not need a restart.
func (service *Service) oauthConfig(
	ctx context.Context, slug, redirectURL string,
) (*oauth2.Config, *oidc.Provider, dbgen.CustomerOidcProvider, error) {
	stored, err := dbgen.New(service.pool).GetCustomerOIDCProvider(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, stored, ErrOIDCDisabled
	}
	if err != nil {
		return nil, nil, stored, err
	}
	if !stored.Enabled {
		return nil, nil, stored, ErrOIDCDisabled
	}

	secret := ""
	if len(stored.ClientSecretCiphertext) > 0 {
		if secret, err = service.openSecret(stored.ClientSecretCiphertext, secretAssociatedData); err != nil {
			return nil, nil, stored, err
		}
	}

	discovered, err := oidc.NewProvider(oidc.ClientContext(ctx, service.httpClient), stored.Issuer)
	if err != nil {
		return nil, nil, stored, err
	}
	return &oauth2.Config{
		ClientID:     stored.ClientID,
		ClientSecret: secret,
		Endpoint:     discovered.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       stored.Scopes,
	}, discovered, stored, nil
}

func oidcProviderFrom(row dbgen.CustomerOidcProvider) OIDCProvider {
	return OIDCProvider{
		Slug: row.Slug, DisplayName: row.DisplayName, Issuer: row.Issuer,
		DiscoveryURL: row.DiscoveryUrl, ClientID: row.ClientID, Scopes: row.Scopes,
		Enabled: row.Enabled, Icon: row.Icon.String, SortOrder: row.SortOrder,
		RequireVerifiedEmail: row.RequireVerifiedEmail,
		AllowAutoProvision:   row.AllowAutoProvision,
		HasClientSecret:      len(row.ClientSecretCiphertext) > 0,
	}
}

func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
