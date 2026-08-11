package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// The operator surface for customer sign-in providers.
//
// It lives on /v1/panel because configuring who may sign in as a customer is an
// installation setting, gated by the same permission as every other one. The
// customer-facing half of the same registry is on /v1/account and shows only
// what a sign-in screen needs to render a button.

// WithCustomerAuth attaches the customer identity service so the panel can
// manage sign-in providers. A nil service leaves the routes unmounted, which is
// what an installation running no customer panel gets.
func (handlers *AdminHandlers) WithCustomerAuth(service *customerauthpg.Service) *AdminHandlers {
	handlers.customerAuth = service
	return handlers
}

func (handlers *AdminHandlers) mountCustomerAuth(secure chi.Router) {
	if handlers.customerAuth == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/customer-oidc", handlers.listCustomerOIDCProviders)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/customer-oidc/presets", handlers.customerOIDCPresets)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Put("/settings/customer-oidc", handlers.saveCustomerOIDCProvider)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Delete("/settings/customer-oidc/{slug}", handlers.deleteCustomerOIDCProvider)
}

func (handlers *AdminHandlers) listCustomerOIDCProviders(writer http.ResponseWriter, request *http.Request) {
	providers, err := handlers.customerAuth.ListOIDCProviders(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "oidc_unavailable", "Providers are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		items = append(items, map[string]any{
			"slug": provider.Slug, "displayName": provider.DisplayName,
			"issuer": provider.Issuer, "discoveryUrl": provider.DiscoveryURL,
			"clientId": provider.ClientID, "scopes": provider.Scopes,
			"enabled": provider.Enabled, "icon": provider.Icon, "sortOrder": provider.SortOrder,
			"requireVerifiedEmail": provider.RequireVerifiedEmail,
			"allowAutoProvision":   provider.AllowAutoProvision,
			// Whether a secret is held, never the secret itself. The panel needs
			// to render "configured" and to leave the field blank on re-save.
			"hasClientSecret": provider.HasClientSecret,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// customerOIDCPresets serves the shipped starting points.
//
// They are values, not code paths: choosing one fills the same form an operator
// could type by hand. Serving them from the API rather than embedding them in
// the panel bundle keeps one definition, which is also the one the tests assert
// against.
func (handlers *AdminHandlers) customerOIDCPresets(writer http.ResponseWriter, request *http.Request) {
	presets := customerauth.ProviderPresets()
	items := make([]map[string]any, 0, len(presets))
	for _, preset := range presets {
		items = append(items, map[string]any{
			"slug": preset.Slug, "displayName": preset.DisplayName,
			"issuer": preset.Issuer, "discoveryUrl": preset.DiscoveryURL,
			"scopes": preset.Scopes, "icon": preset.Icon,
			"requireVerifiedEmail": preset.RequireVerifiedEmail,
			"note":                 preset.Note,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) saveCustomerOIDCProvider(writer http.ResponseWriter, request *http.Request) {
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
		Icon                 string   `json:"icon"`
		SortOrder            int32    `json:"sortOrder"`
		RequireVerifiedEmail bool     `json:"requireVerifiedEmail"`
		AllowAutoProvision   bool     `json:"allowAutoProvision"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}

	provider, err := handlers.customerAuth.SaveOIDCProvider(request.Context(), customerauthpg.SaveOIDCProviderInput{
		Slug: body.Slug, DisplayName: body.DisplayName, Issuer: body.Issuer,
		DiscoveryURL: body.DiscoveryURL, ClientID: body.ClientID, ClientSecret: body.ClientSecret,
		Scopes: body.Scopes, Enabled: body.Enabled, Icon: body.Icon, SortOrder: body.SortOrder,
		RequireVerifiedEmail: body.RequireVerifiedEmail,
		AllowAutoProvision:   body.AllowAutoProvision,
	})
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "oidc_rejected", err.Error())
		return
	}

	// Changing who may sign in as a customer is a configuration action and is
	// audited like every other one.
	handlers.auditCustomerOIDC(request, principal, "admin.customer_oidc.saved", provider.Slug, map[string]any{
		"enabled": provider.Enabled,
	})
	writeJSON(writer, http.StatusOK, map[string]any{
		"slug": provider.Slug, "displayName": provider.DisplayName,
		"issuer": provider.Issuer, "discoveryUrl": provider.DiscoveryURL,
		"clientId": provider.ClientID, "scopes": provider.Scopes,
		"enabled": provider.Enabled, "icon": provider.Icon, "sortOrder": provider.SortOrder,
		"requireVerifiedEmail": provider.RequireVerifiedEmail,
		"allowAutoProvision":   provider.AllowAutoProvision,
		"hasClientSecret":      provider.HasClientSecret,
	})
}

func (handlers *AdminHandlers) deleteCustomerOIDCProvider(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	slug := chi.URLParam(request, "slug")

	err := handlers.customerAuth.DeleteOIDCProvider(request.Context(), slug)
	if errors.Is(err, customerauthpg.ErrNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That provider was not found")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "oidc_unavailable", "Provider could not be removed")
		return
	}
	handlers.auditCustomerOIDC(request, principal, "admin.customer_oidc.deleted", slug, nil)
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) auditCustomerOIDC(
	request *http.Request, principal adminauthpg.Principal,
	action, slug string, metadata map[string]any,
) {
	if handlers.service == nil {
		return
	}
	_ = handlers.service.AppendAudit(request.Context(), adminauthpg.AuditEntry{
		ActorType: "admin", ActorID: principal.Account.ID,
		Action: action, Category: "configuration", Outcome: "success",
		TargetType: "customer_oidc_provider", TargetID: slug,
		RequestID: middlewareRequestID(request), Metadata: metadata,
	})
}
