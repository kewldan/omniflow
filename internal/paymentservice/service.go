package paymentservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/platform"
)

type Service struct {
	pool      *pgxpool.Pool
	commerce  *commercepg.Store
	providers map[string]payments.Provider
	metrics   *platform.Metrics
	clock     func() time.Time
}

func New(pool *pgxpool.Pool, commerceStore *commercepg.Store, providers ...payments.Provider) *Service {
	registry := make(map[string]payments.Provider, len(providers))
	for _, provider := range providers {
		registry[provider.Name()] = provider
	}
	return &Service{pool: pool, commerce: commerceStore, providers: registry, clock: time.Now}
}

// WithMetrics attaches the Prometheus surface. Provider and classification names
// are bounded values, so they are safe as labels; identifiers never are.
func (service *Service) WithMetrics(metrics *platform.Metrics) *Service {
	service.metrics = metrics
	return service
}

// observeWebhook records one webhook intake outcome.
func (service *Service) observeWebhook(provider, outcome string) {
	if service.metrics == nil {
		return
	}
	service.metrics.Webhooks.WithLabelValues(provider, outcome).Inc()
}

// observePayment records one settlement classification.
func (service *Service) observePayment(provider, operation, classification string) {
	if service.metrics == nil {
		return
	}
	service.metrics.Payments.WithLabelValues(provider, operation, classification).Inc()
}

func (service *Service) RunReconciler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		service.reconcileBatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (service *Service) reconcileBatch(ctx context.Context) {
	rows, err := dbgen.New(service.pool).ListPaymentIntentsForReconciliation(ctx, 100)
	if err != nil {
		return
	}
	for _, row := range rows {
		_, _ = service.Reconcile(ctx, uuidString(row.ID))
	}
}

type CreateIntentInput struct {
	OrderID         string
	Provider        string
	IdempotencyKey  string
	Description     string
	ReturnURL       string
	ReceiptMetadata map[string]any
	// SavedMethodToken charges a payment method the customer bound earlier
	// instead of opening a checkout. It is only ever set by the renewal worker,
	// which has already established that the customer consented, that the
	// operator enabled recurring for this merchant account, and that the
	// adapter can do it at all.
	SavedMethodToken string
}

func (service *Service) CreateIntent(ctx context.Context, input CreateIntentInput) (dbgen.PaymentIntent, error) {
	if !validIdempotencyKey(input.IdempotencyKey) {
		return dbgen.PaymentIntent{}, errors.New("invalid idempotency key")
	}
	provider, ok := service.providers[input.Provider]
	if !ok {
		return dbgen.PaymentIntent{}, errors.New("payment provider is not enabled")
	}
	orderID, err := parseUUID(input.OrderID)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	defer connection.Release()
	queries := dbgen.New(connection)
	lockKey := input.Provider + ":" + input.IdempotencyKey
	if err = queries.AcquirePaymentMutationLock(ctx, lockKey); err != nil {
		return dbgen.PaymentIntent{}, err
	}
	defer func() { _ = queries.ReleasePaymentMutationLock(context.WithoutCancel(ctx), lockKey) }()
	order, err := queries.GetOrder(ctx, orderID)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	if order.State != "pending" || order.ExternalMinor <= 0 {
		return dbgen.PaymentIntent{}, errors.New("order does not require an external payment")
	}
	// The provider is chosen here, after the order was opened with the default
	// window. A manual transfer is confirmed by a person in days, so the order
	// is given the window that provider warrants; the extension only ever
	// lengthens it, and only while the order is still pending.
	if window := commerce.PaymentWindow(provider.Name()); window > commerce.DefaultPaymentWindow {
		if err = service.commerce.ExtendOrderExpiry(ctx, input.OrderID, service.clock().Add(window)); err != nil {
			return dbgen.PaymentIntent{}, err
		}
	}
	amount := commerce.Money{Amount: order.ExternalMinor, Currency: order.Currency}
	capabilities, _ := json.Marshal(provider.Capabilities())
	receipt, _ := json.Marshal(input.ReceiptMetadata)
	intent, existingErr := queries.GetPaymentIntentByIdempotency(ctx, dbgen.GetPaymentIntentByIdempotencyParams{Provider: input.Provider, IdempotencyKey: input.IdempotencyKey})
	if existingErr == nil {
		if intent.OrderID != orderID || intent.AmountMinor != amount.Amount || intent.Currency != amount.Currency {
			return dbgen.PaymentIntent{}, errors.New("idempotency key was already used with different payment parameters")
		}
		if intent.ProviderReference.Valid || intent.Status != "processing" {
			return intent, nil
		}
	}
	if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.PaymentIntent{}, existingErr
	}
	if errors.Is(existingErr, pgx.ErrNoRows) {
		intent, err = queries.CreatePaymentIntent(ctx, dbgen.CreatePaymentIntentParams{OrderID: orderID, Provider: provider.Name(), Status: "processing", AmountMinor: amount.Amount, Currency: amount.Currency, IdempotencyKey: input.IdempotencyKey, Capabilities: capabilities, ReceiptMetadata: receipt})
		if err != nil {
			return dbgen.PaymentIntent{}, err
		}
	}
	if recoverer, ok := provider.(payments.Recoverer); ok {
		recovered, found, recoverErr := recoverer.Recover(ctx, input.OrderID)
		if recoverErr != nil {
			return dbgen.PaymentIntent{}, recoverErr
		}
		if found {
			return queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: recovered.Status, ProviderReference: optionalText(recovered.ProviderReference), CheckoutUrl: optionalText(recovered.CheckoutURL)})
		}
	}
	created, err := service.submit(ctx, provider, input, amount)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	if created.Status != "succeeded" {
		return queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: created.Status, ProviderReference: optionalText(created.ProviderReference), CheckoutUrl: optionalText(created.CheckoutURL)})
	}
	// The provider settled synchronously — a saved-method charge with capture
	// does. The money has moved, so it goes through the same classification
	// and settlement a webhook would produce; writing `succeeded` on the intent
	// alone would record a charge that bought nothing. The reference is stored
	// first, still as processing, so a webhook or the reconciler can find the
	// intent should the settlement below not commit.
	intent, err = queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: "processing", ProviderReference: optionalText(created.ProviderReference), CheckoutUrl: optionalText(created.CheckoutURL)})
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	late := order.ExpiresAt.Valid && service.clock().After(order.ExpiresAt.Time)
	settlementAmount := created.Amount
	if settlementAmount.Amount == 0 && settlementAmount.Currency == "" {
		settlementAmount = amount
	}
	_, classification, err := service.commerce.RecordProviderPayment(ctx, uuidString(intent.ID), SynchronousEventID(created.ProviderReference, uuidString(intent.ID)), settlementAmount, late)
	service.observePayment(provider.Name(), order.Operation, classification)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	return queries.GetPaymentIntent(ctx, intent.ID)
}

// SynchronousEventID names the payment event a synchronous settlement writes.
// It is derived from the provider's reference so the event reads as the charge
// it records, and falls back to the intent when the provider returned none. A
// webhook that later reports the same charge carries its own event ID and is
// classified as a duplicate against the order this already settled.
func SynchronousEventID(providerReference, intentID string) string {
	if providerReference != "" {
		return "charge:" + providerReference
	}
	return "charge:" + intentID
}

// submit asks the provider to move the money, by whichever of the two routes
// this intent is for.
//
// An adapter that cannot charge a stored method is refused here rather than
// silently falling back to opening a checkout: a renewal that quietly turns
// into a payment link the customer never sees is worse than a failed renewal,
// because nothing tells anyone it happened.
func (service *Service) submit(
	ctx context.Context, provider payments.Provider, input CreateIntentInput, amount commerce.Money,
) (payments.Intent, error) {
	if input.SavedMethodToken == "" {
		return provider.Create(ctx, payments.CreateRequest{
			OrderID: input.OrderID, IdempotencyKey: input.IdempotencyKey, Amount: amount,
			Description: input.Description, ReturnURL: input.ReturnURL,
			Metadata: map[string]string{"order_id": input.OrderID},
		})
	}
	charger, ok := provider.(payments.RecurringCharger)
	if !ok {
		return payments.Intent{}, payments.ErrUnsupported
	}
	return charger.ChargeSaved(ctx, payments.SavedChargeRequest{
		OrderID: input.OrderID, IdempotencyKey: input.IdempotencyKey,
		MethodToken: input.SavedMethodToken, Amount: amount, Description: input.Description,
	})
}

// CancelIntents withdraws the open payment intents of an order at their
// providers, as far as each provider allows. It belongs in the path that
// cancels an order: the customer has just been told nothing will be charged,
// and a checkout tab they left open should stop being able to contradict that.
//
// It is best effort. An adapter with no cancellation (Telegram Stars, YooKassa)
// leaves its intent open; a provider that refuses or cannot be reached leaves
// its intent open too, and the refusal is returned so the caller can log it.
// An intent the provider did withdraw is marked cancelled locally with a
// payment event. A payment that arrives regardless — through any of these
// paths — is still settled, as a payment after cancellation, and brought to an
// operator. The order's own cancellation must never be made to depend on this
// call succeeding.
func (service *Service) CancelIntents(ctx context.Context, orderID string) error {
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	queries := dbgen.New(service.pool)
	open, err := queries.ListOpenIntentsForOrder(ctx, id)
	if err != nil {
		return err
	}
	var refusals []error
	for _, intent := range open {
		provider, ok := service.providers[intent.Provider]
		if !ok {
			continue
		}
		canceller, ok := provider.(payments.Canceller)
		if !ok || !intent.ProviderReference.Valid {
			continue
		}
		if cancelErr := canceller.CancelIntent(ctx, intent.ProviderReference.String); cancelErr != nil {
			refusals = append(refusals, fmt.Errorf("%s intent %s: %w", intent.Provider, uuidString(intent.ID), cancelErr))
			continue
		}
		if _, err = queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: "cancelled"}); err != nil {
			return err
		}
		if _, err = queries.InsertPaymentEvent(ctx, dbgen.InsertPaymentEventParams{
			PaymentIntentID: intent.ID, Type: "status_changed",
			PreviousStatus: pgtype.Text{String: intent.Status, Valid: true}, Status: pgtype.Text{String: "cancelled", Valid: true},
			AmountMinor: pgtype.Int8{Int64: intent.AmountMinor, Valid: true}, Currency: pgtype.Text{String: intent.Currency, Valid: true},
			ProviderEventID: pgtype.Text{String: "cancel:" + orderID, Valid: true}, Details: []byte(`{"reason":"order_cancelled"}`),
		}); err != nil {
			return err
		}
	}
	return errors.Join(refusals...)
}

func (service *Service) Reconcile(ctx context.Context, paymentIntentID string) (dbgen.PaymentIntent, error) {
	id, err := parseUUID(paymentIntentID)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	queries := dbgen.New(service.pool)
	intent, err := queries.GetPaymentIntent(ctx, id)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	provider, ok := service.providers[intent.Provider]
	if !ok || !provider.Capabilities().Polling || !intent.ProviderReference.Valid {
		return dbgen.PaymentIntent{}, payments.ErrUnsupported
	}
	remote, err := provider.Poll(ctx, intent.ProviderReference.String)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	if remote.Status == "succeeded" {
		_, _, err = service.commerce.RecordProviderPayment(ctx, paymentIntentID, "reconcile:"+remote.ProviderReference, remote.Amount, false)
		if err != nil {
			return dbgen.PaymentIntent{}, err
		}
	}
	return queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: remote.Status, ProviderReference: optionalText(remote.ProviderReference), CheckoutUrl: optionalText(remote.CheckoutURL)})
}

func (service *Service) HandleWebhook(ctx context.Context, providerName string, headers http.Header, body []byte) (string, error) {
	provider, ok := service.providers[providerName]
	if !ok || !provider.Capabilities().Webhooks {
		return "", payments.ErrUnsupported
	}
	digest := sha256.Sum256(body)
	event, verifyErr := provider.VerifyWebhook(headers, body)
	queries := dbgen.New(service.pool)
	if verifyErr != nil {
		_, _ = queries.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{Provider: providerName, ProviderEventID: "invalid:" + hex.EncodeToString(digest[:]), SignatureValid: false, BodySha256: digest[:], RawBody: body, Headers: safeHeaders(headers)})
		service.observeWebhook(providerName, "signature_invalid")
		return "", verifyErr
	}
	stored, err := queries.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{Provider: providerName, ProviderEventID: event.ID, SignatureValid: true, BodySha256: digest[:], RawBody: body, Headers: safeHeaders(headers)})
	if err != nil {
		return "", err
	}
	if stored.Status == "processed" || stored.Status == "ignored" {
		return "duplicate", nil
	}
	// YooKassa notifications are untrusted hints. Fetch the payment through the
	// authenticated API and only use that response for financial state changes.
	if providerName == "yookassa" {
		trusted, pollErr := provider.Poll(ctx, event.ProviderReference)
		if pollErr != nil {
			_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("reconciliation_failed")})
			return "", pollErr
		}
		event.Status, event.Amount = trusted.Status, trusted.Amount
	}
	if event.Status != "succeeded" {
		_, err = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "ignored"})
		return "ignored", err
	}
	intent, err := queries.GetPaymentIntentByProviderReference(ctx, dbgen.GetPaymentIntentByProviderReferenceParams{Provider: providerName, ProviderReference: optionalText(event.ProviderReference)})
	if errors.Is(err, pgx.ErrNoRows) && event.MerchantReference != "" {
		orderID, parseErr := parseUUID(event.MerchantReference)
		if parseErr == nil {
			intent, err = queries.GetPaymentIntentByOrderProvider(ctx, dbgen.GetPaymentIntentByOrderProviderParams{OrderID: orderID, Provider: providerName})
		}
	}
	if err != nil {
		_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("payment_not_found")})
		return "", err
	}
	order, err := queries.GetOrder(ctx, intent.OrderID)
	if err != nil {
		return "", err
	}
	late := order.ExpiresAt.Valid && service.clock().After(order.ExpiresAt.Time)
	_, classification, err := service.commerce.RecordProviderPayment(ctx, uuidString(intent.ID), event.ID, event.Amount, late)
	service.observePayment(providerName, order.Operation, classification)
	service.observeWebhook(providerName, classification)
	status := "processed"
	errorCode := pgtype.Text{}
	if err != nil {
		status = "failed"
		errorCode = optionalText(classification)
	}
	_, completeErr := queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: status, ErrorCode: errorCode})
	if err != nil {
		return classification, err
	}
	return classification, completeErr
}

func (service *Service) Refund(ctx context.Context, paymentIntentID, idempotencyKey string, amount commerce.Money, reason string, receiptMetadata map[string]any) (dbgen.Refund, error) {
	if !validIdempotencyKey(idempotencyKey) || len(reason) == 0 {
		return dbgen.Refund{}, errors.New("refund requires a valid idempotency key and reason")
	}
	id, err := parseUUID(paymentIntentID)
	if err != nil {
		return dbgen.Refund{}, err
	}
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return dbgen.Refund{}, err
	}
	defer connection.Release()
	queries := dbgen.New(connection)
	lockKey := "refund:" + paymentIntentID
	if err = queries.AcquirePaymentMutationLock(ctx, lockKey); err != nil {
		return dbgen.Refund{}, err
	}
	defer func() { _ = queries.ReleasePaymentMutationLock(context.WithoutCancel(ctx), lockKey) }()
	intent, err := queries.GetPaymentIntent(ctx, id)
	if err != nil {
		return dbgen.Refund{}, err
	}
	if intent.Status != "succeeded" || amount.Currency != intent.Currency || amount.Amount <= 0 || amount.Amount > intent.AmountMinor {
		return dbgen.Refund{}, errors.New("invalid refund")
	}
	provider, ok := service.providers[intent.Provider]
	if !ok || !provider.Capabilities().Refunds || !intent.ProviderReference.Valid {
		return dbgen.Refund{}, payments.ErrUnsupported
	}
	if existing, existingErr := queries.GetRefundByIdempotency(ctx, dbgen.GetRefundByIdempotencyParams{PaymentIntentID: intent.ID, IdempotencyKey: idempotencyKey}); existingErr == nil {
		if existing.AmountMinor != amount.Amount || existing.Currency != amount.Currency {
			return dbgen.Refund{}, errors.New("idempotency key was already used with different refund parameters")
		}
		if existing.Status == "succeeded" {
			if _, err = service.commerce.RecordRefund(ctx, paymentIntentID, uuidString(existing.ID), "refund:"+idempotencyKey, commerce.Money{Amount: existing.AmountMinor, Currency: existing.Currency}); err != nil {
				return dbgen.Refund{}, err
			}
		}
		return existing, nil
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Refund{}, existingErr
	}
	reserved, err := queries.GetReservedRefundAmount(ctx, intent.ID)
	if err != nil {
		return dbgen.Refund{}, err
	}
	if reserved+amount.Amount > intent.AmountMinor {
		return dbgen.Refund{}, errors.New("refund exceeds the remaining provider-paid amount")
	}
	remote, err := provider.Refund(ctx, payments.RefundRequest{PaymentReference: intent.ProviderReference.String, IdempotencyKey: idempotencyKey, Amount: amount, Reason: reason})
	if err != nil {
		return dbgen.Refund{}, err
	}
	receipt, _ := json.Marshal(receiptMetadata)
	refund, err := queries.CreateRefund(ctx, dbgen.CreateRefundParams{PaymentIntentID: intent.ID, Status: remote.Status, AmountMinor: amount.Amount, Currency: amount.Currency, ProviderReference: optionalText(remote.ProviderReference), Reason: reason, IdempotencyKey: idempotencyKey, ReceiptMetadata: receipt})
	if err != nil {
		return dbgen.Refund{}, err
	}
	if refund.AmountMinor != amount.Amount || refund.Currency != amount.Currency {
		return dbgen.Refund{}, errors.New("idempotency key was already used with different refund parameters")
	}
	if refund.Status == "succeeded" {
		if _, err = service.commerce.RecordRefund(ctx, paymentIntentID, uuidString(refund.ID), "refund:"+idempotencyKey, commerce.Money{Amount: refund.AmountMinor, Currency: refund.Currency}); err != nil {
			return dbgen.Refund{}, err
		}
	}
	return refund, nil
}

func (service *Service) ApproveManual(ctx context.Context, paymentIntentID, operatorID, reason, idempotencyKey, requestID string, approved bool) (dbgen.ManualPaymentApproval, error) {
	if operatorID == "" || reason == "" || !validIdempotencyKey(idempotencyKey) {
		return dbgen.ManualPaymentApproval{}, errors.New("manual decision requires operator, reason, and idempotency key")
	}
	id, err := parseUUID(paymentIntentID)
	if err != nil {
		return dbgen.ManualPaymentApproval{}, err
	}
	decision := "rejected"
	if approved {
		decision = "approved"
	}
	queries := dbgen.New(service.pool)
	intent, err := queries.GetPaymentIntent(ctx, id)
	if err != nil {
		return dbgen.ManualPaymentApproval{}, err
	}
	if intent.Provider != "manual" {
		return dbgen.ManualPaymentApproval{}, errors.New("manual decision is only valid for a manual payment intent")
	}
	approval, err := queries.ApproveManualPayment(ctx, dbgen.ApproveManualPaymentParams{PaymentIntentID: id, Decision: decision, OperatorID: operatorID, Reason: reason, IdempotencyKey: idempotencyKey, RequestID: optionalText(requestID)})
	if err != nil {
		return approval, err
	}
	if approval.Decision != decision {
		return approval, errors.New("manual payment already has a different final decision")
	}
	if !approved {
		return approval, nil
	}
	_, _, err = service.commerce.RecordProviderPayment(ctx, paymentIntentID, "manual:"+uuidString(approval.ID), commerce.Money{Amount: intent.AmountMinor, Currency: intent.Currency}, false)
	return approval, err
}

func safeHeaders(headers http.Header) []byte {
	value, _ := json.Marshal(map[string]string{"content-type": headers.Get("Content-Type"), "user-agent": headers.Get("User-Agent")})
	return value
}

func optionalText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }

func validIdempotencyKey(value string) bool { return len(value) >= 8 && len(value) <= 128 }

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID")
	}
	return id, nil
}

func uuidString(value pgtype.UUID) string {
	b := value.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
