package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// ---------------------------------------------------------------------------
// Account and session
// ---------------------------------------------------------------------------

// currentAccount is what the shell reads on load: who is signed in, how, and
// whether a sensitive action would need a fresh sign-in.
func (handlers *AccountHandlers) currentAccount(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	writeJSON(writer, http.StatusOK, map[string]any{
		"customer": map[string]any{
			"id":       principal.Customer.ID,
			"locale":   principal.Customer.Locale,
			"timezone": principal.Customer.Timezone,
			"status":   principal.Customer.Status,
		},
		"session": map[string]any{
			"id":                       principal.SessionID,
			"authMethod":               principal.AuthMethod,
			"authProvider":             principal.AuthProvider,
			"expiresAt":                principal.ExpiresAt.Format(time.RFC3339),
			"reauthenticationRequired": principal.ReauthenticationRequired,
		},
	})
}

func (handlers *AccountHandlers) updateProfile(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if handlers.account == nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "account_unavailable", "Unavailable")
		return
	}
	var body struct {
		Locale   string `json:"locale"`
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	updated, err := handlers.account.UpdateProfile(
		request.Context(), principal.Customer.ID, body.Locale, body.Timezone,
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"id": updated.ID, "locale": updated.Locale, "timezone": updated.Timezone, "status": updated.Status,
	})
}

func (handlers *AccountHandlers) logout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if err := handlers.auth.SignOut(
		request.Context(), principal.Customer.ID, principal.SessionID, handlers.requestContext(request),
	); err != nil {
		handlers.logger.Warn("customer sign-out failed", "error", err)
	}
	// The cookie is cleared regardless. A customer who pressed "sign out" must
	// end up signed out of this browser even if the server-side revocation had
	// already happened or transiently failed.
	handlers.clearSessionCookie(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) logoutEverywhere(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		// KeepCurrent distinguishes "sign out my other devices" from "sign out
		// everywhere". After a suspected compromise the second is what is meant,
		// and it must not spare the session that asked for it.
		KeepCurrent bool `json:"keepCurrent"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	revoked, err := handlers.auth.SignOutEverywhere(
		request.Context(), principal.Customer.ID, principal.SessionID, body.KeepCurrent,
		handlers.requestContext(request),
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "sign_out_failed", "Sessions could not be ended")
		return
	}
	if !body.KeepCurrent {
		handlers.clearSessionCookie(writer)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"revoked": revoked})
}

func (handlers *AccountHandlers) listSessions(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	sessions, err := handlers.auth.ListSessions(
		request.Context(), principal.Customer.ID, principal.SessionID, 50,
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "sessions_unavailable", "Sessions are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, map[string]any{
			"id": session.ID, "current": session.Current,
			"authMethod": session.AuthMethod, "authProvider": session.AuthProvider,
			"ip": session.IP, "userAgent": session.UserAgent,
			"createdAt":  session.CreatedAt.Format(time.RFC3339),
			"lastSeenAt": session.LastSeenAt.Format(time.RFC3339),
			"expiresAt":  session.ExpiresAt.Format(time.RFC3339),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AccountHandlers) revokeSession(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	sessionID := chi.URLParam(request, "sessionID")
	err := handlers.auth.RevokeSession(
		request.Context(), principal.Customer.ID, sessionID, handlers.requestContext(request),
	)
	if errors.Is(err, customerauthpg.ErrNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That session was not found")
		return
	}
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "sign_out_failed", "Session could not be ended")
		return
	}
	// Ending the calling session through this route still clears the cookie, so
	// the browser does not keep presenting a token that no longer resolves.
	if sessionID == principal.SessionID {
		handlers.clearSessionCookie(writer)
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) listSecurityEvents(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var cursor *time.Time
	if raw := trimmedQuery(request, "cursor"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(writer, request, http.StatusBadRequest, "invalid_cursor", "The cursor is not a timestamp")
			return
		}
		cursor = &parsed
	}
	events, err := handlers.auth.ListSecurityEvents(
		request.Context(), principal.Customer.ID, cursor, trimmedQuery(request, "cursorId"), 50,
	)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "events_unavailable", "Events are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, map[string]any{
			"id": event.ID, "event": event.Event, "ip": event.IP, "userAgent": event.UserAgent,
			"metadata": event.Metadata, "occurredAt": event.OccurredAt.Format(time.RFC3339),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// ---------------------------------------------------------------------------
// Sign-in methods
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) listSignInMethods(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	methods, err := handlers.auth.ListSignInMethods(request.Context(), principal.Customer.ID)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "methods_unavailable", "Unavailable")
		return
	}
	items := make([]map[string]any, 0, len(methods))
	for _, method := range methods {
		items = append(items, map[string]any{
			"id": method.ID, "provider": method.Provider,
			"label": method.Label, "removable": method.Removable,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AccountHandlers) unlinkSignInMethod(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.auth.UnlinkIdentity(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "identityID"),
		handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, customerauth.ErrLastSignInMethod):
		writeProblem(
			writer, request, http.StatusConflict,
			"last_sign_in_method", "This is the only way left to sign in to this account",
		)
		return
	case errors.Is(err, customerauthpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That sign-in method was not found")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "unlink_failed", "It could not be removed")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Dashboard and subscriptions
// ---------------------------------------------------------------------------

// requireAccount reports whether the read model is attached.
func (handlers *AccountHandlers) requireAccount(writer http.ResponseWriter, request *http.Request) bool {
	if handlers.account == nil {
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"account_unavailable", "The customer panel is not configured",
		)
		return false
	}
	return true
}

func (handlers *AccountHandlers) overview(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	overview, err := handlers.account.Overview(
		request.Context(), principal.Customer.ID, trimmedQuery(request, "locale"),
	)
	if err != nil {
		handlers.logger.Error("customer overview failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "overview_unavailable", "Unavailable")
		return
	}
	subscriptions := make([]map[string]any, 0, len(overview.Subscriptions))
	for _, subscription := range overview.Subscriptions {
		subscriptions = append(subscriptions, subscriptionPayload(subscription))
	}
	payload := map[string]any{
		"customer": map[string]any{
			"id": overview.Customer.ID, "locale": overview.Customer.Locale,
			"timezone": overview.Customer.Timezone, "status": overview.Customer.Status,
		},
		"subscriptions": subscriptions,
		"showSwitcher":  overview.ShowSwitcher,
		"degraded":      overview.Degraded,
	}
	if overview.Notice.Active {
		notice := map[string]any{"active": true, "message": overview.Notice.Message}
		if !overview.Notice.ExpectedReturnAt.IsZero() {
			notice["expectedReturnAt"] = overview.Notice.ExpectedReturnAt.Format(time.RFC3339)
		}
		payload["notice"] = notice
	}
	writeJSON(writer, http.StatusOK, payload)
}

// subscriptionPayload is the wire shape of one subscription.
//
// The traffic percentage and the day count are computed on the server, so the
// bar and its textual equivalent cannot drift apart, and so two surfaces reading
// the same record describe it the same way.
func subscriptionPayload(subscription accountpg.Subscription) map[string]any {
	payload := map[string]any{
		"id": subscription.ID, "slot": subscription.Slot, "label": subscription.Label,
		"plan": subscription.Plan, "phase": string(subscription.Phase),
		"daysLeft": subscription.DaysLeft, "provisioned": subscription.Provisioned,
		"live": subscription.Live,
		"traffic": map[string]any{
			"usedBytes": subscription.Traffic.UsedBytes,
			"limitBytes": func() any {
				if subscription.Traffic.Unlimited {
					return nil
				}
				return subscription.Traffic.LimitBytes
			}(),
			"unlimited": subscription.Traffic.Unlimited,
			"percent":   subscription.Traffic.Percent,
		},
		"devices": map[string]any{
			"used": subscription.Devices.Used,
			"limit": func() any {
				if subscription.Devices.Unlimited {
					return nil
				}
				return subscription.Devices.Limit
			}(),
			"unlimited": subscription.Devices.Unlimited,
		},
	}
	if !subscription.EndsAt.IsZero() {
		payload["endsAt"] = subscription.EndsAt.Format(time.RFC3339)
	}
	return payload
}

func (handlers *AccountHandlers) subscription(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	subscription, err := handlers.account.Subscription(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		trimmedQuery(request, "locale"),
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, subscriptionPayload(subscription))
}

func (handlers *AccountHandlers) renameSubscription(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Label string `json:"label"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.account.RenameSubscription(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"), body.Label,
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) connection(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	// The catalogue is operator-editable, so a platform label and a client's
	// instructions come back resolved to the customer's language rather than as
	// keys: nothing an operator types can be added to a compiled catalogue.
	connection, err := handlers.account.Connection(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		trimmedQuery(request, "platform"), principal.Customer.Locale,
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	clients := make([]map[string]any, 0, len(connection.Clients))
	for _, client := range connection.Clients {
		clients = append(clients, map[string]any{
			"name": client.Name, "deepLink": client.DeepLink,
			"downloadUrl": client.DownloadURL, "instructions": client.Instructions,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"subscriptionUrl": connection.SubscriptionURL,
		"platform":        connection.Platform,
		"platforms":       connection.Platforms,
		"clients":         clients,
	})
}

func (handlers *AccountHandlers) rotateSubscriptionLink(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		// Confirm has to be sent explicitly. Rotation breaks every connected
		// device until the new link is imported, so a mis-routed request or a
		// double submission must not be able to trigger it.
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if !body.Confirm {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"confirmation_required", "This action must be confirmed",
		)
		return
	}
	rotated, err := handlers.account.RotateSubscriptionLink(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		handlers.securityRequest(request),
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"subscriptionUrl": rotated})
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) listDevices(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	devices, err := handlers.account.Devices(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		items = append(items, map[string]any{
			"handle": device.Handle, "name": device.Name, "platform": device.Platform,
			"lastSeen": device.LastSeen.Format(time.RFC3339),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AccountHandlers) removeDevice(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	err := handlers.account.RemoveDevice(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		chi.URLParam(request, "handle"), handlers.securityRequest(request),
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) removeAllDevices(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	if trimmedQuery(request, "confirm") != "true" {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"confirmation_required", "This action must be confirmed",
		)
		return
	}
	removed, err := handlers.account.RemoveAllDevices(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		handlers.securityRequest(request),
	)
	if handlers.writeAccountError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"removed": removed})
}

// writeAccountError maps the read model's errors onto responses, reporting
// whether it wrote one.
func (handlers *AccountHandlers) writeAccountError(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accountpg.ErrInvalidInput):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_input", err.Error())
	case errors.Is(err, accountpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That subscription was not found")
	case errors.Is(err, accountpg.ErrNotProvisioned):
		writeProblem(
			writer, request, http.StatusConflict,
			"not_provisioned", "This subscription is still being set up",
		)
	case errors.Is(err, accountpg.ErrRemnawaveUnavailable):
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"upstream_unavailable", "The service is temporarily unavailable",
		)
	default:
		handlers.logger.Error("customer account request failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "request_failed", "Something went wrong")
	}
	return true
}
