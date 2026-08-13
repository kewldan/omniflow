package httpapi

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// oidcFlowCookie carries the anti-forgery state and the PKCE verifier across
// the authorization round trip.
//
// It lives in a short-lived, HttpOnly, SameSite=Lax cookie rather than in the
// database, so an abandoned sign-in leaves nothing to clean up and a provider
// that never redirects back costs nothing. SameSite=Lax is required rather than
// Strict: the callback arrives as a cross-site navigation from the provider,
// and Strict would withhold the cookie exactly when it is needed.
//
// It is named like the session cookie beside it, so the __Host- prefix appears
// only when the cookie is Secure — see cookiename.go.
func (handlers *AdminHandlers) oidcFlowCookie() string {
	return cookieName(adminOIDCCookieBase, handlers.cookieSecure)
}

type oidcFlowState struct {
	Slug     string `json:"slug"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
}

// mountOIDCPublic registers the sign-in half of the flow, which by definition
// runs before anyone is authenticated.
func (handlers *AdminHandlers) mountOIDCPublic(panel chi.Router) {
	panel.Get("/auth/oidc", handlers.listEnabledOIDC)
	panel.Get("/auth/oidc/{slug}/start", handlers.startOIDC)
	panel.Get("/auth/oidc/{slug}/callback", handlers.callbackOIDC)
}

// mountOIDCSecure registers provider configuration, which is an installation
// setting and gated like every other one.
func (handlers *AdminHandlers) mountOIDCSecure(secure chi.Router) {
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/oidc", handlers.listOIDCProviders)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Put("/settings/oidc", handlers.saveOIDCProvider)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Delete("/settings/oidc/{slug}", handlers.deleteOIDCProvider)
}

func (handlers *AdminHandlers) listEnabledOIDC(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	providers, err := handlers.service.ListEnabledOIDCProviders(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "oidc_unavailable", "Providers are unavailable")
		return
	}
	// The sign-in screen only needs enough to render a button.
	items := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		items = append(items, map[string]any{"slug": provider.Slug, "displayName": provider.DisplayName})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) startOIDC(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	slug := chi.URLParam(request, "slug")
	flow, err := handlers.service.BeginOIDC(request.Context(), slug, handlers.oidcRedirectURL(request, slug))
	if errors.Is(err, adminauthpg.ErrOIDCDisabled) {
		writeProblem(writer, request, http.StatusNotFound, "oidc_disabled", "That provider is not available")
		return
	}
	if err != nil {
		handlers.logger.Error("OIDC flow could not start", "slug", slug, "error", err)
		writeProblem(writer, request, http.StatusBadGateway, "oidc_unavailable", "The identity provider did not respond")
		return
	}

	sealed, err := handlers.sealFlow(oidcFlowState{
		Slug: slug, State: flow.State, Verifier: flow.Verifier,
		Next: safeNextPath(request.URL.Query().Get("next")),
	})
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "oidc_unavailable", "Sign-in could not start")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: handlers.oidcFlowCookie(), Value: sealed, Path: "/",
		Expires:  time.Now().Add(adminauthpg.OIDCFlowTTL).UTC(),
		HttpOnly: true, Secure: handlers.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(writer, request, flow.AuthorizationURL, http.StatusFound)
}

func (handlers *AdminHandlers) callbackOIDC(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	slug := chi.URLParam(request, "slug")
	cookie, err := request.Cookie(handlers.oidcFlowCookie())
	if err != nil || cookie.Value == "" {
		handlers.redirectToLogin(writer, request, "oidc_expired")
		return
	}
	// The flow cookie is single-use; clearing it first means a replayed callback
	// cannot be processed twice even if the exchange below succeeds.
	handlers.clearFlowCookie(writer)

	flow, err := handlers.openFlow(cookie.Value)
	if err != nil || flow.Slug != slug {
		handlers.redirectToLogin(writer, request, "oidc_expired")
		return
	}

	query := request.URL.Query()
	if providerError := query.Get("error"); providerError != "" {
		handlers.redirectToLogin(writer, request, "oidc_denied")
		return
	}
	// Comparing the returned state against the one we issued is what stops an
	// attacker from feeding their own authorization code into this session.
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(flow.State)) != 1 {
		handlers.redirectToLogin(writer, request, "oidc_state_mismatch")
		return
	}

	result, err := handlers.service.CompleteOIDC(
		request.Context(), slug, query.Get("code"), flow.Verifier,
		handlers.oidcRedirectURL(request, slug), handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrOIDCUnverifiedEmail):
		handlers.redirectToLogin(writer, request, "oidc_unverified_email")
		return
	case errors.Is(err, adminauthpg.ErrOIDCNoAccount):
		handlers.redirectToLogin(writer, request, "oidc_no_account")
		return
	case errors.Is(err, adminauthpg.ErrAccountDisabled):
		handlers.redirectToLogin(writer, request, "oidc_account_inactive")
		return
	case err != nil:
		handlers.logger.Warn("OIDC sign-in failed", "slug", slug, "error", err)
		handlers.redirectToLogin(writer, request, "oidc_failed")
		return
	}

	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	next := flow.Next
	if next == "" {
		next = "/admin"
	}
	http.Redirect(writer, request, next, http.StatusFound)
}

func (handlers *AdminHandlers) listOIDCProviders(writer http.ResponseWriter, request *http.Request) {
	providers, err := handlers.service.ListOIDCProviders(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "oidc_unavailable", "Providers are unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": providers})
}

func (handlers *AdminHandlers) saveOIDCProvider(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Slug                 string   `json:"slug"`
		DisplayName          string   `json:"displayName"`
		Issuer               string   `json:"issuer"`
		DiscoveryURL         string   `json:"discoveryUrl"`
		ClientID             string   `json:"clientId"`
		ClientSecret         string   `json:"clientSecret"`
		Scopes               []string `json:"scopes"`
		Enabled              bool     `json:"enabled"`
		RequireVerifiedEmail bool     `json:"requireVerifiedEmail"`
		AllowAutoProvision   bool     `json:"allowAutoProvision"`
		AutoProvisionRole    string   `json:"autoProvisionRole"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	provider, err := handlers.service.SaveOIDCProvider(request.Context(), adminauthpg.SaveOIDCProviderInput{
		Slug: body.Slug, DisplayName: body.DisplayName, Issuer: body.Issuer,
		DiscoveryURL: body.DiscoveryURL, ClientID: body.ClientID, ClientSecret: body.ClientSecret,
		Scopes: body.Scopes, Enabled: body.Enabled,
		RequireVerifiedEmail: body.RequireVerifiedEmail,
		AllowAutoProvision:   body.AllowAutoProvision, AutoProvisionRole: body.AutoProvisionRole,
	}, principal.Account.ID, handlers.requestContext(request))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "oidc_rejected", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, provider)
}

func (handlers *AdminHandlers) deleteOIDCProvider(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	if err := handlers.service.DeleteOIDCProvider(
		request.Context(), chi.URLParam(request, "slug"), principal.Account.ID,
		handlers.requestContext(request),
	); err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "oidc_unavailable", "Provider could not be removed")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Flow cookie
// ---------------------------------------------------------------------------

// sealFlow encrypts the flow state with the same AEAD that protects TOTP
// secrets, so a browser holding the cookie cannot read or forge its contents.
func (handlers *AdminHandlers) sealFlow(state oidcFlowState) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sealed, err := handlers.service.SealFlowState(encoded)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (handlers *AdminHandlers) openFlow(value string) (oidcFlowState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return oidcFlowState{}, err
	}
	opened, err := handlers.service.OpenFlowState(raw)
	if err != nil {
		return oidcFlowState{}, err
	}
	var state oidcFlowState
	if err = json.Unmarshal(opened, &state); err != nil {
		return oidcFlowState{}, err
	}
	return state, nil
}

func (handlers *AdminHandlers) clearFlowCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name: handlers.oidcFlowCookie(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: handlers.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// oidcRedirectURL rebuilds the callback URL. It must match byte for byte
// between the authorization request and the token exchange, which is why it is
// derived in one place rather than configured twice.
func (handlers *AdminHandlers) oidcRedirectURL(request *http.Request, slug string) string {
	scheme := "https"
	// Only a deployment that has explicitly turned off cookie security is
	// expected to be reachable over plain HTTP.
	if !handlers.cookieSecure && request.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + request.Host + "/v1/panel/auth/oidc/" + slug + "/callback"
}

func (handlers *AdminHandlers) redirectToLogin(writer http.ResponseWriter, request *http.Request, reason string) {
	http.Redirect(writer, request, "/admin/login?error="+reason, http.StatusFound)
}

// safeNextPath accepts only a same-site absolute path, so a crafted `next`
// cannot turn the callback into an open redirect.
func safeNextPath(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	return next
}
