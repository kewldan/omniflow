package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// accountFlowCookie carries the anti-forgery state, the PKCE verifier, and the
// nonce across an OIDC round trip.
//
// It lives in a short-lived, HttpOnly, SameSite=Lax cookie rather than in the
// database, so an abandoned sign-in leaves nothing to clean up. Lax is required
// rather than Strict: the callback arrives as a cross-site navigation from the
// provider, and Strict would withhold the cookie exactly when it is needed.
const accountFlowCookie = "__Host-omniflow_account_oidc"

type accountFlowState struct {
	Slug     string `json:"slug"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
	Next     string `json:"next"`
	// LinkTo is the customer this flow will attach the identity to, when it was
	// started from inside a session rather than from the sign-in screen. It is
	// sealed into the cookie rather than read from the session on callback so a
	// link flow can never be completed against a different account than the one
	// that started it.
	LinkTo string `json:"linkTo,omitempty"`
}

// signInMethods reports which routes this installation actually offers.
//
// The sign-in screen renders from this rather than from build-time knowledge, so
// an installation with no bot token does not show a Telegram button that cannot
// work, and one with three OIDC providers shows three.
func (handlers *AccountHandlers) signInMethods(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	providers, err := handlers.auth.ListEnabledOIDCButtons(request.Context())
	if err != nil {
		handlers.logger.Error("customer sign-in providers unavailable", "error", err)
		writeProblem(
			writer, request, http.StatusInternalServerError,
			"account_unavailable", "Sign-in methods are unavailable",
		)
		return
	}
	// The widget is loaded by username, so a token without a resolvable username
	// cannot render one. Reporting `telegram: false` there is honest: the route
	// genuinely is not available, and showing a button that never appears would
	// leave the customer staring at a blank space.
	botUsername := handlers.auth.BotUsername(request.Context())
	writeJSON(writer, http.StatusOK, map[string]any{
		"telegram":    botUsername != "",
		"telegramBot": botUsername,
		"magicLink":   handlers.auth.MagicLinkEnabled(),
		"oidc":        providers,
	})
}

// signInWithTelegram accepts a Login Widget payload.
func (handlers *AccountHandlers) signInWithTelegram(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body map[string]string
	if !decodeJSON(writer, request, &body) {
		return
	}
	values := url.Values{}
	for key, value := range body {
		values.Set(key, value)
	}
	result, err := handlers.auth.SignInWithTelegram(request.Context(), values, handlers.requestContext(request))
	handlers.finishSignIn(writer, request, result, err)
}

// signInWithMiniApp accepts the initData a Telegram Mini App holds.
func (handlers *AccountHandlers) signInWithMiniApp(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		InitData string `json:"initData"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	result, err := handlers.auth.SignInWithMiniApp(
		request.Context(), body.InitData, handlers.requestContext(request),
	)
	handlers.finishSignIn(writer, request, result, err)
}

// finishSignIn maps a sign-in outcome onto a cookie and a response.
//
// Every failure that is not an operational error answers the same way: the
// installation must not become an oracle for which Telegram accounts or which
// external identities are known to it.
func (handlers *AccountHandlers) finishSignIn(
	writer http.ResponseWriter, request *http.Request,
	result customerauthpg.SignInResult, err error,
) {
	switch {
	case errors.Is(err, customerauthpg.ErrTelegramUnset):
		writeProblem(
			writer, request, http.StatusNotFound,
			"method_unavailable", "That sign-in method is not available",
		)
		return
	case errors.Is(err, customerauthpg.ErrAccountInactive):
		writeProblem(
			writer, request, http.StatusForbidden,
			"account_unavailable", "This account is not available",
		)
		return
	case errors.Is(err, customerauthpg.ErrSignInRejected):
		writeProblem(writer, request, http.StatusUnauthorized, "sign_in_rejected", "Sign-in could not be completed")
		return
	case err != nil:
		handlers.logger.Error("customer sign-in failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "sign_in_unavailable", "Sign-in is unavailable")
		return
	}

	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	writeJSON(writer, http.StatusOK, map[string]any{
		"customer": map[string]any{
			"id": result.Customer.ID, "locale": result.Customer.Locale,
			"timezone": result.Customer.Timezone, "status": result.Customer.Status,
		},
		"expiresAt": result.ExpiresAt.Format(time.RFC3339),
	})
}

// completeMagicLink redeems a link the bot delivered.
//
// It is a GET that redirects rather than a JSON endpoint because the customer
// arrives by tapping a link in a chat. The token is consumed before the redirect
// is issued, so the URL left behind in history is already spent.
func (handlers *AccountHandlers) completeMagicLink(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	token := request.URL.Query().Get("token")
	if token == "" {
		handlers.redirectToSignIn(writer, request, "link_invalid")
		return
	}
	result, err := handlers.auth.CompleteMagicLink(request.Context(), token, handlers.requestContext(request))
	switch {
	case errors.Is(err, customerauth.ErrMagicLinkUnavailable):
		handlers.redirectToSignIn(writer, request, "method_unavailable")
		return
	case errors.Is(err, customerauth.ErrMagicLinkInvalid):
		handlers.redirectToSignIn(writer, request, "link_invalid")
		return
	case errors.Is(err, customerauthpg.ErrAccountInactive):
		handlers.redirectToSignIn(writer, request, "account_unavailable")
		return
	case err != nil:
		handlers.logger.Error("magic-link sign-in failed", "error", err)
		handlers.redirectToSignIn(writer, request, "sign_in_failed")
		return
	}
	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	http.Redirect(writer, request, "/account", http.StatusFound)
}

// ---------------------------------------------------------------------------
// OIDC
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) startOIDC(writer http.ResponseWriter, request *http.Request) {
	handlers.beginOIDCFlow(writer, request, "")
}

// startOIDCLink begins the same flow from inside a session, to attach a provider
// to the account the customer already holds.
func (handlers *AccountHandlers) startOIDCLink(writer http.ResponseWriter, request *http.Request) {
	principal, ok := CustomerFrom(request.Context())
	if !ok {
		writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
		return
	}
	handlers.beginOIDCFlow(writer, request, principal.Customer.ID)
}

func (handlers *AccountHandlers) beginOIDCFlow(
	writer http.ResponseWriter, request *http.Request, linkTo string,
) {
	if !handlers.ready(writer, request) {
		return
	}
	slug := chi.URLParam(request, "slug")
	flow, err := handlers.auth.BeginOIDC(request.Context(), slug, handlers.oidcRedirectURL(request, slug))
	if errors.Is(err, customerauthpg.ErrOIDCDisabled) {
		writeProblem(writer, request, http.StatusNotFound, "method_unavailable", "That provider is not available")
		return
	}
	if err != nil {
		handlers.logger.Error("customer OIDC flow could not start", "slug", slug, "error", err)
		writeProblem(writer, request, http.StatusBadGateway, "provider_unavailable", "The provider did not respond")
		return
	}

	sealed, err := handlers.sealAccountFlow(accountFlowState{
		Slug: slug, State: flow.State, Verifier: flow.Verifier, Nonce: flow.Nonce,
		Next: safeNextPath(request.URL.Query().Get("next")), LinkTo: linkTo,
	})
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "sign_in_unavailable", "Sign-in could not start")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: accountFlowCookie, Value: sealed, Path: "/",
		Expires:  time.Now().Add(customerauthpg.OIDCFlowTTL).UTC(),
		HttpOnly: true, Secure: handlers.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(writer, request, flow.AuthorizationURL, http.StatusFound)
}

func (handlers *AccountHandlers) callbackOIDC(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	slug := chi.URLParam(request, "slug")
	cookie, err := request.Cookie(accountFlowCookie)
	if err != nil || cookie.Value == "" {
		handlers.redirectToSignIn(writer, request, "sign_in_expired")
		return
	}
	// The flow cookie is single-use; clearing it first means a replayed callback
	// cannot be processed twice even if the exchange below succeeds.
	handlers.clearAccountFlowCookie(writer)

	flow, err := handlers.openAccountFlow(cookie.Value)
	if err != nil || flow.Slug != slug {
		handlers.redirectToSignIn(writer, request, "sign_in_expired")
		return
	}
	query := request.URL.Query()
	if query.Get("error") != "" {
		handlers.redirectToSignIn(writer, request, "sign_in_denied")
		return
	}
	// Comparing the returned state against the one we issued is what stops an
	// attacker feeding their own authorization code into this browser.
	if query.Get("state") == "" || query.Get("state") != flow.State {
		handlers.redirectToSignIn(writer, request, "sign_in_expired")
		return
	}

	redirectURL := handlers.oidcRedirectURL(request, slug)
	if flow.LinkTo != "" {
		handlers.finishOIDCLink(writer, request, flow, query.Get("code"), redirectURL)
		return
	}

	result, err := handlers.auth.CompleteOIDCSignIn(
		request.Context(), slug, query.Get("code"), flow.Verifier, flow.Nonce, redirectURL,
		handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, customerauth.ErrUnverifiedEmail):
		handlers.redirectToSignIn(writer, request, "unverified_email")
		return
	case errors.Is(err, customerauthpg.ErrAccountInactive):
		handlers.redirectToSignIn(writer, request, "account_unavailable")
		return
	case err != nil:
		handlers.logger.Warn("customer OIDC sign-in failed", "slug", slug, "error", err)
		handlers.redirectToSignIn(writer, request, "sign_in_failed")
		return
	}

	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	next := flow.Next
	if next == "" {
		next = "/account"
	}
	http.Redirect(writer, request, next, http.StatusFound)
}

// finishOIDCLink completes a flow that was started from inside a session.
func (handlers *AccountHandlers) finishOIDCLink(
	writer http.ResponseWriter, request *http.Request,
	flow accountFlowState, code, redirectURL string,
) {
	err := handlers.auth.LinkOIDCIdentity(
		request.Context(), flow.LinkTo, flow.Slug, code, flow.Verifier, flow.Nonce, redirectURL,
		handlers.requestContext(request),
	)
	destination := "/account/security"
	switch {
	case errors.Is(err, customerauth.ErrIdentityTaken):
		// The refusal says the identity is in use, never whose account it is on.
		destination += "?error=identity_taken"
	case errors.Is(err, customerauth.ErrUnverifiedEmail):
		destination += "?error=unverified_email"
	case err != nil:
		handlers.logger.Warn("customer OIDC link failed", "slug", flow.Slug, "error", err)
		destination += "?error=link_failed"
	default:
		destination += "?linked=" + url.QueryEscape(flow.Slug)
	}
	http.Redirect(writer, request, destination, http.StatusFound)
}

// ---------------------------------------------------------------------------
// Flow cookie
// ---------------------------------------------------------------------------

// sealAccountFlow encrypts the flow state with the installation's data key, so a
// browser holding the cookie can neither read nor forge its contents.
func (handlers *AccountHandlers) sealAccountFlow(state accountFlowState) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sealed, err := handlers.auth.SealFlowState(encoded)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (handlers *AccountHandlers) openAccountFlow(value string) (accountFlowState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return accountFlowState{}, err
	}
	opened, err := handlers.auth.OpenFlowState(raw)
	if err != nil {
		return accountFlowState{}, err
	}
	var state accountFlowState
	if err = json.Unmarshal(opened, &state); err != nil {
		return accountFlowState{}, err
	}
	return state, nil
}

func (handlers *AccountHandlers) clearAccountFlowCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name: accountFlowCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: handlers.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// oidcRedirectURL rebuilds the callback URL. It must match byte for byte between
// the authorization request and the token exchange, which is why it is derived
// in one place rather than configured twice.
func (handlers *AccountHandlers) oidcRedirectURL(request *http.Request, slug string) string {
	scheme := "https"
	// Only a deployment that has explicitly turned off cookie security is
	// expected to be reachable over plain HTTP.
	if !handlers.cookieSecure && request.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + request.Host + "/v1/account/auth/oidc/" + slug + "/callback"
}

func (handlers *AccountHandlers) redirectToSignIn(
	writer http.ResponseWriter, request *http.Request, reason string,
) {
	http.Redirect(writer, request, "/account/sign-in?error="+url.QueryEscape(reason), http.StatusFound)
}

// trimmedQuery reads a query parameter with surrounding whitespace removed.
func trimmedQuery(request *http.Request, key string) string {
	return strings.TrimSpace(request.URL.Query().Get(key))
}
