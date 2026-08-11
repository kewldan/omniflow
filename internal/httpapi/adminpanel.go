package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// AdminHandlers serves the operator panel API.
//
// It is mounted at /v1/panel, separately from the token-authenticated
// /v1/admin surface that the Telegram operator tooling uses. The two have
// different trust models — a session cookie with CSRF and RBAC here, a shared
// bearer token there — and collapsing them onto one prefix would mean one of
// the two silently inherits the other's middleware.
type AdminHandlers struct {
	service *adminauthpg.Service
	limiter *platform.RateLimiter
	logger  *slog.Logger
	proxies *TrustedProxies

	// operations serves the v0.7 surfaces. A nil value leaves them unmounted,
	// which is what a panel running only the v0.6 foundation gets.
	operations *panelpg.Service
	// adapterRecurring is what each compiled-in payment adapter declares about
	// storing a payment method. It is computed at construction so the panel and
	// the enforcement path read the same fact.
	adapterRecurring map[string]bool
	// providers are the compiled-in payment adapters. The panel uses them only
	// to probe credentials; it never creates a payment through them.
	providers map[string]payments.Provider
	// health runs the same dependency probes the readiness endpoint uses, so
	// the panel and /readyz can never disagree about whether a dependency is up.
	health *platform.Health
	// fulfillment queues an operator's subscription change through the same
	// pipeline a purchase uses, so it carries the same idempotency key, retry
	// policy, and history.
	fulfillment *fulfillment.Service
	// remnawave answers the device questions Omniflow does not store. A nil
	// value leaves the device routes unmounted.
	remnawave *remnawave.Client
	// customerAuth manages the customer panel's sign-in providers. A nil value
	// leaves those settings routes unmounted, which is what an installation
	// running no customer panel gets.
	customerAuth *customerauthpg.Service

	// version is the running build, published in diagnostics and in the
	// telemetry preview so an operator can see what would be sent.
	version string

	cookieName   string
	cookieSecure bool
	cookiePath   string
	issuer       string
}

// AdminOptions configures the panel API.
type AdminOptions struct {
	Service *adminauthpg.Service
	Limiter *platform.RateLimiter
	Logger  *slog.Logger
	Proxies *TrustedProxies
	// Operations serves the day-to-day operator surfaces.
	Operations *panelpg.Service
	// Providers are the configured payment adapters, used only to publish their
	// declared capabilities.
	Providers map[string]payments.Provider
	// Health is the same dependency registry the readiness probe reports on.
	Health *platform.Health
	// Fulfillment queues subscription changes. A nil value leaves those routes
	// unmounted.
	Fulfillment *fulfillment.Service
	// Remnawave answers device queries. A nil value leaves those routes
	// unmounted.
	Remnawave *remnawave.Client
	// CookieSecure must be true in production. It is separately configurable
	// only so a plain-HTTP local development stack can sign in at all.
	CookieSecure bool
	// Issuer labels the account inside an authenticator app.
	Issuer string
	// Version is the running build. It appears in the diagnostics bundle and in
	// the telemetry preview, so an empty value renders as "unknown" rather than
	// as a version somebody might quote back.
	Version string
}

// versionOrUnknown renders an unset build version as something an operator will
// not quote back as a real one.
func versionOrUnknown(version string) string {
	if strings.TrimSpace(version) == "" {
		return "unknown"
	}
	return version
}

// NewAdminHandlers builds the panel API.
func NewAdminHandlers(options AdminOptions) *AdminHandlers {
	issuer := options.Issuer
	if issuer == "" {
		issuer = "Omniflow"
	}
	proxies := options.Proxies
	if proxies == nil {
		proxies = &TrustedProxies{}
	}
	return &AdminHandlers{
		service:          options.Service,
		limiter:          options.Limiter,
		logger:           options.Logger,
		proxies:          proxies,
		operations:       options.Operations,
		adapterRecurring: adapterCapabilities(options.Providers),
		providers:        options.Providers,
		health:           options.Health,
		fulfillment:      options.Fulfillment,
		remnawave:        options.Remnawave,
		version:          versionOrUnknown(options.Version),
		// The __Host- prefix binds the cookie to this exact origin: a browser
		// refuses to accept it with a Domain attribute or over plain HTTP, so
		// a sibling subdomain cannot set or overwrite the operator's session.
		cookieName:   "__Host-omniflow_admin",
		cookieSecure: options.CookieSecure,
		cookiePath:   "/",
		issuer:       issuer,
	}
}

// Mount registers the panel routes.
func (handlers *AdminHandlers) Mount(router chi.Router) {
	router.Route("/v1/panel", func(panel chi.Router) {
		panel.Use(SecurityHeaders)

		// Unauthenticated: bootstrap, sign-in, and password reset.
		panel.Get("/bootstrap", handlers.bootstrapStatus)
		panel.Post("/bootstrap", handlers.completeBootstrap)
		panel.Post("/auth/login", handlers.login)
		panel.Post("/auth/challenge", handlers.challenge)
		panel.Post("/auth/password-reset", handlers.requestPasswordReset)
		panel.Post("/auth/password-reset/complete", handlers.completePasswordReset)
		handlers.mountOIDCPublic(panel)

		// Authenticated.
		panel.Group(func(secure chi.Router) {
			secure.Use(handlers.requireSession)
			secure.Use(handlers.requireCSRF)

			secure.Get("/auth/session", handlers.currentSession)
			secure.Post("/auth/logout", handlers.logout)
			secure.Post("/auth/logout-all", handlers.logoutAll)
			secure.Patch("/auth/profile", handlers.updateProfile)
			secure.Put("/auth/preferences", handlers.savePreferences)
			secure.Post("/auth/password", handlers.changePassword)

			secure.Get("/auth/sessions", handlers.listSessions)
			secure.Delete("/auth/sessions/{sessionID}", handlers.revokeSession)

			secure.Post("/auth/totp", handlers.beginTOTP)
			secure.Post("/auth/totp/confirm", handlers.confirmTOTP)
			secure.Delete("/auth/totp", handlers.disableTOTP)
			secure.Post("/auth/recovery-codes", handlers.regenerateRecoveryCodes)

			secure.With(handlers.requirePermission(rbac.PermissionAdminsRead)).
				Get("/admins", handlers.listAdmins)
			secure.With(handlers.requirePermission(rbac.PermissionAdminsRead)).
				Get("/admins/{adminID}", handlers.getAdmin)
			secure.With(handlers.requirePermission(rbac.PermissionAdminsWrite)).
				Post("/admins", handlers.createAdmin)
			secure.With(handlers.requirePermission(rbac.PermissionAdminsWrite)).
				Post("/admins/{adminID}/status", handlers.setAdminStatus)
			secure.With(handlers.requirePermission(rbac.PermissionAdminsRoles)).
				Put("/admins/{adminID}/roles", handlers.setAdminRoles)

			secure.With(handlers.requirePermission(rbac.PermissionAuditRead)).
				Get("/audit", handlers.searchAudit)
			secure.With(handlers.requirePermission(rbac.PermissionAuditRead)).
				Get("/audit/actions", handlers.auditActions)
			secure.With(handlers.requirePermission(rbac.PermissionAuditExport)).
				Get("/audit/export", handlers.exportAudit)

			secure.Get("/rbac/catalog", handlers.permissionCatalog)
			handlers.mountOIDCSecure(secure)
			handlers.mountOperations(secure)
			handlers.mountSupport(secure)
			handlers.mountLoyalty(secure)
			handlers.mountMarketing(secure)
			handlers.mountSettings(secure)
			handlers.mountCustomerAuth(secure)
		})
	})
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

// ready reports whether an identity service is attached, answering 503 when it
// is not.
//
// The authenticated routes need no such check: requireSession refuses them
// before any handler runs. The routes reachable before sign-in do run, and a
// panel mounted without a service should degrade to "unavailable" rather than
// crash the process on the first unauthenticated request.
func (handlers *AdminHandlers) ready(writer http.ResponseWriter, request *http.Request) bool {
	if handlers.service == nil {
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"panel_unavailable", "The operator panel is not configured",
		)
		return false
	}
	return true
}

func (handlers *AdminHandlers) bootstrapStatus(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	state, err := handlers.service.BootstrapStatus(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "bootstrap_unavailable", "Setup status is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"setupRequired": state.Required})
}

func (handlers *AdminHandlers) completeBootstrap(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		SetupToken  string `json:"setupToken"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
		Locale      string `json:"locale"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	account, err := handlers.service.CompleteBootstrap(
		request.Context(), body.SetupToken, body.Email, body.DisplayName, body.Password, body.Locale,
		handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrBootstrapClosed):
		writeProblem(writer, request, http.StatusConflict, "bootstrap_closed", "Setup has already been completed")
		return
	case errors.Is(err, adminauth.ErrPasswordTooShort), errors.Is(err, adminauth.ErrPasswordTooLong):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case errors.Is(err, adminauthpg.ErrConflict):
		writeProblem(writer, request, http.StatusConflict, "email_in_use", "That address already has an account")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "bootstrap_rejected", "Setup could not be completed")
		return
	}
	writeJSON(writer, http.StatusCreated, accountPayload(account))
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) login(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}

	// Two independent limits: one per address, so a single account cannot be
	// ground down, and one per source address, so a spray across many accounts
	// from one origin is bounded too.
	if !handlers.allow(request, "admin-login-account", adminauthpg.NormalizeEmail(body.Email), 10, time.Minute) ||
		!handlers.allow(request, "admin-login-ip", clientKey(handlers.requestContext(request)), 30, time.Minute) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts")
		return
	}

	result, err := handlers.service.Authenticate(
		request.Context(), body.Email, body.Password, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrAccountLocked):
		writeProblem(writer, request, http.StatusTooManyRequests, "account_locked", "Too many failed attempts. Try again later.")
		return
	case errors.Is(err, adminauthpg.ErrInvalidCredentials),
		errors.Is(err, adminauthpg.ErrAccountDisabled):
		// One shared response for wrong password, unknown address, and a
		// disabled account, so none of them can be told apart.
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_credentials", "Those details are not correct")
		return
	case err != nil:
		handlers.logger.Error("admin sign-in failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "login_unavailable", "Sign-in is unavailable")
		return
	}

	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	writeJSON(writer, http.StatusOK, map[string]any{
		"challengeRequired": result.ChallengeRequired,
		"csrfToken":         result.CSRFToken,
		"expiresAt":         result.ExpiresAt.UTC(),
		"account":           accountPayload(result.Account),
	})
}

func (handlers *AdminHandlers) challenge(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	cookie, err := request.Cookie(handlers.cookieName)
	if err != nil || cookie.Value == "" {
		writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
		return
	}
	if !handlers.allow(request, "admin-challenge", clientKey(handlers.requestContext(request)), 15, time.Minute) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many verification attempts")
		return
	}

	result, err := handlers.service.VerifyChallenge(
		request.Context(), cookie.Value, body.Code, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrSessionInvalid):
		handlers.clearSessionCookie(writer)
		writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign in again")
		return
	case errors.Is(err, adminauthpg.ErrInvalidCredentials):
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_code", "That code is not valid")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "challenge_unavailable", "Verification is unavailable")
		return
	}

	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	writeJSON(writer, http.StatusOK, map[string]any{
		"csrfToken": result.CSRFToken,
		"expiresAt": result.ExpiresAt.UTC(),
		"account":   accountPayload(result.Account),
	})
}

func (handlers *AdminHandlers) currentSession(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	remaining, err := handlers.service.RemainingRecoveryCodes(request.Context(), principal.Account.ID)
	if err != nil {
		remaining = 0
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"account":                accountPayload(principal.Account),
		"permissions":            permissionStrings(principal.Grant.Permissions()),
		"csrfToken":              principal.CSRFToken,
		"sessionId":              principal.SessionID,
		"expiresAt":              principal.ExpiresAt.UTC(),
		"remainingRecoveryCodes": remaining,
	})
}

func (handlers *AdminHandlers) logout(writer http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie(handlers.cookieName); err == nil {
		if logoutErr := handlers.service.Logout(request.Context(), cookie.Value, handlers.requestContext(request)); logoutErr != nil {
			writeProblem(writer, request, http.StatusInternalServerError, "logout_failed", "Sign-out failed")
			return
		}
	}
	handlers.clearSessionCookie(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) logoutAll(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	// The calling session is kept so the operator is not signed out of the
	// browser they are using to secure the account.
	revoked, err := handlers.service.LogoutAll(
		request.Context(), principal.Account.ID, principal.SessionID, handlers.requestContext(request),
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "logout_failed", "Sign-out failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"revokedSessions": revoked})
}

// ---------------------------------------------------------------------------
// Profile, password, sessions
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) updateProfile(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		DisplayName string `json:"displayName"`
		Locale      string `json:"locale"`
		Timezone    string `json:"timezone"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	account, err := handlers.service.UpdateProfile(
		request.Context(), principal.Account.ID, body.DisplayName, body.Locale, body.Timezone,
		handlers.requestContext(request),
	)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "profile_rejected", "Profile could not be saved")
		return
	}
	writeJSON(writer, http.StatusOK, accountPayload(account))
}

func (handlers *AdminHandlers) savePreferences(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body adminauthpg.Preferences
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.service.SavePreferences(request.Context(), principal.Account.ID, body)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "preferences_failed", "Preferences could not be saved")
		return
	}
	writeJSON(writer, http.StatusOK, saved)
}

func (handlers *AdminHandlers) changePassword(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.service.ChangePassword(
		request.Context(), principal.Account.ID, body.CurrentPassword, body.NewPassword,
		principal.SessionID, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrInvalidCredentials):
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_credentials", "The current password is not correct")
		return
	case errors.Is(err, adminauth.ErrPasswordTooShort), errors.Is(err, adminauth.ErrPasswordTooLong):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "password_change_failed", "Password could not be changed")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) requestPasswordReset(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if !handlers.allow(request, "admin-reset", clientKey(handlers.requestContext(request)), 5, time.Hour) {
		// Even the rate-limit response is uniform, so it cannot be used to
		// probe which addresses exist.
		writer.WriteHeader(http.StatusAccepted)
		return
	}

	token, account, err := handlers.service.RequestPasswordReset(
		request.Context(), body.Email, handlers.requestContext(request),
	)
	if err != nil {
		handlers.logger.Error("password reset request failed", "error", err)
	}
	if token != "" {
		// v0.6 has no operator email transport: delivery arrives with the
		// notification work in v0.7. The token is logged at warn level so a
		// self-hosting operator can complete a reset from the server console,
		// and never returned in the response body.
		handlers.logger.Warn(
			"admin password reset issued; deliver this token out of band",
			"adminUserId", account.ID, "token", token,
		)
	}
	// Always the same answer, whether or not the address matched an account.
	writer.WriteHeader(http.StatusAccepted)
}

func (handlers *AdminHandlers) completePasswordReset(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.service.CompletePasswordReset(
		request.Context(), body.Token, body.NewPassword, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrInvalidCredentials):
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_token", "That reset link is not valid")
		return
	case errors.Is(err, adminauth.ErrPasswordTooShort), errors.Is(err, adminauth.ErrPasswordTooLong):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "reset_failed", "Password could not be reset")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) listSessions(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	sessions, err := handlers.service.ListSessions(request.Context(), principal.Account.ID, principal.SessionID)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "sessions_unavailable", "Sessions are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		item := map[string]any{
			"id":         session.ID,
			"current":    session.Current,
			"userAgent":  session.UserAgent,
			"createdAt":  session.CreatedAt.UTC(),
			"lastSeenAt": session.LastSeenAt.UTC(),
			"expiresAt":  session.ExpiresAt.UTC(),
			"methods":    session.Methods,
		}
		if session.IP != nil {
			item["ip"] = session.IP.String()
		}
		items = append(items, item)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) revokeSession(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	err := handlers.service.RevokeSession(
		request.Context(), principal.Account.ID, chi.URLParam(request, "sessionID"),
		handlers.requestContext(request),
	)
	if errors.Is(err, adminauthpg.ErrNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "session_not_found", "Session not found")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "session_revoke_failed", "Session could not be ended")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Two-factor authentication
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) beginTOTP(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	enrolment, err := handlers.service.BeginTOTPEnrolment(
		request.Context(), principal.Account.ID, handlers.issuer, handlers.requestContext(request),
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "totp_unavailable", "Enrolment could not be started")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"secret": enrolment.Secret, "uri": enrolment.URI})
}

func (handlers *AdminHandlers) confirmTOTP(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	codes, err := handlers.service.ConfirmTOTPEnrolment(
		request.Context(), principal.Account.ID, body.Code, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrInvalidCredentials):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_code", "That code is not valid")
		return
	case errors.Is(err, adminauthpg.ErrNotFound):
		writeProblem(writer, request, http.StatusConflict, "totp_not_started", "Start enrolment first")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "totp_unavailable", "Enrolment could not be completed")
		return
	}
	// The only time these are ever returned.
	writeJSON(writer, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

func (handlers *AdminHandlers) disableTOTP(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// Removing a second factor is exactly what an attacker on a hijacked
	// session would try, so the password is re-proven first. This is a
	// read-only check: it must not disturb the account it is protecting.
	verified, err := handlers.service.VerifyPassword(request.Context(), principal.Account.ID, body.Password)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "totp_unavailable", "Two-factor could not be disabled")
		return
	}
	if !verified {
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_credentials", "That password is not correct")
		return
	}
	if err := handlers.service.DisableTOTP(
		request.Context(), principal.Account.ID, principal.Account.ID, handlers.requestContext(request),
	); err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "totp_unavailable", "Two-factor could not be disabled")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) regenerateRecoveryCodes(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	codes, err := handlers.service.RegenerateRecoveryCodes(
		request.Context(), principal.Account.ID, handlers.requestContext(request),
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "recovery_unavailable", "Recovery codes could not be issued")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// ---------------------------------------------------------------------------
// Operator accounts
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) listAdmins(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	pageSize, _ := strconv.Atoi(query.Get("pageSize"))
	page, err := handlers.service.ListAccounts(
		request.Context(), query.Get("status"), query.Get("cursor"), int32(pageSize),
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "admins_unavailable", "Operators are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(page.Accounts))
	for _, account := range page.Accounts {
		items = append(items, accountPayload(account))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}

func (handlers *AdminHandlers) getAdmin(writer http.ResponseWriter, request *http.Request) {
	account, err := handlers.service.GetAccount(request.Context(), chi.URLParam(request, "adminID"))
	if errors.Is(err, adminauthpg.ErrNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "admin_not_found", "Operator not found")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "admins_unavailable", "Operator is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, accountPayload(account))
}

func (handlers *AdminHandlers) createAdmin(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Email       string   `json:"email"`
		DisplayName string   `json:"displayName"`
		Password    string   `json:"password"`
		Locale      string   `json:"locale"`
		Timezone    string   `json:"timezone"`
		Roles       []string `json:"roles"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	roles, err := parseRoles(body.Roles)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "unknown_role", err.Error())
		return
	}
	// Only an owner may mint another owner, mirroring the same guard on the
	// role-change path.
	if containsRole(roles, rbac.RoleOwner) && !principal.Grant.HasRole(rbac.RoleOwner) {
		writeProblem(writer, request, http.StatusForbidden, "forbidden", "Only an owner may grant the owner role")
		return
	}

	account, err := handlers.service.CreateAccount(request.Context(), adminauthpg.CreateAccountInput{
		Email: body.Email, DisplayName: body.DisplayName, Password: body.Password,
		Locale: body.Locale, Timezone: body.Timezone, Roles: roles,
	}, principal.Account.ID, handlers.requestContext(request))
	switch {
	case errors.Is(err, adminauthpg.ErrConflict):
		writeProblem(writer, request, http.StatusConflict, "email_in_use", "That address already has an account")
		return
	case errors.Is(err, adminauth.ErrPasswordTooShort), errors.Is(err, adminauth.ErrPasswordTooLong):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case err != nil:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "admin_rejected", "Operator could not be created")
		return
	}
	writeJSON(writer, http.StatusCreated, accountPayload(account))
}

func (handlers *AdminHandlers) setAdminRoles(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Roles []string `json:"roles"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	roles, err := parseRoles(body.Roles)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "unknown_role", err.Error())
		return
	}
	account, err := handlers.service.SetRoles(
		request.Context(), chi.URLParam(request, "adminID"), roles, principal, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrLastOwner):
		writeProblem(writer, request, http.StatusConflict, "last_owner", "The installation must keep at least one active owner")
		return
	case errors.Is(err, adminauthpg.ErrForbidden):
		writeProblem(writer, request, http.StatusForbidden, "forbidden", "Only an owner may grant the owner role")
		return
	case errors.Is(err, adminauthpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "admin_not_found", "Operator not found")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "roles_unavailable", "Roles could not be changed")
		return
	}
	writeJSON(writer, http.StatusOK, accountPayload(account))
}

func (handlers *AdminHandlers) setAdminStatus(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Status string `json:"status"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	account, err := handlers.service.SetStatus(
		request.Context(), chi.URLParam(request, "adminID"), body.Status, principal.Account.ID,
		handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrLastOwner):
		writeProblem(writer, request, http.StatusConflict, "last_owner", "The installation must keep at least one active owner")
		return
	case errors.Is(err, adminauthpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "admin_not_found", "Operator not found")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "status_rejected", "Status could not be changed")
		return
	}
	writeJSON(writer, http.StatusOK, accountPayload(account))
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) searchAudit(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.service.SearchAudit(request.Context(), auditFilterFrom(request))
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "audit_unavailable", "The audit trail is unavailable")
		return
	}
	items := make([]map[string]any, 0, len(page.Events))
	for _, event := range page.Events {
		items = append(items, auditPayload(event))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}

func (handlers *AdminHandlers) auditActions(writer http.ResponseWriter, request *http.Request) {
	actions, err := handlers.service.AuditActions(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "audit_unavailable", "The audit trail is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": actions})
}

// exportAudit streams the filtered trail as CSV.
//
// The export carries the same columns the browser shows and no others; nothing
// in this package writes a secret into an audit row, so there is no redaction
// step that the export could skip.
func (handlers *AdminHandlers) exportAudit(writer http.ResponseWriter, request *http.Request) {
	filter := auditFilterFrom(request)
	filter.PageSize = 200

	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="audit-events.csv"`)
	writer.WriteHeader(http.StatusOK)

	_, _ = writer.Write([]byte("occurred_at,category,outcome,action,actor_type,actor_id,target_type,target_id,reason,request_id\n"))

	// Bounded so a single export cannot walk an unbounded trail forever; the
	// operator narrows the filter to reach older rows.
	const maxPages = 50
	for page := 0; page < maxPages; page++ {
		result, err := handlers.service.SearchAudit(request.Context(), filter)
		if err != nil {
			handlers.logger.Error("audit export failed", "error", err)
			return
		}
		for _, event := range result.Events {
			_, _ = writer.Write([]byte(strings.Join([]string{
				csvField(event.OccurredAt.UTC().Format(time.RFC3339)),
				csvField(event.Category), csvField(event.Outcome), csvField(event.Action),
				csvField(event.ActorType), csvField(event.ActorID),
				csvField(event.TargetType), csvField(event.TargetID),
				csvField(event.Reason), csvField(event.RequestID),
			}, ",") + "\n"))
		}
		if result.NextCursor == "" {
			return
		}
		filter.Cursor = result.NextCursor
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (handlers *AdminHandlers) permissionCatalog(writer http.ResponseWriter, request *http.Request) {
	roles := make([]map[string]any, 0, len(rbac.AllRoles))
	for _, role := range rbac.AllRoles {
		roles = append(roles, map[string]any{
			"role":        string(role),
			"permissions": permissionStrings(rbac.PermissionsFor(role)),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"permissions": permissionStrings(rbac.AllPermissions),
		"roles":       roles,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) setSessionCookie(writer http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(writer, &http.Cookie{
		Name:     handlers.cookieName,
		Value:    token,
		Path:     handlers.cookiePath,
		Expires:  expires.UTC(),
		HttpOnly: true,
		Secure:   handlers.cookieSecure,
		// Lax rather than Strict so that following a link into the panel from
		// an email or chat still arrives signed in; the CSRF token, not the
		// cookie policy, is what actually defends the unsafe methods.
		SameSite: http.SameSiteLaxMode,
	})
}

func (handlers *AdminHandlers) clearSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name:     handlers.cookieName,
		Value:    "",
		Path:     handlers.cookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   handlers.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// allow applies a rate limit, failing closed when the limiter is unreachable so
// an outage cannot silently remove the protection.
func (handlers *AdminHandlers) allow(
	request *http.Request, scope, subject string, limit int64, window time.Duration,
) bool {
	if handlers.limiter == nil || subject == "" {
		return true
	}
	allowed, err := handlers.limiter.Allow(request.Context(), scope, subject, limit, window)
	if err != nil {
		handlers.logger.Warn("admin rate limiter unavailable", "scope", scope, "error", err)
		return false
	}
	return allowed
}

func clientKey(request adminauthpg.RequestContext) string {
	if request.IP == nil {
		return "unknown"
	}
	return request.IP.String()
}

func accountPayload(account adminauthpg.Account) map[string]any {
	payload := map[string]any{
		"id":          account.ID,
		"email":       account.Email,
		"displayName": account.DisplayName,
		"status":      account.Status,
		"locale":      account.Locale,
		"timezone":    account.Timezone,
		"roles":       roleStrings(account.Roles),
		"totpEnabled": account.TOTPEnabled,
		"createdAt":   account.CreatedAt.UTC(),
		"preferences": account.Preferences,
	}
	if account.LastLoginAt != nil {
		payload["lastLoginAt"] = account.LastLoginAt.UTC()
	}
	return payload
}

func auditPayload(event adminauthpg.AuditEvent) map[string]any {
	return map[string]any{
		"id":         event.ID,
		"occurredAt": event.OccurredAt.UTC(),
		"actorType":  event.ActorType,
		"actorId":    event.ActorID,
		"action":     event.Action,
		"category":   event.Category,
		"outcome":    event.Outcome,
		"targetType": event.TargetType,
		"targetId":   event.TargetID,
		"reason":     event.Reason,
		"requestId":  event.RequestID,
		"metadata":   event.Metadata,
	}
}

func auditFilterFrom(request *http.Request) adminauthpg.AuditFilter {
	query := request.URL.Query()
	pageSize, _ := strconv.Atoi(query.Get("pageSize"))
	filter := adminauthpg.AuditFilter{
		Category:   query.Get("category"),
		Outcome:    query.Get("outcome"),
		ActorType:  query.Get("actorType"),
		ActorID:    query.Get("actorId"),
		Action:     query.Get("action"),
		TargetType: query.Get("targetType"),
		TargetID:   query.Get("targetId"),
		Cursor:     query.Get("cursor"),
		PageSize:   int32(pageSize),
		// Any value other than an explicit "asc" keeps the newest-first default,
		// which is the ordering an operator reviewing recent activity wants.
		Ascending: query.Get("sort") == "asc",
	}
	if from, err := time.Parse(time.RFC3339, query.Get("from")); err == nil {
		filter.From = &from
	}
	if to, err := time.Parse(time.RFC3339, query.Get("to")); err == nil {
		filter.To = &to
	}
	return filter
}

func parseRoles(values []string) ([]rbac.Role, error) {
	roles := make([]rbac.Role, 0, len(values))
	for _, value := range values {
		role, err := rbac.ParseRole(value)
		if err != nil {
			return nil, err
		}
		if !containsRole(roles, role) {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

func containsRole(roles []rbac.Role, target rbac.Role) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func roleStrings(roles []rbac.Role) []string {
	values := make([]string, 0, len(roles))
	for _, role := range roles {
		values = append(values, string(role))
	}
	return values
}

func permissionStrings(permissions []rbac.Permission) []string {
	values := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		values = append(values, string(permission))
	}
	return values
}

// csvField quotes a CSV value. Doubling an embedded quote is what RFC 4180
// requires, and a leading formula character is prefixed so a spreadsheet does
// not evaluate an exported value as a formula.
func csvField(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value[:1], "=+-@\t\r") {
		value = "'" + value
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
