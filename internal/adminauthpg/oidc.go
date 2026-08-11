package adminauthpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/rbac"
	"golang.org/x/oauth2"
)

// OIDC sign-in is generic: an operator supplies a discovery document and client
// credentials, and no provider-specific branch exists anywhere here. Anything
// that speaks OpenID Connect Discovery works, and an installation with no
// configured provider authenticates with passwords alone.

const (
	// oidcStateBytes is the entropy in the anti-forgery state value.
	oidcStateBytes = 32
	// OIDCFlowTTL bounds how long an authorization round trip may take.
	OIDCFlowTTL = 10 * time.Minute
	// oidcSecretAssociatedData binds a sealed client secret to its purpose.
	oidcSecretAssociatedData = "admin.oidc.client_secret"
)

var (
	// ErrOIDCDisabled reports that no enabled provider matches the request.
	ErrOIDCDisabled = errors.New("no OIDC provider is enabled")
	// ErrOIDCUnverifiedEmail reports an assertion the provider has not verified.
	ErrOIDCUnverifiedEmail = errors.New("the provider did not verify this address")
	// ErrOIDCNoAccount reports a subject that matches no operator account.
	ErrOIDCNoAccount = errors.New("no operator account is linked to this identity")
)

// OIDCProvider is a configured identity provider as the API presents it.
//
// The client secret is never included: the panel renders and re-saves this
// shape, and echoing the secret back would put it in a browser response for no
// reason.
type OIDCProvider struct {
	Slug                 string
	DisplayName          string
	Issuer               string
	DiscoveryURL         string
	ClientID             string
	Scopes               []string
	Enabled              bool
	RequireVerifiedEmail bool
	AllowAutoProvision   bool
	AutoProvisionRole    string
	HasClientSecret      bool
}

// OIDCFlow is the state a caller must carry across the authorization round trip.
type OIDCFlow struct {
	AuthorizationURL string
	// State and Verifier are held by transport in a short-lived sealed cookie
	// and replayed on callback. Keeping them out of the database means an
	// abandoned sign-in leaves nothing behind to clean up.
	State    string
	Verifier string
}

// ListOIDCProviders returns every configured provider.
func (service *Service) ListOIDCProviders(ctx context.Context) ([]OIDCProvider, error) {
	rows, err := dbgen.New(service.pool).ListAdminOIDCProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]OIDCProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, oidcProviderFrom(row))
	}
	return providers, nil
}

// ListEnabledOIDCProviders returns the providers the sign-in screen may offer.
func (service *Service) ListEnabledOIDCProviders(ctx context.Context) ([]OIDCProvider, error) {
	rows, err := dbgen.New(service.pool).ListEnabledAdminOIDCProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]OIDCProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, oidcProviderFrom(row))
	}
	return providers, nil
}

// SaveOIDCProviderInput configures a provider. An empty ClientSecret leaves the
// stored secret untouched, so the panel can re-save the form without it.
type SaveOIDCProviderInput struct {
	Slug                 string
	DisplayName          string
	Issuer               string
	DiscoveryURL         string
	ClientID             string
	ClientSecret         string
	Scopes               []string
	Enabled              bool
	RequireVerifiedEmail bool
	AllowAutoProvision   bool
	AutoProvisionRole    string
}

// SaveOIDCProvider creates or updates a provider.
func (service *Service) SaveOIDCProvider(
	ctx context.Context, input SaveOIDCProviderInput, actorID string, request RequestContext,
) (OIDCProvider, error) {
	if !strings.HasPrefix(input.Issuer, "https://") || !strings.HasPrefix(input.DiscoveryURL, "https://") {
		return OIDCProvider{}, errors.New("issuer and discovery URL must be HTTPS")
	}
	if input.AllowAutoProvision {
		role, err := rbac.ParseRole(input.AutoProvisionRole)
		if err != nil || role == rbac.RoleOwner {
			// Auto-provisioning an owner would let anyone the provider accepts
			// take full control of the installation.
			return OIDCProvider{}, errors.New("auto-provisioning requires a non-owner role")
		}
	}
	scopes := input.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}

	var sealed []byte
	if input.ClientSecret != "" {
		var err error
		if sealed, err = service.sealSecret(input.ClientSecret, oidcSecretAssociatedData); err != nil {
			return OIDCProvider{}, err
		}
	}

	var provider OIDCProvider
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertAdminOIDCProvider(ctx, dbgen.UpsertAdminOIDCProviderParams{
			Slug:                   strings.ToLower(strings.TrimSpace(input.Slug)),
			DisplayName:            input.DisplayName,
			Issuer:                 input.Issuer,
			DiscoveryUrl:           input.DiscoveryURL,
			ClientID:               input.ClientID,
			ClientSecretCiphertext: sealed,
			Scopes:                 scopes,
			Enabled:                input.Enabled,
			RequireVerifiedEmail:   input.RequireVerifiedEmail,
			AllowAutoProvision:     input.AllowAutoProvision,
			AutoProvisionRole:      optionalText(input.AutoProvisionRole),
		})
		if txErr != nil {
			return txErr
		}
		provider = oidcProviderFrom(row)
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actorID,
			Action: "admin.oidc.provider_saved", Category: "configuration",
			TargetType: "oidc_provider", TargetID: row.Slug, RequestID: request.RequestID,
			Metadata: map[string]any{
				"enabled": row.Enabled, "issuer": row.Issuer,
				"autoProvision": row.AllowAutoProvision,
			},
		})
	})
	if err != nil {
		return OIDCProvider{}, err
	}
	return provider, nil
}

// DeleteOIDCProvider removes a provider and every identity linked through it.
func (service *Service) DeleteOIDCProvider(
	ctx context.Context, slug, actorID string, request RequestContext,
) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.DeleteAdminOIDCProvider(ctx, slug); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: actorID,
			Action: "admin.oidc.provider_deleted", Category: "configuration",
			TargetType: "oidc_provider", TargetID: slug, RequestID: request.RequestID,
		})
	})
}

// BeginOIDC starts an authorization code flow with PKCE.
//
// PKCE is used even though this is a confidential client with a secret: it
// binds the authorization code to this specific flow, so a code intercepted at
// the redirect cannot be redeemed by anyone who did not originate the request.
func (service *Service) BeginOIDC(ctx context.Context, slug, redirectURL string) (OIDCFlow, error) {
	config, _, err := service.oauthConfig(ctx, slug, redirectURL)
	if err != nil {
		return OIDCFlow{}, err
	}

	state, err := randomToken(oidcStateBytes)
	if err != nil {
		return OIDCFlow{}, err
	}
	verifier, err := randomToken(oidcStateBytes)
	if err != nil {
		return OIDCFlow{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))

	url := config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	return OIDCFlow{AuthorizationURL: url, State: state, Verifier: verifier}, nil
}

// CompleteOIDC redeems an authorization code and resolves it to an operator.
//
// The caller has already checked that the returned state matches the one it
// issued; this verifies the ID token, enforces the verified-email requirement,
// and applies the account-matching policy.
func (service *Service) CompleteOIDC(
	ctx context.Context, slug, code, verifier, redirectURL string, request RequestContext,
) (LoginResult, error) {
	config, provider, err := service.oauthConfig(ctx, slug, redirectURL)
	if err != nil {
		return LoginResult{}, err
	}

	token, err := config.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", verifier),
	)
	if err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return LoginResult{}, ErrInvalidCredentials
	}

	stored, err := dbgen.New(service.pool).GetAdminOIDCProviderBySlug(ctx, slug)
	if err != nil {
		return LoginResult{}, err
	}

	// The verifier checks the signature against the provider's published JWKS,
	// the issuer, the audience, and expiry. Skipping any of these would let a
	// token minted for another application authenticate here.
	idToken, err := provider.Verifier(&oidc.Config{ClientID: stored.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err = idToken.Claims(&claims); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	if stored.RequireVerifiedEmail && !claims.EmailVerified {
		return LoginResult{}, ErrOIDCUnverifiedEmail
	}

	account, err := service.resolveOIDCSubject(ctx, stored, idToken.Subject, claims.Email, claims.Name, request)
	if err != nil {
		return LoginResult{}, err
	}
	// The provider has already asserted the second factor if the operator
	// configured one there, so an OIDC sign-in issues a complete session.
	return service.startOIDCSession(ctx, account, request)
}

// resolveOIDCSubject applies the account-matching policy.
//
// A subject already linked signs in. A subject that is not linked never silently
// adopts an existing account by matching its address — that would let anyone who
// can make a provider assert an address take over an operator account. It is
// either auto-provisioned into a fresh account, when the operator has opted in,
// or refused.
func (service *Service) resolveOIDCSubject(
	ctx context.Context, provider dbgen.AdminOidcProvider, subject, email, name string, request RequestContext,
) (dbgen.AdminUser, error) {
	queries := dbgen.New(service.pool)
	linked, err := queries.GetAdminOIDCIdentity(ctx, dbgen.GetAdminOIDCIdentityParams{
		ProviderID: provider.ID, Subject: subject,
	})
	if err == nil {
		if linked.AdminUser.Status != "active" {
			return dbgen.AdminUser{}, ErrAccountDisabled
		}
		_ = queries.RecordAdminOIDCLogin(ctx, linked.AdminOidcIdentity.ID)
		return linked.AdminUser, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return dbgen.AdminUser{}, err
	}

	if !provider.AllowAutoProvision {
		return dbgen.AdminUser{}, ErrOIDCNoAccount
	}
	normalized := NormalizeEmail(email)
	if normalized == "" {
		return dbgen.AdminUser{}, ErrOIDCNoAccount
	}
	// An address that already belongs to an operator is refused rather than
	// merged: linking must be a deliberate act by someone already signed in.
	if _, lookupErr := queries.GetAdminUserByEmail(ctx, normalized); lookupErr == nil {
		return dbgen.AdminUser{}, ErrOIDCNoAccount
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return dbgen.AdminUser{}, lookupErr
	}

	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = normalized
	}

	var created dbgen.AdminUser
	err = service.inTx(ctx, func(tx *dbgen.Queries) error {
		row, txErr := tx.CreateAdminUser(ctx, dbgen.CreateAdminUserParams{
			Email:           strings.TrimSpace(email),
			EmailNormalized: normalized,
			DisplayName:     displayName,
			// No password: this account authenticates through the provider only.
			Locale:   "en",
			Timezone: "UTC",
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if txErr != nil {
			return txErr
		}
		if _, txErr = tx.LinkAdminOIDCIdentity(ctx, dbgen.LinkAdminOIDCIdentityParams{
			AdminUserID: row.ID, ProviderID: provider.ID, Subject: subject,
		}); txErr != nil {
			return txErr
		}
		if txErr = tx.GrantAdminRole(ctx, dbgen.GrantAdminRoleParams{
			AdminUserID: row.ID, Role: provider.AutoProvisionRole.String,
		}); txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, tx, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.oidc.auto_provisioned", Category: "authorization",
			TargetType: "admin_user", TargetID: uuidString(row.ID), RequestID: request.RequestID,
			Metadata: map[string]any{"provider": provider.Slug, "role": provider.AutoProvisionRole.String},
		}); txErr != nil {
			return txErr
		}
		created = row
		return notifySecurity(
			ctx, tx, "admin.oidc.auto_provisioned", uuidString(row.ID), subject, "oidc",
		)
	})
	if err != nil {
		return dbgen.AdminUser{}, err
	}
	return created, nil
}

// LinkOIDCIdentity attaches a provider subject to the signed-in operator.
func (service *Service) LinkOIDCIdentity(
	ctx context.Context, adminUserID, slug, subject string, request RequestContext,
) error {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return err
	}
	provider, err := dbgen.New(service.pool).GetAdminOIDCProviderBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.LinkAdminOIDCIdentity(ctx, dbgen.LinkAdminOIDCIdentityParams{
			AdminUserID: id, ProviderID: provider.ID, Subject: subject,
		}); errors.Is(txErr, pgx.ErrNoRows) {
			// The subject already belongs to someone.
			return ErrConflict
		} else if txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.oidc.identity_linked", Category: "authentication",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"provider": slug},
		})
	})
}

// startOIDCSession issues a fully authenticated session for a resolved account.
func (service *Service) startOIDCSession(
	ctx context.Context, row dbgen.AdminUser, request RequestContext,
) (LoginResult, error) {
	if row.Status != "active" {
		return LoginResult{}, ErrAccountDisabled
	}
	token, digest, err := adminauth.NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrfSecret, err := adminauth.NewCSRFSecret()
	if err != nil {
		return LoginResult{}, err
	}
	now := service.now()
	idle, absolute := service.sessions.SessionDeadlines(now, false)

	var result LoginResult
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		fresh, txErr := queries.RecordAdminLoginSuccess(ctx, row.ID)
		if txErr != nil {
			return txErr
		}
		session, txErr := queries.CreateAdminSession(ctx, dbgen.CreateAdminSessionParams{
			AdminUserID: row.ID, TokenHash: digest, CsrfSecret: csrfSecret,
			PendingTotp: false, AuthMethods: []string{"oidc"},
			Ip: request.IP, UserAgent: optionalText(truncate(request.UserAgent, 400)),
			IdleExpiresAt: timestamp(idle), AbsoluteExpiresAt: timestamp(absolute),
		})
		if txErr != nil {
			return txErr
		}
		roles, txErr := loadRoles(ctx, queries, row.ID)
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.login", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID), RequestID: request.RequestID,
			Metadata: map[string]any{"sessionId": uuidString(session.ID), "method": "oidc"},
		}); txErr != nil {
			return txErr
		}
		result = LoginResult{
			Token:     token,
			CSRFToken: adminauth.CSRFToken(csrfSecret),
			ExpiresAt: idle,
			Account:   service.accountFrom(fresh, roles),
		}
		return nil
	})
	if err != nil {
		return LoginResult{}, err
	}
	return result, nil
}

// oauthConfig resolves a provider's discovery document and builds the exchange
// configuration. Discovery is fetched per flow rather than cached, which keeps a
// rotated JWKS or a moved endpoint from needing a restart.
func (service *Service) oauthConfig(
	ctx context.Context, slug, redirectURL string,
) (*oauth2.Config, *oidc.Provider, error) {
	stored, err := dbgen.New(service.pool).GetAdminOIDCProviderBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrOIDCDisabled
	}
	if err != nil {
		return nil, nil, err
	}
	if !stored.Enabled {
		return nil, nil, ErrOIDCDisabled
	}

	secret := ""
	if len(stored.ClientSecretCiphertext) > 0 {
		if secret, err = service.openSecret(stored.ClientSecretCiphertext, oidcSecretAssociatedData); err != nil {
			return nil, nil, err
		}
	}

	provider, err := oidc.NewProvider(ctx, stored.Issuer)
	if err != nil {
		return nil, nil, err
	}
	return &oauth2.Config{
		ClientID:     stored.ClientID,
		ClientSecret: secret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       stored.Scopes,
	}, provider, nil
}

func oidcProviderFrom(row dbgen.AdminOidcProvider) OIDCProvider {
	return OIDCProvider{
		Slug:                 row.Slug,
		DisplayName:          row.DisplayName,
		Issuer:               row.Issuer,
		DiscoveryURL:         row.DiscoveryUrl,
		ClientID:             row.ClientID,
		Scopes:               row.Scopes,
		Enabled:              row.Enabled,
		RequireVerifiedEmail: row.RequireVerifiedEmail,
		AllowAutoProvision:   row.AllowAutoProvision,
		AutoProvisionRole:    row.AutoProvisionRole.String,
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
