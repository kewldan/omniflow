package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/smartsub"
	"github.com/omniflow/omniflow/internal/smartsubpg"
)

// SmartSubscriptionHandler serves the link VPN clients import while the smart
// subscription is on: GET /v1/sub/{shortUUID}. The short UUID is the panel's
// own, so the link is exactly as secret as the panel's, and re-issuing the
// panel link re-issues this one.
type SmartSubscriptionHandler struct {
	service *smartsubpg.Service
	limiter *platform.RateLimiter
	proxies *TrustedProxies
	logger  *slog.Logger
}

// NewSmartSubscriptionHandler builds the public handler. A nil limiter
// leaves the route unlimited.
func NewSmartSubscriptionHandler(
	service *smartsubpg.Service, limiter *platform.RateLimiter, proxies *TrustedProxies, logger *slog.Logger,
) *SmartSubscriptionHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SmartSubscriptionHandler{service: service, limiter: limiter, proxies: proxies, logger: logger}
}

// Mount registers the route. It is mounted whether the module is on or off:
// a link a client imported while it was on keeps answering, with the panel's
// own subscription, after it is turned off.
func (handler *SmartSubscriptionHandler) Mount(router chi.Router) {
	router.Get("/v1/sub/{shortUUID}", handler.serve)
	router.Head("/v1/sub/{shortUUID}", handler.serve)
}

// subscriptionRequestsPerMinute bounds one address. It is generous because a
// mobile carrier's NAT puts many customers behind one address; a script
// walking short UUIDs still runs into it.
const subscriptionRequestsPerMinute = 600

func (handler *SmartSubscriptionHandler) serve(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	clientIP := ""
	if address := handler.proxies.ClientIP(request); address != nil {
		clientIP = address.String()
	}
	if handler.limiter != nil && clientIP != "" {
		allowed, err := handler.limiter.Allow(request.Context(), "smart_subscription", clientIP,
			subscriptionRequestsPerMinute, time.Minute)
		if err == nil && !allowed {
			writer.Header().Set("Retry-After", "60")
			writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many subscription requests")
			return
		}
	}
	response, err := handler.service.Serve(request.Context(), chi.URLParam(request, "shortUUID"), request.Header, clientIP)
	switch {
	case errors.Is(err, smartsubpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That subscription does not exist")
		return
	case err != nil:
		handler.logger.Warn("smart subscription could not reach the panel", "error", err)
		writer.Header().Set("Retry-After", "30")
		writeProblem(writer, request, http.StatusBadGateway, "panel_unavailable", "The subscription is temporarily unavailable")
		return
	}
	for name, values := range response.Header {
		writer.Header()[name] = values
	}
	if response.Status == http.StatusOK {
		// The body only changes when the servers, the settings, or the
		// customer's choices do, so a client that already has it is told so
		// and does not reload its tunnel.
		sum := sha256.Sum256(response.Body)
		tag := `"` + hex.EncodeToString(sum[:16]) + `"`
		writer.Header().Set("ETag", tag)
		if match := request.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writer.WriteHeader(response.Status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(response.Body)
	}
}

// WithSmartSubscription attaches the module to the operator panel. A nil
// service leaves its routes unmounted.
func (handlers *AdminHandlers) WithSmartSubscription(service *smartsubpg.Service) *AdminHandlers {
	handlers.smartSubscription = service
	return handlers
}

// mountSmartSubscription registers the module's settings and the live host
// list the settings screen places servers with. Both sit with the other
// installation settings, behind `settings.read` and `settings.write`.
func (handlers *AdminHandlers) mountSmartSubscription(secure chi.Router) {
	if handlers.smartSubscription == nil || handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/smart-subscription", handlers.smartSubscriptionSettings)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Put("/settings/smart-subscription", handlers.saveSmartSubscriptionSettings)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/smart-subscription/hosts", handlers.smartSubscriptionHosts)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/smart-subscription/stats", handlers.smartSubscriptionStats)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Post("/settings/smart-subscription/icons", handlers.uploadSmartSubscriptionIcon)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Post("/settings/smart-subscription/icons/fetch", handlers.fetchSmartSubscriptionIcon)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/smart-subscription/icons/{iconID}", handlers.smartSubscriptionIcon)
}

func (handlers *AdminHandlers) smartSubscriptionStats(writer http.ResponseWriter, request *http.Request) {
	stats, err := handlers.smartSubscription.Stats(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, stats)
}

// uploadSmartSubscriptionIcon stores a rule icon sent as the raw request
// body. Its type is read from the bytes, not from the header.
func (handlers *AdminHandlers) uploadSmartSubscriptionIcon(writer http.ResponseWriter, request *http.Request) {
	data, err := io.ReadAll(io.LimitReader(request.Body, smartsubpg.MaxIconBytes+1))
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "The upload could not be read")
		return
	}
	id, err := handlers.smartSubscription.SaveIcon(request.Context(), data, actorFrom(request).AdminID)
	handlers.writeIconResult(writer, request, id, err)
}

// fetchSmartSubscriptionIcon downloads a site's logo from logo.dev by domain.
func (handlers *AdminHandlers) fetchSmartSubscriptionIcon(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	id, err := handlers.smartSubscription.FetchLogo(request.Context(), body.Domain, actorFrom(request).AdminID)
	handlers.writeIconResult(writer, request, id, err)
}

func (handlers *AdminHandlers) writeIconResult(writer http.ResponseWriter, request *http.Request, id string, err error) {
	var field *smartsub.FieldError
	switch {
	case err == nil:
		writeJSON(writer, http.StatusCreated, map[string]any{"id": id})
	case errors.Is(err, smartsubpg.ErrIconInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_icon", err.Error())
	case errors.Is(err, smartsubpg.ErrLogoSearchOff):
		writeProblem(writer, request, http.StatusConflict, "logo_search_off", "Logo search needs APP_LOGO_DEV_TOKEN")
	case errors.Is(err, smartsubpg.ErrLogoNotFound):
		writeProblem(writer, request, http.StatusNotFound, "logo_not_found", "logo.dev has no logo for that domain")
	case errors.As(err, &field):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_domain", field.Reason)
	default:
		handlers.logger.Warn("smart subscription icon failed", "error", err)
		writeProblem(writer, request, http.StatusBadGateway, "icon_failed", "The icon could not be stored")
	}
}

func (handlers *AdminHandlers) smartSubscriptionIcon(writer http.ResponseWriter, request *http.Request) {
	serveIcon(writer, request, handlers.smartSubscription)
}

// serveIcon writes a stored icon. An icon never changes under its ID, and an
// SVG is sandboxed so opening it directly runs nothing.
func serveIcon(writer http.ResponseWriter, request *http.Request, service *smartsubpg.Service) {
	icon, err := service.Icon(request.Context(), chi.URLParam(request, "iconID"))
	if errors.Is(err, smartsubpg.ErrIconNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That icon does not exist")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "operation_failed", "The icon could not be read")
		return
	}
	writer.Header().Set("Content-Type", icon.ContentType)
	writer.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	_, _ = writer.Write(icon.Data)
}

func (handlers *AdminHandlers) smartSubscriptionPayload(setting panelpg.SmartSubscription) map[string]any {
	hosts := setting.Hosts
	if hosts == nil {
		hosts = map[string]smartsub.HostOverride{}
	}
	return map[string]any{
		"enabled": setting.Enabled, "version": setting.Version,
		"linkBase": setting.LinkBase, "effectiveLinkBase": handlers.smartSubscription.LinkBase(setting.Settings),
		"probeUrl": setting.ProbeURL, "probeIntervalSeconds": setting.ProbeIntervalSeconds,
		"autoName": setting.AutoName, "reserveName": setting.ReserveName,
		"customRules": setting.CustomRules, "maxRules": setting.MaxRules, "rules": rulesPayload(setting.Rules),
		"outageNotices": setting.OutageNotices, "outageMinutes": setting.OutageMinutes,
		"logoSearch": handlers.smartSubscription.LogoSearch(), "hosts": hosts,
	}
}

func (handlers *AdminHandlers) smartSubscriptionSettings(writer http.ResponseWriter, request *http.Request) {
	setting, err := handlers.operations.SmartSubscription(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, handlers.smartSubscriptionPayload(setting))
}

func (handlers *AdminHandlers) saveSmartSubscriptionSettings(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.SmartSubscription
	if !decodeJSON(writer, request, &body) {
		return
	}
	setting, err := handlers.operations.SaveSmartSubscription(request.Context(), body, actorFrom(request))
	var field *smartsub.FieldError
	if errors.As(err, &field) {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
			"type": "https://omniflow.dev/problems/invalid_setting", "title": "Unprocessable Entity",
			"status": http.StatusUnprocessableEntity, "detail": field.Reason, "field": field.Field,
			"request_id": middlewareRequestID(request),
		})
		return
	}
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	handlers.smartSubscription.Invalidate()
	writeJSON(writer, http.StatusOK, handlers.smartSubscriptionPayload(setting))
}

// smartSubscriptionHosts lists the panel's hosts with the country read from
// each remark, so the operator corrects only what was read wrong, and the node
// each host exits from with what that node's latest geocheck found.
func (handlers *AdminHandlers) smartSubscriptionHosts(writer http.ResponseWriter, request *http.Request) {
	setting, err := handlers.operations.SmartSubscription(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	catalog, err := handlers.smartSubscription.FreshCatalog(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusBadGateway, "panel_unavailable", "Remnawave did not answer")
		return
	}
	checks := handlers.smartSubscription.NodeChecks(request.Context())
	hosts := make([]map[string]any, 0, len(catalog.Hosts))
	for _, host := range catalog.Hosts {
		server := catalog.Classify(host.Remark, host.Address, host.Port, setting.Settings)
		entry := map[string]any{
			"uuid": host.UUID, "remark": host.Remark, "address": host.Address, "port": host.Port,
			"detectedCountry": server.Detected, "country": server.Country, "role": server.Role,
			"disabled": host.Disabled, "hidden": host.Hidden, "down": server.Down,
			"nodes": host.Nodes, "exitNode": server.ExitNode,
		}
		if check, ok := checks[server.ExitNode]; ok && server.ExitNode != "" {
			entry["services"], entry["checkedAt"] = check.Services, check.CheckedAt
		}
		hosts = append(hosts, entry)
	}
	nodes := make([]map[string]string, 0, len(catalog.NodeNames))
	for id, name := range catalog.NodeNames {
		nodes = append(nodes, map[string]string{"uuid": id, "name": name})
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left]["name"] < nodes[right]["name"] })
	writeJSON(writer, http.StatusOK, map[string]any{"hosts": hosts, "nodes": nodes})
}

// WithSmartSubscription attaches the module to the customer panel.
func (handlers *AccountHandlers) WithSmartSubscription(service *smartsubpg.Service) *AccountHandlers {
	handlers.smartSubscription = service
	return handlers
}

func (handlers *AccountHandlers) mountSmartSubscription(secure chi.Router) {
	if handlers.smartSubscription == nil {
		return
	}
	secure.Get("/smart-subscription", handlers.smartSubscriptionStatus)
	secure.Get("/smart-subscription/icons/{iconID}", handlers.smartSubscriptionIcon)
	secure.Get("/subscriptions/{subscriptionID}/smart", handlers.smartSubscriptionView)
	secure.Put("/subscriptions/{subscriptionID}/smart", handlers.saveSmartSubscription)
	secure.Delete("/subscriptions/{subscriptionID}/smart", handlers.resetSmartSubscription)
}

// smartSubscriptionStatus tells the panel whether to offer the screen at all.
func (handlers *AccountHandlers) smartSubscriptionStatus(writer http.ResponseWriter, request *http.Request) {
	settings := handlers.smartSubscription.Settings(request.Context())
	writeJSON(writer, http.StatusOK, map[string]any{"enabled": settings.Enabled})
}

func (handlers *AccountHandlers) smartSubscriptionView(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.smartSubscription.CustomerView(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
	)
	if handlers.writeSmartSubscriptionError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"servers": view.Servers, "preferences": view.Preferences, "customized": view.Customized,
		"customRules": view.CustomRules, "maxRules": view.MaxRules,
		"rules": rulesPayload(view.Rules), "aiServers": view.AIServers,
	})
}

func (handlers *AccountHandlers) smartSubscriptionIcon(writer http.ResponseWriter, request *http.Request) {
	serveIcon(writer, request, handlers.smartSubscription)
}

// rulesPayload renders service rules with their domains always a list.
func rulesPayload(rules []smartsub.ServiceRule) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]any{
			"id": rule.ID, "kind": rule.Kind, "name": rule.Name, "caption": rule.Caption, "icon": rule.Icon,
			"action": rule.Action, "mode": rule.Mode, "domains": nonNil(rule.Domains),
		})
	}
	return out
}

func (handlers *AccountHandlers) saveSmartSubscription(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body smartsub.Preferences
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.smartSubscription.SavePreferences(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"), body,
	)
	if handlers.writeSmartSubscriptionError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"preferences": saved})
}

func (handlers *AccountHandlers) resetSmartSubscription(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.smartSubscription.ResetPreferences(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
	)
	if handlers.writeSmartSubscriptionError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) writeSmartSubscriptionError(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	var field *smartsub.FieldError
	switch {
	case err == nil:
		return false
	case errors.As(err, &field):
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
			"type": "https://omniflow.dev/problems/invalid_preferences", "title": "Unprocessable Entity",
			"status": http.StatusUnprocessableEntity, "detail": field.Reason, "field": field.Field,
			"request_id": middlewareRequestID(request),
		})
	case errors.Is(err, smartsubpg.ErrDisabled):
		writeProblem(writer, request, http.StatusConflict, "smart_subscription_off", "The smart subscription is not on")
	case errors.Is(err, smartsubpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That subscription does not exist")
	case errors.Is(err, smartsubpg.ErrNotProvisioned):
		writeProblem(writer, request, http.StatusConflict, "not_provisioned", "The subscription is still being set up")
	case errors.Is(err, smartsubpg.ErrPanelUnavailable):
		writeProblem(writer, request, http.StatusServiceUnavailable, "panel_unavailable", "The server list is temporarily unavailable")
	default:
		handlers.logger.Error("smart subscription request failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "operation_failed", "That could not be completed")
	}
	return true
}

// nonNil renders an absent list as an empty one, so the screen always reads
// an array.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
