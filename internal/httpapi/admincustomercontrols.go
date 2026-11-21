package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountCustomerControls mounts the customer card's operator controls:
// restrictions, trial reset, auto-renew, free grants, purchases on the
// customer's behalf, the activity log, and the advanced list.
func (handlers *AdminHandlers) mountCustomerControls(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).Group(func(read chi.Router) {
		read.Get("/customers/advanced", handlers.searchCustomersAdvanced)
		read.Get("/customers/{customerID}/controls", handlers.customerControls)
		read.Get("/customers/{customerID}/activity", handlers.customerActivity)
		read.Get("/customers/{customerID}/legacy-records", handlers.customerLegacyRecords)
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).
		Get("/reports/bot-buttons", handlers.botButtonStats)
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).
		Put("/customers/{customerID}/restrictions/{kind}", handlers.setCustomerRestriction)
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).Group(func(write chi.Router) {
		write.Post("/customers/{customerID}/trial-reset", handlers.resetCustomerTrial)
		write.Put("/customers/{customerID}/auto-renew", handlers.setCustomerAutoRenew)
		if handlers.commerce != nil {
			write.Post("/customers/{customerID}/grants", handlers.grantCustomerPlan)
		}
	})
	// A purchase on the customer's behalf spends their wallet, so it is
	// finance's, like a wallet adjustment.
	if handlers.commerce != nil {
		secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).
			Post("/customers/{customerID}/purchases", handlers.purchaseForCustomer)
	}
}

func (handlers *AdminHandlers) customerControls(writer http.ResponseWriter, request *http.Request) {
	controls, err := handlers.operations.CustomerControlState(request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, controls, err)
}

func (handlers *AdminHandlers) customerActivity(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerActivityLog(request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) customerLegacyRecords(writer http.ResponseWriter, request *http.Request) {
	records, kinds, total, err := handlers.operations.CustomerLegacyRecords(request.Context(),
		chi.URLParam(request, "customerID"), request.URL.Query().Get("kind"), int32(queryInt(request, "before")))
	handlers.respond(writer, request, map[string]any{"items": records, "kinds": kinds, "total": total}, err)
}

func (handlers *AdminHandlers) botButtonStats(writer http.ResponseWriter, request *http.Request) {
	days, _ := strconv.Atoi(request.URL.Query().Get("days"))
	items, err := handlers.operations.ButtonStats(request.Context(), days)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) searchCustomersAdvanced(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	number := func(name string) int {
		// Every filter here is a positive bound or absent, so a negative or
		// malformed value reads as absent, and a huge one cannot wrap.
		return int(parseInt32(query.Get(name)))
	}
	items, err := handlers.operations.SearchCustomersAdvanced(request.Context(), panelpg.AdvancedFilter{
		Status: query.Get("status"), ExpiringDays: number("expiringDays"),
		WithBalance: query.Get("withBalance") == "true", AutoRenew: query.Get("autoRenew") == "true",
		Restricted: query.Get("restricted") == "true", TrialOnly: query.Get("trialOnly") == "true",
		Campaign: strings.TrimSpace(query.Get("campaign")), ActiveWithinDays: number("activeWithinDays"),
		TrafficPercent: number("trafficPercent"), Sort: query.Get("sort"),
	})
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) setCustomerRestriction(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Restricted bool   `json:"restricted"`
		Reason     string `json:"reason"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SetCustomerRestriction(request.Context(), chi.URLParam(request, "customerID"),
		chi.URLParam(request, "kind"), body.Reason, body.Restricted, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"restricted": body.Restricted}, err)
}

func (handlers *AdminHandlers) resetCustomerTrial(writer http.ResponseWriter, request *http.Request) {
	removed, err := handlers.operations.ResetCustomerTrial(request.Context(), chi.URLParam(request, "customerID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"cleared": removed}, err)
}

func (handlers *AdminHandlers) setCustomerAutoRenew(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SetCustomerAutoRenew(request.Context(), chi.URLParam(request, "customerID"), body.Enabled, actorFrom(request))
	if errors.Is(err, panelpg.ErrRejected) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "consent_required",
			"Only a renewal the customer set up and consented to can be turned back on")
		return
	}
	handlers.respond(writer, request, map[string]any{"enabled": body.Enabled}, err)
}

func (handlers *AdminHandlers) grantCustomerPlan(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Mode           string `json:"mode"`
		PlanID         string `json:"planId"`
		PlanVersionID  string `json:"planVersionId"`
		Days           int    `json:"days"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.PlanVersionID == "" && body.PlanID != "" {
		version, _, err := handlers.operations.LatestPlanVersion(request.Context(), body.PlanID)
		if err != nil {
			handlers.operationsError(writer, request, err)
			return
		}
		body.PlanVersionID = version
	}
	customerID := chi.URLParam(request, "customerID")
	endsAt, orderID, err := handlers.commerce.GrantPlan(request.Context(), commercepg.GrantInput{
		CustomerID: customerID, PlanVersionID: body.PlanVersionID, Mode: body.Mode, Days: body.Days,
		IdempotencyKey: body.IdempotencyKey,
	})
	if errors.Is(err, commercepg.ErrGrantRefused) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "grant_refused",
			"That grant does not apply: a trial needs a customer without access who never had one, a plan change needs current access")
		return
	}
	if err != nil {
		handlers.logger.Error("panel grant failed", "error", err)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "grant_failed", "The grant could not be made")
		return
	}
	_ = handlers.operations.RecordCustomerAction(request.Context(), "customer.plan.granted", "customer", customerID,
		map[string]any{"mode": body.Mode, "days": body.Days, "orderId": orderID}, actorFrom(request))
	writeJSON(writer, http.StatusOK, map[string]any{"orderId": orderID, "endsAt": endsAt})
}

func (handlers *AdminHandlers) purchaseForCustomer(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		PlanID         string `json:"planId"`
		PlanVersionID  string `json:"planVersionId"`
		Currency       string `json:"currency"`
		Operation      string `json:"operation"`
		SubscriptionID string `json:"subscriptionId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.PlanVersionID == "" && body.PlanID != "" {
		version, currency, err := handlers.operations.LatestPlanVersion(request.Context(), body.PlanID)
		if err != nil {
			handlers.operationsError(writer, request, err)
			return
		}
		body.PlanVersionID = version
		if body.Currency == "" {
			body.Currency = currency
		}
	}
	if body.Operation == "" {
		body.Operation = "purchase"
	}
	customerID := chi.URLParam(request, "customerID")
	order, err := handlers.commerce.BuyFromBalance(request.Context(), commercepg.CreateOrderInput{
		CustomerID: customerID, PlanVersionID: body.PlanVersionID, Currency: strings.ToUpper(body.Currency),
		Operation: body.Operation, SubscriptionID: body.SubscriptionID,
		IdempotencyKey: "panel-purchase:" + body.IdempotencyKey,
	})
	switch {
	case errors.Is(err, commercepg.ErrInsufficientBalance):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "insufficient_balance", "The wallet does not cover this plan")
		return
	case errors.Is(err, commercepg.ErrRestricted):
		writeProblem(writer, request, http.StatusForbidden, "customer_restricted", "Purchases are restricted for this customer")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "purchase_failed", err.Error())
		return
	}
	_ = handlers.operations.RecordCustomerAction(request.Context(), "customer.purchase.on_behalf", "financial", customerID,
		map[string]any{"orderId": uuidString(order.ID), "operation": body.Operation}, actorFrom(request))
	writeJSON(writer, http.StatusOK, map[string]any{"orderId": uuidString(order.ID), "state": order.State})
}
