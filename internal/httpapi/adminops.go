package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountOperations registers the v0.7 operations routes.
//
// Every route declares the permission it needs at the mount point rather than
// checking inside the handler. That is deliberate: a permission check written
// inside a handler is one an added early return can skip, whereas a middleware
// wrapping the route cannot be bypassed by anything the handler does.
//
// The panel renders from the same permission set, so a surface an operator
// cannot use is not shown — but hiding it is presentation, and this is the
// enforcement.
func (handlers *AdminHandlers) mountOperations(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	// -- Overview ------------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/overview/dashboard", handlers.dashboard)
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/overview/incidents", handlers.recentIncidents)
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/overview/health", handlers.dependencyHealth)
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/overview/maintenance", handlers.maintenanceState)
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).
		Put("/overview/maintenance", handlers.setMaintenance)

	// -- Customers -----------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).Group(func(read chi.Router) {
		read.Get("/customers", handlers.searchCustomers)
		read.Get("/customers/{customerID}", handlers.customerProfile)
		read.Get("/customers/{customerID}/subscriptions", handlers.customerSubscriptions)
		read.Get("/customers/{customerID}/orders", handlers.customerOrders)
		read.Get("/customers/{customerID}/tickets", handlers.customerTickets)
		read.Get("/customers/{customerID}/consents", handlers.customerConsents)
		read.Get("/customers/{customerID}/referrals", handlers.customerReferrals)
	})
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).
		Post("/customers/{customerID}/status", handlers.setCustomerStatus)
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).
		Post("/customers/{customerID}/anonymize", handlers.anonymizeCustomer)
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).
		Post("/customers/{customerID}/deletion", handlers.requestCustomerDeletion)
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
		Patch("/customers/{customerID}/subscriptions/{subscriptionID}", handlers.renameSubscription)

	// The wallet ledger is money, so it is gated on finance rather than on
	// customer access: a support operator can see that a customer has a
	// balance without being able to read every movement that produced it.
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/customers/{customerID}/wallet", handlers.customerWallet)
		read.Get("/customers/{customerID}/payment-methods", handlers.customerPaymentMethods)
		read.Get("/customers/{customerID}/charges", handlers.customerCharges)
	})
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).
		Delete("/customers/{customerID}/payment-methods/{methodID}", handlers.revokePaymentMethod)

	// -- Catalogue -----------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionCatalogRead)).Group(func(read chi.Router) {
		read.Get("/catalog/plans", handlers.listPlans)
		read.Get("/catalog/plans/{planID}", handlers.planDetail)
		read.Get("/catalog/addons", handlers.listAddons)
		read.Get("/catalog/addons/{addonID}/versions", handlers.addonVersions)
		read.Get("/catalog/promotions", handlers.listPromotions)
		read.Get("/catalog/promotions/{promotionID}/codes", handlers.listPromoCodes)
	})
	secure.With(handlers.requirePermission(rbac.PermissionCatalogWrite)).Group(func(write chi.Router) {
		write.Patch("/catalog/plans/{planID}", handlers.updatePlan)
		write.Post("/catalog/plans/{planID}/archive", handlers.archivePlan)
		write.Put("/catalog/plans/{planID}/localizations/{locale}", handlers.savePlanLocalization)
		write.Post("/catalog/plans/{planID}/versions", handlers.createPlanVersion)
		write.Post("/catalog/plan-versions/{planVersionID}/retire", handlers.retirePlanVersion)
		write.Put("/catalog/addons", handlers.saveAddon)
		write.Post("/catalog/addon-versions/{addonVersionID}/retire", handlers.retireAddonVersion)
		write.Patch("/catalog/promotions/{promotionID}", handlers.updatePromotion)
		write.Post("/catalog/promo-codes/{promoCodeID}/status", handlers.setPromoCodeActive)
	})

	// -- Personal offers -----------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).
		Get("/offers", handlers.searchOffers)
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Post("/offers", handlers.createOffer)
		write.Post("/offers/{offerID}/revoke", handlers.revokeOffer)
	})

	// -- Finance -------------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/finance/orders", handlers.searchOrders)
		read.Get("/finance/orders/{orderID}", handlers.orderDetail)
		read.Get("/finance/stuck-payments", handlers.stuckPayments)
		read.Get("/finance/failed-charges", handlers.failedCharges)
		read.Get("/finance/export", handlers.exportFinance)
	})
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).Group(func(write chi.Router) {
		write.Post("/finance/payments/{paymentIntentID}/reconcile", handlers.reconcilePayment)
		write.Post("/finance/orders/{orderID}/refund", handlers.recordRefund)
	})

	// -- Fulfillment and jobs -------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).Group(func(read chi.Router) {
		read.Get("/system/jobs", handlers.searchFulfillment)
		read.Get("/system/jobs/{operationID}/history", handlers.fulfillmentHistory)
		read.Get("/system/webhooks", handlers.searchWebhooks)
		read.Get("/system/outbox", handlers.outboxBacklog)
		read.Get("/system/drift", handlers.openDrifts)
	})
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).Group(func(write chi.Router) {
		write.Post("/system/jobs/{operationID}/retry", handlers.retryFulfillment)
		write.Post("/system/jobs/{operationID}/cancel", handlers.cancelFulfillment)
		write.Post("/system/webhooks/{eventID}/replay", handlers.replayWebhook)
	})

	// -- Settings ------------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).Group(func(read chi.Router) {
		read.Get("/settings/commerce", handlers.commerceSettings)
		read.Get("/settings/providers", handlers.listProviderSettings)
	})
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/settings/commerce/topup", handlers.saveTopUpSettings)
		write.Put("/settings/commerce/subscriptions", handlers.saveSubscriptionSettings)
		write.Put("/settings/providers/{provider}", handlers.saveProviderSettings)
		write.Post("/settings/providers/{provider}/recurring", handlers.configureRecurring)
		write.Post("/settings/providers/{provider}/test", handlers.testProviderConnection)
	})

	// -- Risk ----------------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionRiskRead)).Group(func(read chi.Router) {
		read.Get("/risk/sources", handlers.listBlocklistSources)
		read.Get("/risk/matches", handlers.searchMatches)
		read.Get("/customers/{customerID}/risk", handlers.customerMatches)
		read.Get("/risk/rules", handlers.listAnomalyRules)
		read.Get("/risk/anomalies", handlers.searchAnomalies)
	})
	secure.With(handlers.requirePermission(rbac.PermissionRiskWrite)).Group(func(write chi.Router) {
		write.Put("/risk/sources", handlers.saveBlocklistSource)
		write.Delete("/risk/sources/{sourceID}", handlers.deleteBlocklistSource)
		write.Post("/risk/matches/{matchID}/decision", handlers.decideMatch)
		write.Post("/risk/matches/{matchID}/appeal", handlers.appealMatch)
		write.Put("/risk/rules/{metric}", handlers.saveAnomalyRule)
		write.Post("/risk/anomalies/{signalID}/review", handlers.reviewAnomaly)
		write.Put("/customers/{customerID}/allowlist", handlers.setAllowlisted)
		write.Delete("/customers/{customerID}/allowlist", handlers.setAllowlisted)
	})

	// -- Gifts ---------------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionGiftsRead)).
		Get("/gifts", handlers.searchGifts)
	secure.With(handlers.requirePermission(rbac.PermissionGiftsWrite)).Group(func(write chi.Router) {
		write.Post("/gifts/{giftID}/revoke", handlers.revokeGift)
		write.Post("/gifts/{giftID}/refund", handlers.refundGift)
	})

	// -- Digital goods -------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionGoodsRead)).Group(func(read chi.Router) {
		read.Get("/goods/providers", handlers.listGoodsProviders)
		read.Get("/goods/products", handlers.listGoodsProducts)
		read.Get("/goods/orders", handlers.searchGoodsOrders)
		read.Get("/goods/orders/{orderID}/attempts", handlers.goodsDeliveryHistory)
		read.Get("/goods/review", handlers.goodsReviewQueue)
	})
	secure.With(handlers.requirePermission(rbac.PermissionGoodsWrite)).Group(func(write chi.Router) {
		write.Put("/goods/providers/{slug}", handlers.saveGoodsProvider)
		write.Post("/goods/products", handlers.createGoodsProduct)
		write.Patch("/goods/products/{productID}", handlers.updateGoodsProduct)
		write.Put("/goods/products/{productID}/localizations/{locale}", handlers.saveGoodsLocalization)
		write.Put("/goods/products/{productID}/pricing", handlers.saveGoodsPricing)
		write.Post("/goods/orders/{orderID}/cancel", handlers.cancelGoodsDelivery)
		write.Post("/goods/orders/{orderID}/resolve", handlers.resolveGoodsDelivery)
	})

	handlers.mountSubscriptionOperations(secure)

	// -- Bulk actions --------------------------------------------------------
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).Group(func(read chi.Router) {
		read.Get("/bulk", handlers.listBulkOperations)
		read.Get("/bulk/{operationID}/items", handlers.bulkItems)
	})
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).Group(func(write chi.Router) {
		write.Post("/bulk", handlers.previewBulk)
		write.Post("/bulk/{operationID}/start", handlers.startBulk)
		write.Post("/bulk/{operationID}/cancel", handlers.cancelBulk)
	})
}

// ---------------------------------------------------------------------------
// Shared plumbing
// ---------------------------------------------------------------------------

// actorFrom builds the audit actor for a request.
//
// The reason travels in a header rather than in each body, because almost every
// mutation here needs one and threading it through sixty request structs would
// guarantee one of them forgets. The service refuses a change that requires a
// reason and did not get one.
func actorFrom(request *http.Request) panelpg.Actor {
	principal, _ := PrincipalFrom(request.Context())
	return panelpg.Actor{
		AdminID:   principal.Account.ID,
		Type:      "admin",
		RequestID: middleware.GetReqID(request.Context()),
		Reason:    strings.TrimSpace(request.Header.Get("X-Operator-Reason")),
	}
}

// operationsError maps a service failure onto an RFC 9457 problem response.
//
// It is one function rather than a switch in each handler so that "not found",
// "refused in this state", and "invalid input" cannot drift into different
// status codes on different endpoints.
func (handlers *AdminHandlers) operationsError(
	writer http.ResponseWriter, request *http.Request, err error,
) {
	switch {
	case errors.Is(err, panelpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That record does not exist")
	case errors.Is(err, panelpg.ErrConflict):
		writeProblem(writer, request, http.StatusConflict, "conflict", "That record already exists")
	case errors.Is(err, panelpg.ErrRejected):
		writeProblem(
			writer, request, http.StatusConflict,
			"state_conflict", "That is not allowed while the record is in its current state",
		)
	case errors.Is(err, panelpg.ErrValidaton):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"invalid_request", "Those details are not valid, or a reason is required",
		)
	default:
		handlers.logger.Error("panel operation failed", "path", request.URL.Path, "error", err)
		writeProblem(
			writer, request, http.StatusInternalServerError,
			"operation_failed", "That operation could not be completed",
		)
	}
}

// respond writes a successful result, or the mapped problem when one occurred.
func (handlers *AdminHandlers) respond(
	writer http.ResponseWriter, request *http.Request, value any, err error,
) {
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

// query reads a trimmed query parameter.
func query(request *http.Request, name string) string {
	return strings.TrimSpace(request.URL.Query().Get(name))
}

// queryInt reads an integer query parameter, defaulting on anything unusable.
// A malformed page size is a caller mistake that should still return a page.
func queryInt(request *http.Request, name string) int32 {
	value, err := strconv.Atoi(query(request, name))
	if err != nil || value < 0 {
		return 0
	}
	return int32(value)
}

// queryTime reads an RFC 3339 instant, or nil when absent or unparseable.
func queryTime(request *http.Request, name string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, query(request, name))
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

// queryBool reads a tri-state flag: absent means "no filter".
func queryBool(request *http.Request, name string) *bool {
	switch query(request, name) {
	case "true":
		value := true
		return &value
	case "false":
		value := false
		return &value
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) dashboard(writer http.ResponseWriter, request *http.Request) {
	dashboard, err := handlers.operations.Dashboard(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// The window is published in seconds beside the numbers, because a metric
	// whose period the reader has to guess is a metric they will misread.
	writeJSON(writer, http.StatusOK, map[string]any{
		"windowSeconds": int64(dashboard.Window.Seconds()),
		"generatedAt":   dashboard.GeneratedAt,
		"timezone":      dashboard.Timezone,
		"customers":     dashboard.Customers,
		"subscriptions": dashboard.Subscriptions,
		"payments":      dashboard.Payments,
		"revenue":       dashboard.Revenue,
		"support":       dashboard.Support,
		"operations":    dashboard.Operations,
		"attention":     dashboard.Attention,
	})
}

func (handlers *AdminHandlers) recentIncidents(writer http.ResponseWriter, request *http.Request) {
	events, err := handlers.operations.RecentIncidents(request.Context(), queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{"items": events}, err)
}

// dependencyHealth reports the live dependency probes beside what each
// configured provider last told us.
//
// The probes are the same registry /readyz reports on, so the panel and the
// load balancer can never disagree about whether PostgreSQL is up. A provider's
// status comes from the database instead, because "configured but never
// reached" is a different problem from "reached and failing" and a live probe
// would collapse the two.
func (handlers *AdminHandlers) dependencyHealth(writer http.ResponseWriter, request *http.Request) {
	dependencies := []platform.Check{}
	healthy := true
	if handlers.health != nil {
		dependencies, healthy = handlers.health.Report(request.Context())
	}

	providers, err := handlers.operations.ProviderHealth(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	goodsProviders, err := handlers.operations.GoodsProviderHealth(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"healthy": healthy, "dependencies": dependencies,
		"paymentProviders": providers, "goodsProviders": goodsProviders,
	})
}

func (handlers *AdminHandlers) maintenanceState(writer http.ResponseWriter, request *http.Request) {
	state, err := handlers.operations.MaintenanceState(request.Context())
	handlers.respond(writer, request, state, err)
}

// setMaintenance switches maintenance mode manually.
//
// A manual change is always recorded as manual, never as automatic detection:
// the two are cleared by different things, and mislabelling one would leave an
// operator waiting for a recovery that will not come.
func (handlers *AdminHandlers) setMaintenance(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Active           bool       `json:"active"`
		NoticeRU         string     `json:"noticeRu"`
		NoticeEN         string     `json:"noticeEn"`
		ExpectedReturnAt *time.Time `json:"expectedReturnAt"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	state, err := handlers.operations.SetMaintenance(request.Context(), panelpg.MaintenanceInput{
		Active: body.Active, NoticeRU: body.NoticeRU, NoticeEN: body.NoticeEN,
		ExpectedReturnAt: body.ExpectedReturnAt,
	}, actorFrom(request))
	handlers.respond(writer, request, state, err)
}

// ---------------------------------------------------------------------------
// Customers
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) searchCustomers(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchCustomers(request.Context(), panelpg.CustomerFilter{
		Status:   query(request, "status"),
		Segment:  query(request, "segment"),
		Query:    query(request, "q"),
		Cursor:   query(request, "cursor"),
		PageSize: queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) customerProfile(writer http.ResponseWriter, request *http.Request) {
	profile, err := handlers.operations.CustomerProfile(request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, profile, err)
}

func (handlers *AdminHandlers) customerSubscriptions(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerSubscriptions(
		request.Context(), chi.URLParam(request, "customerID"),
	)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) customerOrders(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerOrders(
		request.Context(), chi.URLParam(request, "customerID"), queryInt(request, "pageSize"),
	)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) customerWallet(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerWallet(
		request.Context(), chi.URLParam(request, "customerID"), queryInt(request, "pageSize"),
	)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) customerTickets(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerTickets(
		request.Context(), chi.URLParam(request, "customerID"), queryInt(request, "pageSize"),
	)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) customerConsents(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerConsents(request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) setCustomerStatus(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	profile, err := handlers.operations.SetCustomerStatus(
		request.Context(), chi.URLParam(request, "customerID"), body.Status, actorFrom(request),
	)
	handlers.respond(writer, request, profile, err)
}

// anonymizeCustomer scrubs identifying data while keeping financial history.
//
// The two cannot be separated: an order that funded a ledger entry cannot be
// removed without unbalancing the ledger, so anonymisation removes who placed
// it and keeps that it was placed.
func (handlers *AdminHandlers) anonymizeCustomer(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.AnonymizeCustomer(
		request.Context(), chi.URLParam(request, "customerID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// requestCustomerDeletion marks a customer for deletion under the retention
// policy.
//
// It is a request rather than an act. The retention worker removes the record
// once its window elapses, which gives an operator time to discover a mistake
// and keeps a deletion from tearing a hole in the ledger mid-transaction.
func (handlers *AdminHandlers) requestCustomerDeletion(writer http.ResponseWriter, request *http.Request) {
	profile, err := handlers.operations.RequestCustomerDeletion(
		request.Context(), chi.URLParam(request, "customerID"), actorFrom(request),
	)
	handlers.respond(writer, request, profile, err)
}

func (handlers *AdminHandlers) renameSubscription(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.RenameSubscription(
		request.Context(), chi.URLParam(request, "customerID"),
		chi.URLParam(request, "subscriptionID"), body.Label, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) customerPaymentMethods(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerPaymentMethods(
		request.Context(), chi.URLParam(request, "customerID"),
	)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) customerCharges(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerCharges(
		request.Context(), chi.URLParam(request, "customerID"), queryInt(request, "pageSize"),
	)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) revokePaymentMethod(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RevokePaymentMethod(
		request.Context(), chi.URLParam(request, "customerID"),
		chi.URLParam(request, "methodID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Catalogue
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) listPlans(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListPlans(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) planDetail(writer http.ResponseWriter, request *http.Request) {
	detail, err := handlers.operations.PlanDetail(request.Context(), chi.URLParam(request, "planID"))
	handlers.respond(writer, request, detail, err)
}

func (handlers *AdminHandlers) updatePlan(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Visible       bool   `json:"visible"`
		SortOrder     int32  `json:"sortOrder"`
		MaxConcurrent *int32 `json:"maxConcurrentPerCustomer"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.UpdatePlanPresentation(
		request.Context(), chi.URLParam(request, "planID"),
		panelpg.PlanPresentation{
			Visible: body.Visible, SortOrder: body.SortOrder, MaxConcurrent: body.MaxConcurrent,
		},
		actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) archivePlan(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Archived bool `json:"archived"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.ArchivePlan(
		request.Context(), chi.URLParam(request, "planID"), body.Archived, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) savePlanLocalization(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SavePlanLocalization(
		request.Context(), chi.URLParam(request, "planID"), chi.URLParam(request, "locale"),
		body.Name, body.Description, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) retirePlanVersion(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RetirePlanVersion(
		request.Context(), chi.URLParam(request, "planVersionID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) listAddons(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListAddons(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) addonVersions(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.AddonVersions(request.Context(), chi.URLParam(request, "addonID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) listPromotions(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListPromotions(request.Context(), panelpg.PromotionFilter{
		Active:   queryBool(request, "active"),
		Kind:     query(request, "kind"),
		PageSize: queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) updatePromotion(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Value           int64           `json:"value"`
		Currency        string          `json:"currency"`
		StartsAt        *time.Time      `json:"startsAt"`
		EndsAt          *time.Time      `json:"endsAt"`
		RedemptionLimit *int32          `json:"redemptionLimit"`
		PerCustomer     int32           `json:"perCustomerLimit"`
		Eligibility     json.RawMessage `json:"eligibility"`
		Active          bool            `json:"active"`
		Stackable       bool            `json:"stackable"`
		Precedence      int32           `json:"precedence"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.UpdatePromotion(
		request.Context(), chi.URLParam(request, "promotionID"),
		panelpg.PromotionUpdate{
			Value: body.Value, Currency: body.Currency,
			StartsAt: body.StartsAt, EndsAt: body.EndsAt,
			RedemptionLimit: body.RedemptionLimit, PerCustomer: body.PerCustomer,
			Eligibility: body.Eligibility, Active: body.Active,
			Stackable: body.Stackable, Precedence: body.Precedence,
		},
		actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) listPromoCodes(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListPromoCodes(request.Context(), chi.URLParam(request, "promotionID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) setPromoCodeActive(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Active bool `json:"active"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SetPromoCodeActive(
		request.Context(), chi.URLParam(request, "promoCodeID"), body.Active, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Personal offers
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) searchOffers(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchPersonalOffers(request.Context(), panelpg.OfferFilter{
		Status:     query(request, "status"),
		CustomerID: query(request, "customerId"),
		Cursor:     query(request, "cursor"),
		PageSize:   queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) createOffer(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CustomerID  string    `json:"customerId"`
		PromotionID string    `json:"promotionId"`
		PlanID      string    `json:"planId"`
		TitleRU     string    `json:"titleRu"`
		TitleEN     string    `json:"titleEn"`
		TermsRU     string    `json:"termsRu"`
		TermsEN     string    `json:"termsEn"`
		StartsAt    time.Time `json:"startsAt"`
		ExpiresAt   time.Time `json:"expiresAt"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.StartsAt.IsZero() {
		body.StartsAt = time.Now().UTC()
	}
	offer, err := handlers.operations.CreatePersonalOffer(request.Context(), panelpg.OfferInput{
		CustomerID: body.CustomerID, PromotionID: body.PromotionID, PlanID: body.PlanID,
		TitleRU: body.TitleRU, TitleEN: body.TitleEN,
		TermsRU: body.TermsRU, TermsEN: body.TermsEN,
		StartsAt: body.StartsAt, ExpiresAt: body.ExpiresAt,
	}, actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, offer)
}

func (handlers *AdminHandlers) revokeOffer(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RevokePersonalOffer(
		request.Context(), chi.URLParam(request, "offerID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Finance
// ---------------------------------------------------------------------------

func orderFilterFrom(request *http.Request) panelpg.OrderFilter {
	return panelpg.OrderFilter{
		State:      query(request, "state"),
		Operation:  query(request, "operation"),
		CustomerID: query(request, "customerId"),
		Currency:   query(request, "currency"),
		From:       queryTime(request, "from"),
		To:         queryTime(request, "to"),
		Cursor:     query(request, "cursor"),
		PageSize:   queryInt(request, "pageSize"),
	}
}

func (handlers *AdminHandlers) searchOrders(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchOrders(request.Context(), orderFilterFrom(request))
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) orderDetail(writer http.ResponseWriter, request *http.Request) {
	detail, err := handlers.operations.OrderDetail(request.Context(), chi.URLParam(request, "orderID"))
	handlers.respond(writer, request, detail, err)
}

func (handlers *AdminHandlers) stuckPayments(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.StuckPayments(request.Context(), queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) failedCharges(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.FailedCharges(
		request.Context(), query(request, "cursor"), queryInt(request, "pageSize"),
	)
	handlers.respond(writer, request, page, err)
}

// exportFinance streams the filtered orders as CSV.
//
// It walks the cursor server-side and flushes as it goes, so an export over a
// year of orders never has to be held in memory. The page count is bounded for
// the same reason the audit export is: one request must not be able to walk an
// unbounded table forever.
func (handlers *AdminHandlers) exportFinance(writer http.ResponseWriter, request *http.Request) {
	filter := orderFilterFrom(request)
	filter.PageSize = panelpg.MaxPageSize

	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="orders.csv"`)
	writer.WriteHeader(http.StatusOK)

	header := make([]string, 0, len(panelpg.FinanceExportHeader))
	for _, column := range panelpg.FinanceExportHeader {
		header = append(header, csvField(column))
	}
	_, _ = writer.Write([]byte(strings.Join(header, ",") + "\n"))

	const maxPages = 200
	for page := 0; page < maxPages; page++ {
		rows, next, err := handlers.operations.ExportFinance(request.Context(), filter)
		if err != nil {
			handlers.logger.Error("finance export failed", "error", err)
			return
		}
		for _, row := range rows {
			fields := row.Fields()
			encoded := make([]string, 0, len(fields))
			for _, field := range fields {
				encoded = append(encoded, csvField(field))
			}
			_, _ = writer.Write([]byte(strings.Join(encoded, ",") + "\n"))
		}
		if next == "" {
			return
		}
		filter.Cursor = next
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (handlers *AdminHandlers) reconcilePayment(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Outcome string `json:"outcome"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.RecordReconciliation(
		request.Context(), chi.URLParam(request, "paymentIntentID"), body.Outcome, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

func (handlers *AdminHandlers) recordRefund(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		RefundID    string `json:"refundId"`
		AmountMinor int64  `json:"amountMinor"`
		Currency    string `json:"currency"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.RecordRefund(
		request.Context(), chi.URLParam(request, "orderID"), body.RefundID,
		body.AmountMinor, body.Currency, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

// ---------------------------------------------------------------------------
// Fulfillment and jobs
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) searchFulfillment(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchFulfillment(request.Context(), panelpg.FulfillmentFilter{
		Status:        query(request, "status"),
		Operation:     query(request, "operation"),
		EntitlementID: query(request, "entitlementId"),
		Cursor:        query(request, "cursor"),
		PageSize:      queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) fulfillmentHistory(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.FulfillmentHistory(request.Context(), chi.URLParam(request, "operationID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) retryFulfillment(writer http.ResponseWriter, request *http.Request) {
	operation, err := handlers.operations.RetryFulfillment(
		request.Context(), chi.URLParam(request, "operationID"), actorFrom(request),
	)
	handlers.respond(writer, request, operation, err)
}

func (handlers *AdminHandlers) cancelFulfillment(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.CancelFulfillment(
		request.Context(), chi.URLParam(request, "operationID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) searchWebhooks(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchWebhooks(request.Context(), panelpg.WebhookFilter{
		Provider: query(request, "provider"),
		Status:   query(request, "status"),
		Cursor:   query(request, "cursor"),
		PageSize: queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) replayWebhook(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.ReplayWebhook(
		request.Context(), chi.URLParam(request, "eventID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

func (handlers *AdminHandlers) outboxBacklog(writer http.ResponseWriter, request *http.Request) {
	entries, err := handlers.operations.OutboxBacklog(request.Context(), queryInt(request, "pageSize"))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, map[string]any{
			"id": entry.ID, "topic": entry.Topic, "occurredAt": entry.OccurredAt,
			"ageSeconds": int64(entry.Age.Seconds()),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) openDrifts(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.OpenDrifts(request.Context(), queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) commerceSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.operations.CommerceSettings(request.Context())
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) saveTopUpSettings(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.TopUpSettings
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.operations.SaveTopUpSettings(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) saveSubscriptionSettings(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.SubscriptionSettings
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.operations.SaveSubscriptionSettings(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, settings, err)
}

// listProviderSettings merges the stored configuration with what each compiled-in
// adapter actually declares.
//
// The database records what the operator chose; the adapter records what the
// integration can do. Showing them together is what lets the panel explain why
// a recurring switch is unavailable rather than simply refusing the save.
func (handlers *AdminHandlers) listProviderSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.operations.ListProviderSettings(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	for index := range settings {
		settings[index].AdapterRecurring = handlers.adapterRecurring[settings[index].Provider]
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": settings, "adapters": handlers.adapterRecurring,
	})
}

func (handlers *AdminHandlers) saveProviderSettings(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		MerchantID    string  `json:"merchantId"`
		Enabled       bool    `json:"enabled"`
		DisplayOrder  int32   `json:"displayOrder"`
		Credentials   *string `json:"credentials"`
		WebhookSecret *string `json:"webhookSecret"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SaveProviderSettings(request.Context(), panelpg.ProviderSettingsInput{
		Provider: chi.URLParam(request, "provider"), MerchantID: body.MerchantID,
		Enabled: body.Enabled, DisplayOrder: body.DisplayOrder,
		Credentials: body.Credentials, WebhookSecret: body.WebhookSecret,
	}, actorFrom(request))
	if err == nil {
		saved.AdapterRecurring = handlers.adapterRecurring[saved.Provider]
	}
	handlers.respond(writer, request, saved, err)
}

// testProviderConnection asks the adapter to verify its own credentials.
//
// The probe is a read the provider's API already offers, so a test can never
// create a payment. Three outcomes are distinguished because they need
// different responses from an operator: the credentials work, the credentials
// are rejected, or this adapter has no way to tell — the last is a limitation
// to be recorded honestly, not a failure to be shown as one.
func (handlers *AdminHandlers) testProviderConnection(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		MerchantID string `json:"merchantId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	name := chi.URLParam(request, "provider")
	adapter, configured := handlers.providers[name]
	if !configured {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"provider_not_loaded", "This provider is not configured in this deployment",
		)
		return
	}

	// The probe talks to a third party, so it gets its own deadline rather than
	// borrowing the request's: an unresponsive provider must not hold an
	// operator's connection open for as long as the server would allow.
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()

	status, errorCode := "passed", ""
	switch err := payments.Probe(ctx, adapter); {
	case err == nil:
	case errors.Is(err, payments.ErrUnsupported):
		status = "unsupported"
	default:
		status, errorCode = "failed", probeErrorCode(err)
		handlers.logger.WarnContext(
			request.Context(), "provider connection test failed",
			slog.String("provider", name), slog.String("error", err.Error()),
		)
	}

	saved, err := handlers.operations.RecordConnectionCheck(
		request.Context(), name, body.MerchantID, status, errorCode, actorFrom(request),
	)
	if err == nil {
		saved.AdapterRecurring = handlers.adapterRecurring[name]
	}
	handlers.respond(writer, request, saved, err)
}

// probeErrorCode reduces an adapter failure to a stable, non-secret label.
//
// The provider's own message can carry a merchant identifier or a fragment of a
// key, and it is already in the server log; what the panel stores and shows is
// the class of failure.
func probeErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, payments.ErrProviderResponse):
		return "rejected"
	default:
		return "unreachable"
	}
}

// configureRecurring records a capability test and, only on a pass, may enable
// automatic charging.
//
// The adapter's own declaration is checked first: an operator cannot enable
// recurring for an integration that has no way to bind a payment method,
// however the test was reported.
func (handlers *AdminHandlers) configureRecurring(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		MerchantID string `json:"merchantId"`
		Passed     bool   `json:"passed"`
		Enable     bool   `json:"enable"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	provider := chi.URLParam(request, "provider")
	if body.Enable && !handlers.adapterRecurring[provider] {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"recurring_unsupported", "This payment adapter cannot store a payment method",
		)
		return
	}
	saved, err := handlers.operations.RecordRecurringTest(
		request.Context(), provider, body.MerchantID, body.Passed, body.Enable, actorFrom(request),
	)
	if err == nil {
		saved.AdapterRecurring = handlers.adapterRecurring[provider]
	}
	handlers.respond(writer, request, saved, err)
}

func (handlers *AdminHandlers) customerReferrals(writer http.ResponseWriter, request *http.Request) {
	summary, err := handlers.operations.CustomerReferrals(
		request.Context(), chi.URLParam(request, "customerID"), int32(queryInt(request, "pageSize")),
	)
	handlers.respond(writer, request, summary, err)
}

// createPlanVersion publishes the next version of a plan.
//
// There is no update route on purpose. A plan version is immutable once an
// order references it, so an editor that could change one would silently
// re-price history; publishing the next version leaves the old one costing what
// it cost.
func (handlers *AdminHandlers) createPlanVersion(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		BillingPeriod         string           `json:"billingPeriod"`
		DurationSeconds       int64            `json:"durationSeconds"`
		TrafficAllowanceBytes *int64           `json:"trafficAllowanceBytes"`
		DeviceLimit           *int32           `json:"deviceLimit"`
		SquadIDs              []string         `json:"squadIds"`
		SquadSelection        string           `json:"squadSelection"`
		MinSelectableSquads   int32            `json:"minSelectableSquads"`
		MaxSelectableSquads   *int32           `json:"maxSelectableSquads"`
		UpgradePolicy         string           `json:"upgradePolicy"`
		DowngradePolicy       string           `json:"downgradePolicy"`
		CancellationPolicy    string           `json:"cancellationPolicy"`
		GracePeriodSeconds    int64            `json:"gracePeriodSeconds"`
		TrialEligibility      string           `json:"trialEligibility"`
		RecurringCapable      bool             `json:"recurringCapable"`
		Prices                map[string]int64 `json:"prices"`
		AddonIDs              []string         `json:"addonIds"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	version, err := handlers.operations.CreatePlanVersion(
		request.Context(), chi.URLParam(request, "planID"),
		panelpg.PlanVersionInput{
			BillingPeriod: body.BillingPeriod, DurationSeconds: body.DurationSeconds,
			TrafficAllowanceBytes: body.TrafficAllowanceBytes, DeviceLimit: body.DeviceLimit,
			SquadIDs: body.SquadIDs, SquadSelection: body.SquadSelection,
			MinSelectableSquads: body.MinSelectableSquads,
			MaxSelectableSquads: body.MaxSelectableSquads,
			UpgradePolicy:       body.UpgradePolicy, DowngradePolicy: body.DowngradePolicy,
			CancellationPolicy: body.CancellationPolicy,
			GracePeriodSeconds: body.GracePeriodSeconds,
			TrialEligibility:   body.TrialEligibility, RecurringCapable: body.RecurringCapable,
			Prices: body.Prices, AddonIDs: body.AddonIDs,
		}, actorFrom(request),
	)
	handlers.respond(writer, request, version, err)
}

// saveAddon creates or updates an add-on and publishes a new version of it.
func (handlers *AdminHandlers) saveAddon(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Code          string           `json:"code"`
		Kind          string           `json:"kind"`
		Visible       bool             `json:"visible"`
		SortOrder     int32            `json:"sortOrder"`
		NameEN        string           `json:"nameEn"`
		NameRU        string           `json:"nameRu"`
		DescriptionEN string           `json:"descriptionEn"`
		DescriptionRU string           `json:"descriptionRu"`
		TrafficBytes  *int64           `json:"trafficBytes"`
		DeviceSlots   *int32           `json:"deviceSlots"`
		SquadIDs      []string         `json:"squadIds"`
		MaxQuantity   int32            `json:"maxQuantity"`
		Proration     string           `json:"proration"`
		Prices        map[string]int64 `json:"prices"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	addonID, err := handlers.operations.SaveAddon(request.Context(), panelpg.AddonInput{
		Code: body.Code, Kind: body.Kind, Visible: body.Visible, SortOrder: body.SortOrder,
		NameEN: body.NameEN, NameRU: body.NameRU,
		DescriptionEN: body.DescriptionEN, DescriptionRU: body.DescriptionRU,
		TrafficBytes: body.TrafficBytes, DeviceSlots: body.DeviceSlots,
		SquadIDs: body.SquadIDs, MaxQuantity: body.MaxQuantity,
		Proration: body.Proration, Prices: body.Prices,
	}, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"id": addonID}, err)
}

func (handlers *AdminHandlers) retireAddonVersion(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RetireAddonVersion(
		request.Context(), chi.URLParam(request, "addonVersionID"), actorFrom(request),
	)
	handlers.respond(writer, request, map[string]any{"retired": true}, err)
}

// ---------------------------------------------------------------------------
// Risk
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) listBlocklistSources(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListBlocklistSources(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) saveBlocklistSource(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Slug            string  `json:"slug"`
		DisplayName     string  `json:"displayName"`
		SubjectKind     string  `json:"subjectKind"`
		URL             string  `json:"url"`
		AuthHeader      *string `json:"authHeader"`
		Enabled         bool    `json:"enabled"`
		RefreshInterval int64   `json:"refreshIntervalSeconds"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SaveBlocklistSource(request.Context(), panelpg.BlocklistSourceInput{
		Slug: body.Slug, DisplayName: body.DisplayName, SubjectKind: body.SubjectKind,
		URL: body.URL, AuthHeader: body.AuthHeader, Enabled: body.Enabled,
		RefreshInterval: body.RefreshInterval,
	}, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

func (handlers *AdminHandlers) deleteBlocklistSource(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteBlocklistSource(
		request.Context(), chi.URLParam(request, "sourceID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) searchMatches(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchBlocklistMatches(request.Context(), panelpg.MatchFilter{
		Status:     query(request, "status"),
		CustomerID: query(request, "customerId"),
		Cursor:     query(request, "cursor"),
		PageSize:   queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) customerMatches(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.CustomerMatches(request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) decideMatch(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Decision string `json:"decision"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	match, err := handlers.operations.DecideBlocklistMatch(
		request.Context(), chi.URLParam(request, "matchID"), body.Decision, actorFrom(request),
	)
	handlers.respond(writer, request, match, err)
}

func (handlers *AdminHandlers) appealMatch(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.AppealBlocklistMatch(
		request.Context(), chi.URLParam(request, "matchID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// setAllowlisted serves both the PUT that adds an override and the DELETE that
// removes it, because they differ only in one boolean and the audit action.
func (handlers *AdminHandlers) setAllowlisted(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.SetAllowlisted(
		request.Context(), chi.URLParam(request, "customerID"),
		request.Method == http.MethodPut, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) listAnomalyRules(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListAnomalyRules(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) saveAnomalyRule(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled        bool  `json:"enabled"`
		WindowSeconds  int64 `json:"windowSeconds"`
		WarnThreshold  int64 `json:"warnThreshold"`
		AlertThreshold int64 `json:"alertThreshold"`
		MinimumSample  int32 `json:"minimumSample"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SaveAnomalyRule(request.Context(), panelpg.AnomalyRule{
		Metric: chi.URLParam(request, "metric"), Enabled: body.Enabled,
		WindowSeconds: body.WindowSeconds, WarnThreshold: body.WarnThreshold,
		AlertThreshold: body.AlertThreshold, MinimumSample: body.MinimumSample,
	}, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

func (handlers *AdminHandlers) searchAnomalies(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchAnomalySignals(request.Context(), panelpg.SignalFilter{
		Status:   query(request, "status"),
		Metric:   query(request, "metric"),
		Severity: query(request, "severity"),
		Cursor:   query(request, "cursor"),
		PageSize: queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) reviewAnomaly(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	signal, err := handlers.operations.ReviewAnomalySignal(
		request.Context(), chi.URLParam(request, "signalID"), body.Status, actorFrom(request),
	)
	handlers.respond(writer, request, signal, err)
}

// ---------------------------------------------------------------------------
// Gifts
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) searchGifts(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchGifts(request.Context(), panelpg.GiftFilter{
		Status:   query(request, "status"),
		Kind:     query(request, "kind"),
		SenderID: query(request, "senderId"),
		Cursor:   query(request, "cursor"),
		PageSize: queryInt(request, "pageSize"),
	})
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	totals, err := handlers.operations.GiftTotals(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": page.Items, "nextCursor": page.NextCursor, "totals": totals,
	})
}

func (handlers *AdminHandlers) revokeGift(writer http.ResponseWriter, request *http.Request) {
	gift, err := handlers.operations.RevokeGift(
		request.Context(), chi.URLParam(request, "giftID"), actorFrom(request),
	)
	handlers.respond(writer, request, gift, err)
}

func (handlers *AdminHandlers) refundGift(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.MarkGiftRefunded(
		request.Context(), chi.URLParam(request, "giftID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Digital goods
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) listGoodsProviders(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListGoodsProviders(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) saveGoodsProvider(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled             bool    `json:"enabled"`
		Credentials         *string `json:"credentials"`
		LowBalanceThreshold *int64  `json:"lowBalanceThresholdMinor"`
		SpendLimitMinor     int64   `json:"spendLimitMinor"`
		SpendWindowSeconds  int64   `json:"spendWindowSeconds"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SaveGoodsProvider(request.Context(), panelpg.GoodsProviderInput{
		Slug: chi.URLParam(request, "slug"), Enabled: body.Enabled,
		Credentials: body.Credentials, LowBalanceThreshold: body.LowBalanceThreshold,
		SpendLimitMinor: body.SpendLimitMinor, SpendWindowSeconds: body.SpendWindowSeconds,
	}, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

func (handlers *AdminHandlers) listGoodsProducts(writer http.ResponseWriter, request *http.Request) {
	includeArchived := query(request, "includeArchived") == "true"
	items, err := handlers.operations.ListGoodsProducts(request.Context(), includeArchived)
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) createGoodsProduct(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Code           string `json:"code"`
		ProviderSlug   string `json:"providerSlug"`
		Kind           string `json:"kind"`
		DurationMonths *int32 `json:"durationMonths"`
		StarQuantity   *int32 `json:"starQuantity"`
		Visible        bool   `json:"visible"`
		SortOrder      int32  `json:"sortOrder"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	product, err := handlers.operations.CreateGoodsProduct(request.Context(), panelpg.GoodsProductInput{
		Code: body.Code, ProviderSlug: body.ProviderSlug, Kind: body.Kind,
		DurationMonths: body.DurationMonths, StarQuantity: body.StarQuantity,
		Visible: body.Visible, SortOrder: body.SortOrder,
	}, actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, product)
}

func (handlers *AdminHandlers) updateGoodsProduct(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Visible   bool  `json:"visible"`
		SortOrder int32 `json:"sortOrder"`
		Archived  bool  `json:"archived"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.UpdateGoodsProduct(
		request.Context(), chi.URLParam(request, "productID"),
		body.Visible, body.SortOrder, body.Archived, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) saveGoodsLocalization(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SaveGoodsLocalization(
		request.Context(), chi.URLParam(request, "productID"), chi.URLParam(request, "locale"),
		body.Name, body.Description, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) saveGoodsPricing(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.GoodsPricing
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SaveGoodsPricing(
		request.Context(), chi.URLParam(request, "productID"), body, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) searchGoodsOrders(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchGoodsOrders(request.Context(), panelpg.GoodsOrderFilter{
		Status:     query(request, "status"),
		CustomerID: query(request, "customerId"),
		Cursor:     query(request, "cursor"),
		PageSize:   queryInt(request, "pageSize"),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) goodsDeliveryHistory(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.GoodsDeliveryHistory(request.Context(), chi.URLParam(request, "orderID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) goodsReviewQueue(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.GoodsReviewQueue(request.Context(), queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

// resolveGoodsDelivery records an operator's verdict on a delivery whose
// outcome nobody could resolve automatically.
//
// The evidence lives outside Omniflow — the operator checks with the provider —
// so this records what they found rather than deciding anything itself. Saying
// it was not delivered releases the ordinary refund path.
func (handlers *AdminHandlers) resolveGoodsDelivery(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Delivered bool `json:"delivered"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.ResolveGoodsDelivery(
		request.Context(), chi.URLParam(request, "orderID"), body.Delivered, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) cancelGoodsDelivery(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.CancelGoodsDelivery(
		request.Context(), chi.URLParam(request, "orderID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Bulk actions
// ---------------------------------------------------------------------------

func (handlers *AdminHandlers) listBulkOperations(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.ListBulkOperations(request.Context(), queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) previewBulk(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Kind    string `json:"kind"`
		Targets []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"targets"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}

	// The idempotency key is a request header, matching every other mutation in
	// the product, so a double-submitted form returns the preview it already
	// created rather than a second one.
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"idempotency_key_required", "A bulk action requires an Idempotency-Key header",
		)
		return
	}

	targets := make([]panelpg.BulkTarget, 0, len(body.Targets))
	for _, target := range body.Targets {
		targets = append(targets, panelpg.BulkTarget{Type: target.Type, ID: target.ID})
	}
	operation, err := handlers.operations.PreviewBulkOperation(request.Context(), panelpg.BulkInput{
		Kind: body.Kind, Targets: targets, Parameters: body.Parameters, IdempotencyKey: key,
	}, actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, operation)
}

func (handlers *AdminHandlers) startBulk(writer http.ResponseWriter, request *http.Request) {
	operation, err := handlers.operations.StartBulkOperation(
		request.Context(), chi.URLParam(request, "operationID"), actorFrom(request),
	)
	handlers.respond(writer, request, operation, err)
}

func (handlers *AdminHandlers) cancelBulk(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.CancelBulkOperation(
		request.Context(), chi.URLParam(request, "operationID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) bulkItems(writer http.ResponseWriter, request *http.Request) {
	operationID := chi.URLParam(request, "operationID")
	operation, err := handlers.operations.BulkOperation(request.Context(), operationID)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	items, err := handlers.operations.BulkItems(request.Context(), operationID, queryInt(request, "pageSize"))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"operation": operation, "items": items})
}

// uuidValue renders a pgtype UUID for a response body.
func uuidValue(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// adapterCapabilities reduces the configured payment adapters to the one fact
// the panel needs about each: whether it can bind a payment method for
// automatic renewal.
//
// It is computed once at construction from the compiled-in adapters, so the
// panel and the enforcement path cannot disagree about what a provider can do.
func adapterCapabilities(providers map[string]payments.Provider) map[string]bool {
	capabilities := make(map[string]bool, len(providers))
	for name, provider := range providers {
		capabilities[name] = provider.Capabilities().Recurring
	}
	return capabilities
}

// ensure the context import is used by the file's helper signatures.
var _ = context.Background
