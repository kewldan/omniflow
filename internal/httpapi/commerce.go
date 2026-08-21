package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/catalogpg"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/customerpg"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/platform"
)

const maxJSONBody = 1 << 20

type CommerceHandlers struct {
	queries       *dbgen.Queries
	catalog       *catalogpg.Service
	commerce      *commercepg.Store
	payments      *paymentservice.Service
	customers     *customerpg.Service
	imports       *importservice.Service
	fulfillment   *fulfillment.Service
	promoLimiter  *platform.RateLimiter
	operatorToken string
	operations    *operations
}

func NewCommerceHandlers(queries *dbgen.Queries, catalogService *catalogpg.Service, commerceStore *commercepg.Store, paymentService *paymentservice.Service, customerService *customerpg.Service, importService *importservice.Service, fulfillmentService *fulfillment.Service, promoLimiter *platform.RateLimiter, operatorToken string) *CommerceHandlers {
	return &CommerceHandlers{queries: queries, catalog: catalogService, commerce: commerceStore, payments: paymentService, customers: customerService, imports: importService, fulfillment: fulfillmentService, promoLimiter: promoLimiter, operatorToken: operatorToken}
}

func (handlers *CommerceHandlers) Mount(router chi.Router) {
	router.Get("/v1/catalog/plans", handlers.listPlans)
	router.Post("/v1/payments/webhooks/{provider}", handlers.paymentWebhook)
	router.Route("/v1/admin", func(admin chi.Router) {
		admin.Use(handlers.operatorAuth)
		admin.Post("/plans", handlers.createPlan)
		admin.Post("/plans/{planID}/versions", handlers.createPlanVersion)
		admin.Post("/promotions", handlers.createPromotion)
		admin.Patch("/customers/{customerID}", handlers.updateCustomer)
		admin.Post("/customers/{customerID}/identities", handlers.linkIdentity)
		admin.Delete("/customers/{customerID}/identities/{identityID}", handlers.unlinkIdentity)
		admin.Post("/customers/{customerID}/contacts", handlers.createContact)
		admin.Post("/customers/{customerID}/consents", handlers.recordConsent)
		admin.Post("/customers/{customerID}/lifecycle", handlers.customerLifecycle)
		admin.Post("/imports/remnawave/preview", handlers.previewImport)
		admin.Post("/imports/remnawave/{importID}/resume", handlers.resumeImport)
		admin.Post("/imports/remnawave/{importID}/apply", handlers.applyImport)
		admin.Post("/orders", handlers.createOrder)
		admin.Get("/orders/{orderID}", handlers.getOrder)
		admin.Post("/orders/{orderID}/cancel", handlers.cancelOrder)
		admin.Post("/orders/{orderID}/payments", handlers.createPayment)
		admin.Post("/payments/{paymentID}/reconcile", handlers.reconcilePayment)
		admin.Post("/payments/{paymentID}/refunds", handlers.refundPayment)
		admin.Post("/payments/{paymentID}/manual-decision", handlers.manualDecision)
		admin.Post("/customers/{customerID}/wallet-adjustments", handlers.adjustWallet)
		admin.Get("/fulfillment/drifts", handlers.listFulfillmentDrifts)
		admin.Post("/entitlements/{entitlementID}/operations", handlers.enqueueFulfillmentOperation)
		handlers.mountOperations(admin)
	})
}

func (handlers *CommerceHandlers) enqueueFulfillmentOperation(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Operation             string     `json:"operation"`
		CorrelationID         string     `json:"correlationId"`
		EffectiveAt           *time.Time `json:"effectiveAt"`
		EndsAt                *time.Time `json:"endsAt"`
		TrafficAllowanceBytes *int64     `json:"trafficAllowanceBytes"`
		DeviceLimit           *int32     `json:"deviceLimit"`
		SquadIDs              []string   `json:"squadIDs"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	key := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(key) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	correlationID := body.CorrelationID
	if correlationID == "" {
		correlationID = middlewareRequestID(request)
	}
	operation, err := handlers.fulfillment.Enqueue(request.Context(), chi.URLParam(request, "entitlementID"), fulfillment.OperationInput{Operation: body.Operation, IdempotencyKey: key, CorrelationID: correlationID, EffectiveAt: body.EffectiveAt, EndsAt: body.EndsAt, TrafficAllowanceBytes: body.TrafficAllowanceBytes, DeviceLimit: body.DeviceLimit, SquadIDs: body.SquadIDs})
	if err != nil {
		writeProblem(writer, request, 422, "fulfillment_operation_rejected", err.Error())
		return
	}
	writeJSON(writer, 202, map[string]any{"id": uuidString(operation.ID), "entitlementId": uuidString(operation.EntitlementID), "operation": operation.Operation, "status": operation.Status})
}

func (handlers *CommerceHandlers) cancelOrder(writer http.ResponseWriter, request *http.Request) {
	var body struct{ Reason string }
	if !decodeJSON(writer, request, &body) {
		return
	}
	id, err := parseUUID(chi.URLParam(request, "orderID"))
	if err != nil {
		writeProblem(writer, request, 400, "invalid_order", "Invalid order ID")
		return
	}
	key := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(key) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	// The store's cancellation also releases a subscription row the unpaid
	// order opened, in the same transaction, so an operator cancelling a
	// never-paid order does not leave a ghost subscription on the dashboard.
	order, err := handlers.commerce.CancelOrder(request.Context(), uuidString(id), key, body.Reason)
	if err != nil {
		writeProblem(writer, request, 409, "order_cancellation_rejected", "Order cannot be cancelled")
		return
	}
	// Withdrawing the provider payment is best effort: a provider that cannot
	// be asked leaves the intent open, and a payment that lands anyway is still
	// settled and brought to an operator. The cancellation itself stands.
	if handlers.payments != nil {
		if cancelErr := handlers.payments.CancelIntents(request.Context(), uuidString(order.ID)); cancelErr != nil {
			slog.Default().Warn("provider payment withdrawal failed after cancellation",
				"order_id", uuidString(order.ID), "error", cancelErr)
		}
	}
	writeJSON(writer, 200, orderResponse(order))
}

func (handlers *CommerceHandlers) listFulfillmentDrifts(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := handlers.queries.ListOpenEntitlementDrifts(request.Context(), int32(limit))
	if err != nil {
		writeProblem(writer, request, 500, "drifts_unavailable", "Fulfillment drifts are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var expected, observed any
		_ = json.Unmarshal(row.Expected, &expected)
		_ = json.Unmarshal(row.Observed, &observed)
		items = append(items, map[string]any{"id": uuidString(row.ID), "entitlementId": uuidString(row.EntitlementID), "kind": row.Kind, "expected": expected, "observed": observed, "detectedAt": row.DetectedAt.Time})
	}
	writeJSON(writer, 200, map[string]any{"items": items})
}

func (handlers *CommerceHandlers) createPlan(writer http.ResponseWriter, request *http.Request) {
	var body catalogpg.PlanInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	plan, version, err := handlers.catalog.CreatePlan(request.Context(), body)
	if err != nil {
		writeProblem(writer, request, 422, "plan_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(plan.ID), "code": plan.Code, "kind": plan.Kind, "visible": plan.Visible, "versionId": uuidString(version.ID), "version": version.Version})
}

func (handlers *CommerceHandlers) createPlanVersion(writer http.ResponseWriter, request *http.Request) {
	var body catalogpg.VersionInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	version, err := handlers.catalog.CreateVersion(request.Context(), chi.URLParam(request, "planID"), body)
	if err != nil {
		writeProblem(writer, request, 422, "plan_version_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(version.ID), "planId": uuidString(version.PlanID), "version": version.Version})
}

func (handlers *CommerceHandlers) createPromotion(writer http.ResponseWriter, request *http.Request) {
	var body catalogpg.PromotionInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	promotion, err := handlers.catalog.CreatePromotion(request.Context(), body)
	if err != nil {
		writeProblem(writer, request, 422, "promotion_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(promotion.ID), "code": promotion.Code, "kind": promotion.Kind, "active": promotion.Active})
}

func (handlers *CommerceHandlers) operatorAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if handlers.operatorToken == "" || len(provided) != len(handlers.operatorToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(handlers.operatorToken)) != 1 {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthorized", "Operator authorization is required")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (handlers *CommerceHandlers) listPlans(writer http.ResponseWriter, request *http.Request) {
	locale := request.URL.Query().Get("locale")
	if locale != "en" {
		locale = "ru"
	}
	rows, err := handlers.queries.ListVisiblePlans(request.Context(), locale)
	if err != nil {
		writeProblem(writer, request, 500, "catalog_unavailable", "Catalog is unavailable")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{"id": uuidString(row.ID), "code": row.Code, "kind": row.Kind, "sortOrder": row.SortOrder, "name": row.Name, "description": row.Description, "planVersionId": uuidString(row.PlanVersionID), "version": row.Version, "billingPeriod": row.BillingPeriod, "durationSeconds": row.DurationSeconds, "trafficAllowanceBytes": nullableInt8(row.TrafficAllowanceBytes), "deviceLimit": nullableInt4(row.DeviceLimit), "squadIDs": databaseutil.UUIDStrings(row.RemnawaveSquadIds), "recurringCapable": row.RecurringCapable, "currency": row.Currency, "amountMinor": row.AmountMinor})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *CommerceHandlers) createOrder(writer http.ResponseWriter, request *http.Request) {
	var body struct{ CustomerID, PlanVersionID, Currency, Operation, PromoCode string }
	if !decodeJSON(writer, request, &body) {
		return
	}
	idempotency := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(idempotency) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	if body.PromoCode != "" {
		allowed, limitErr := handlers.promoLimiter.Allow(request.Context(), "promo", body.CustomerID, 10, time.Minute)
		if limitErr != nil {
			writeProblem(writer, request, 503, "rate_limit_unavailable", "Promo validation is temporarily unavailable")
			return
		}
		if !allowed {
			writeProblem(writer, request, 429, "promo_rate_limited", "Too many promo-code attempts")
			return
		}
	}
	order, err := handlers.commerce.CreateOrder(request.Context(), commercepg.CreateOrderInput{CustomerID: body.CustomerID, PlanVersionID: body.PlanVersionID, Currency: body.Currency, Operation: body.Operation, PromoCode: body.PromoCode, IdempotencyKey: idempotency})
	if err != nil {
		writeProblem(writer, request, 422, "order_rejected", err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, orderResponse(order))
}

func (handlers *CommerceHandlers) getOrder(writer http.ResponseWriter, request *http.Request) {
	id, err := parseUUID(chi.URLParam(request, "orderID"))
	if err != nil {
		writeProblem(writer, request, 400, "invalid_order", "Invalid order ID")
		return
	}
	order, err := handlers.queries.GetOrder(request.Context(), id)
	if err != nil {
		writeProblem(writer, request, 404, "order_not_found", "Order not found")
		return
	}
	writeJSON(writer, 200, orderResponse(order))
}

func (handlers *CommerceHandlers) createPayment(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Provider, Description, ReturnURL string
		ReceiptMetadata                  map[string]any
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	idempotency := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(idempotency) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	intent, err := handlers.payments.CreateIntent(request.Context(), paymentservice.CreateIntentInput{OrderID: chi.URLParam(request, "orderID"), Provider: body.Provider, IdempotencyKey: idempotency, Description: body.Description, ReturnURL: body.ReturnURL, ReceiptMetadata: body.ReceiptMetadata})
	if err != nil {
		writeProblem(writer, request, 422, "payment_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, paymentResponse(intent))
}

func (handlers *CommerceHandlers) paymentWebhook(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxJSONBody))
	if err != nil {
		writeProblem(writer, request, 413, "payload_too_large", "Webhook is too large")
		return
	}
	classification, err := handlers.payments.HandleWebhook(request.Context(), chi.URLParam(request, "provider"), request.Header, body)
	if err != nil {
		writeProblem(writer, request, 400, "webhook_rejected", "Webhook could not be verified or processed")
		return
	}
	writeJSON(writer, 200, map[string]string{"status": classification})
}

func (handlers *CommerceHandlers) reconcilePayment(writer http.ResponseWriter, request *http.Request) {
	intent, err := handlers.payments.Reconcile(request.Context(), chi.URLParam(request, "paymentID"))
	if err != nil {
		writeProblem(writer, request, 422, "reconciliation_failed", err.Error())
		return
	}
	writeJSON(writer, 200, paymentResponse(intent))
}

func (handlers *CommerceHandlers) refundPayment(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		AmountMinor      int64
		Currency, Reason string
		ReceiptMetadata  map[string]any
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	idempotency := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(idempotency) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	refund, err := handlers.payments.Refund(request.Context(), chi.URLParam(request, "paymentID"), idempotency, commerce.Money{Amount: body.AmountMinor, Currency: body.Currency}, body.Reason, body.ReceiptMetadata)
	if err != nil {
		writeProblem(writer, request, 422, "refund_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(refund.ID), "status": refund.Status, "amountMinor": refund.AmountMinor, "currency": refund.Currency, "providerReference": nullableText(refund.ProviderReference)})
}

func (handlers *CommerceHandlers) manualDecision(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Approved           bool
		OperatorID, Reason string
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	idempotency := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(idempotency) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	approval, err := handlers.payments.ApproveManual(request.Context(), chi.URLParam(request, "paymentID"), body.OperatorID, body.Reason, idempotency, middlewareRequestID(request), body.Approved)
	if err != nil {
		writeProblem(writer, request, 422, "manual_decision_rejected", err.Error())
		return
	}
	writeJSON(writer, 200, map[string]any{"id": uuidString(approval.ID), "decision": approval.Decision, "occurredAt": approval.OccurredAt.Time})
}

func (handlers *CommerceHandlers) adjustWallet(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		AmountMinor                                   int64
		Currency, Type, Reference, Reason, OperatorID string
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	idempotency := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(idempotency) {
		writeProblem(writer, request, 400, "missing_idempotency_key", "Idempotency-Key is required")
		return
	}
	err := handlers.commerce.AdjustWallet(request.Context(), chi.URLParam(request, "customerID"), body.Currency, body.AmountMinor, body.Type, body.Reference, idempotency, body.Reason, body.OperatorID)
	if err != nil {
		writeProblem(writer, request, 422, "adjustment_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]string{"status": "recorded"})
}

func (handlers *CommerceHandlers) updateCustomer(writer http.ResponseWriter, request *http.Request) {
	var body struct{ Locale, Timezone string }
	if !decodeJSON(writer, request, &body) {
		return
	}
	row, err := handlers.customers.UpdateProfile(request.Context(), chi.URLParam(request, "customerID"), body.Locale, body.Timezone)
	if err != nil {
		writeProblem(writer, request, 422, "customer_update_rejected", err.Error())
		return
	}
	writeJSON(writer, 200, customerResponse(row))
}

func (handlers *CommerceHandlers) linkIdentity(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Provider, Subject string
		VerifiedAt        time.Time
		Metadata          map[string]any
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	row, err := handlers.customers.LinkIdentity(request.Context(), chi.URLParam(request, "customerID"), body.Provider, body.Subject, body.VerifiedAt, body.Metadata)
	if err != nil {
		writeProblem(writer, request, 409, "identity_conflict", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(row.ID), "provider": row.Provider, "verifiedAt": row.VerifiedAt.Time})
}

func (handlers *CommerceHandlers) unlinkIdentity(writer http.ResponseWriter, request *http.Request) {
	_, err := handlers.customers.UnlinkIdentity(request.Context(), chi.URLParam(request, "customerID"), chi.URLParam(request, "identityID"))
	if err != nil {
		writeProblem(writer, request, 409, "identity_unlink_rejected", err.Error())
		return
	}
	writer.WriteHeader(204)
}

func (handlers *CommerceHandlers) createContact(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Kind, Value                            string
		VerifiedAt                             *time.Time
		TransactionalEnabled, MarketingEnabled bool
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	row, err := handlers.customers.SetContact(request.Context(), chi.URLParam(request, "customerID"), body.Kind, body.Value, body.VerifiedAt, body.TransactionalEnabled, body.MarketingEnabled)
	if err != nil {
		writeProblem(writer, request, 422, "contact_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(row.ID), "kind": row.Kind, "verified": row.VerifiedAt.Valid, "transactionalEnabled": row.TransactionalEnabled, "marketingEnabled": row.MarketingEnabled})
}

func (handlers *CommerceHandlers) recordConsent(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Purpose               string
		Granted               bool
		PolicyVersion, Source string
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	row, err := handlers.customers.RecordConsent(request.Context(), chi.URLParam(request, "customerID"), body.Purpose, body.Granted, body.PolicyVersion, body.Source, middlewareRequestID(request))
	if err != nil {
		writeProblem(writer, request, 422, "consent_rejected", err.Error())
		return
	}
	writeJSON(writer, 201, map[string]any{"id": uuidString(row.ID), "purpose": row.Purpose, "granted": row.Granted, "occurredAt": row.OccurredAt.Time})
}

func (handlers *CommerceHandlers) customerLifecycle(writer http.ResponseWriter, request *http.Request) {
	var body struct{ Action, Reason, ActorType, ActorID string }
	if !decodeJSON(writer, request, &body) {
		return
	}
	row, err := handlers.customers.Lifecycle(request.Context(), chi.URLParam(request, "customerID"), body.Action, body.Reason, body.ActorType, body.ActorID, middlewareRequestID(request))
	if err != nil {
		writeProblem(writer, request, 409, "lifecycle_rejected", err.Error())
		return
	}
	writeJSON(writer, 200, customerResponse(row))
}

func (handlers *CommerceHandlers) previewImport(writer http.ResponseWriter, request *http.Request) {
	handlers.runImportPreview(writer, request, "")
}
func (handlers *CommerceHandlers) resumeImport(writer http.ResponseWriter, request *http.Request) {
	handlers.runImportPreview(writer, request, chi.URLParam(request, "importID"))
}
func (handlers *CommerceHandlers) runImportPreview(writer http.ResponseWriter, request *http.Request, id string) {
	pageSize, _ := strconv.Atoi(request.URL.Query().Get("pageSize"))
	run, err := handlers.imports.Preview(request.Context(), id, pageSize)
	if err != nil {
		writeProblem(writer, request, 422, "import_failed", err.Error())
		return
	}
	writeJSON(writer, 200, importResponse(run))
}
func (handlers *CommerceHandlers) applyImport(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	run, err := handlers.imports.Apply(request.Context(), chi.URLParam(request, "importID"), limit)
	if err != nil {
		writeProblem(writer, request, 422, "import_failed", err.Error())
		return
	}
	writeJSON(writer, 200, importResponse(run))
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProblem(writer, request, 400, "invalid_request", "Invalid JSON request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(writer, request, 400, "invalid_request", "Request body must contain exactly one JSON value")
		return false
	}
	return true
}
func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, detail string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writeJSON(writer, status, map[string]any{"type": "https://omniflow.dev/problems/" + code, "title": http.StatusText(status), "status": status, "detail": detail, "request_id": middlewareRequestID(request)})
}
func middlewareRequestID(request *http.Request) string { return middleware.GetReqID(request.Context()) }
func validIdempotencyKey(value string) bool            { return len(value) >= 8 && len(value) <= 128 }
func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	err := id.Scan(value)
	if err != nil || !id.Valid {
		return pgtype.UUID{}, errors.New("invalid UUID")
	}
	return id, nil
}
func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	b := value.Bytes
	return strings.ToLower(strings.Join([]string{hexBytes(b[0:4]), hexBytes(b[4:6]), hexBytes(b[6:8]), hexBytes(b[8:10]), hexBytes(b[10:16])}, "-"))
}
func hexBytes(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, b := range value {
		result[index*2] = digits[b>>4]
		result[index*2+1] = digits[b&15]
	}
	return string(result)
}
func nullableInt8(value pgtype.Int8) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
func nullableInt4(value pgtype.Int4) any {
	if !value.Valid {
		return nil
	}
	return value.Int32
}
func nullableText(value pgtype.Text) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
func orderResponse(row dbgen.Order) map[string]any {
	return map[string]any{"id": uuidString(row.ID), "customerId": uuidString(row.UserID), "state": row.State, "operation": row.Operation, "currency": row.Currency, "subtotalMinor": row.SubtotalMinor, "discountMinor": row.DiscountMinor, "walletMinor": row.WalletMinor, "externalMinor": row.ExternalMinor, "paidMinor": row.PaidMinor, "refundedMinor": row.RefundedMinor, "expiresAt": row.ExpiresAt.Time, "createdAt": row.CreatedAt.Time}
}
func paymentResponse(row dbgen.PaymentIntent) map[string]any {
	return map[string]any{"id": uuidString(row.ID), "orderId": uuidString(row.OrderID), "provider": row.Provider, "status": row.Status, "amountMinor": row.AmountMinor, "currency": row.Currency, "providerReference": nullableText(row.ProviderReference), "checkoutUrl": nullableText(row.CheckoutUrl)}
}
func customerResponse(row dbgen.User) map[string]any {
	return map[string]any{"id": uuidString(row.ID), "status": row.Status, "locale": row.Locale, "timezone": row.Timezone, "suspendedAt": nullableTime(row.SuspendedAt), "deletedAt": nullableTime(row.DeletedAt), "anonymizedAt": nullableTime(row.AnonymizedAt), "retentionUntil": nullableTime(row.RetentionUntil)}
}
func nullableTime(value pgtype.Timestamptz) any {
	if !value.Valid {
		return nil
	}
	return value.Time
}
func importResponse(run dbgen.CustomerImport) map[string]any {
	return map[string]any{"id": uuidString(run.ID), "status": run.Status, "cursor": nullableText(run.Cursor), "totalCount": run.TotalCount, "validCount": run.ValidCount, "conflictCount": run.ConflictCount, "invalidCount": run.InvalidCount, "startedAt": run.StartedAt.Time, "updatedAt": run.UpdatedAt.Time, "completedAt": nullableTime(run.CompletedAt)}
}
