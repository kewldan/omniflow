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
	contacts  ContactResolver
	options   optionCache
	purposes  payments.PurposeTemplates
}

// WithPurposeTemplates sets the operator's wording of payment purposes.
func (service *Service) WithPurposeTemplates(templates payments.PurposeTemplates) *Service {
	service.purposes = templates
	return service
}

// ContactResolver opens a customer's verified contact. It is customerpg's
// contact sealing, narrowed to the one read the payment service needs.
type ContactResolver interface {
	VerifiedContact(ctx context.Context, customerID, kind string) (string, error)
}

// WithContacts lets the service hand the payer's verified e-mail to the
// adapters that require one. Without it those adapters receive none: an
// Antilopay checkout is then refused, and a YooKassa receipt goes to the
// installation-wide fallback address.
func (service *Service) WithContacts(contacts ContactResolver) *Service {
	service.contacts = contacts
	return service
}

// payerContact adds the customer's verified e-mail to the payer fields, only
// for an adapter that declares it needs one. A lookup failure leaves it out:
// the adapter then refuses or falls back exactly as with no address at all.
func (service *Service) payerContact(ctx context.Context, provider payments.Provider, payer map[string]string) {
	if service.contacts == nil || !payments.UsesCustomerContact(provider) {
		return
	}
	email, err := service.contacts.VerifiedContact(ctx, payer["customer_id"], "email")
	if err == nil && email != "" {
		payer["customer_email"] = email
	}
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

// ReconcilePass re-polls one batch of in-flight payment intents, then revives
// refunds whose provider outcome was never written down.
//
// The worker runs it as a cluster-wide periodic job (see internal/jobs), so the
// providers are asked once per interval rather than once per api replica. One
// intent that fails to reconcile does not stop the rest; only a failure to
// list the batch is reported.
func (service *Service) ReconcilePass(ctx context.Context) error {
	defer service.reviveRefunds(ctx)
	rows, err := dbgen.New(service.pool).ListPaymentIntentsForReconciliation(ctx, 100)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, _ = service.Reconcile(ctx, uuidString(row.ID))
	}
	return nil
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
	// SaveMethod is the customer's explicit consent, given on this checkout,
	// to keep the instrument for automatic renewal. It is honoured only when
	// the adapter can bind a method and the operator enabled recurring after a
	// passing capability test; otherwise the payment proceeds unchanged and no
	// method is bound. Callers include it in the idempotency key, because a
	// payment opened with consent is a different request from one without.
	SaveMethod bool
	// Method is the provider method the customer chose; empty is the
	// adapter's default. It is stored on the intent and sent to the adapter.
	Method string
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
	// A loyalty group may keep its members to some payment methods; the
	// surfaces hide the others, and this is what makes that binding.
	if allowed, allowErr := service.commerce.ProviderAllowed(ctx, uuidString(order.UserID), provider.Name()); allowErr != nil {
		return dbgen.PaymentIntent{}, allowErr
	} else if !allowed {
		return dbgen.PaymentIntent{}, commercepg.ErrProviderRestricted
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
	saveMethod := input.SaveMethod && input.SavedMethodToken == "" && service.RecurringAllowed(ctx, provider.Name())
	consentAt := pgtype.Timestamptz{}
	if saveMethod {
		consentAt = pgtype.Timestamptz{Time: service.clock().UTC(), Valid: true}
	}
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
		intent, err = queries.CreatePaymentIntent(ctx, dbgen.CreatePaymentIntentParams{OrderID: orderID, Provider: provider.Name(), Status: "processing", AmountMinor: amount.Amount, Currency: amount.Currency, IdempotencyKey: input.IdempotencyKey, Capabilities: capabilities, ReceiptMetadata: receipt, SaveMethodConsentAt: consentAt, Method: input.Method})
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
			return queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: payments.IntentStatus(recovered.Status), ProviderReference: optionalText(recovered.ProviderReference), CheckoutUrl: optionalText(recovered.CheckoutURL)})
		}
	}
	input.SaveMethod = intent.SaveMethodConsentAt.Valid
	input.Method = intent.Method
	input.Description = service.purposes.Render(order.Operation, input.Description, input.OrderID)
	payer := payerMetadata(ctx, queries, order.UserID)
	service.payerContact(ctx, provider, payer)
	created, err := service.submit(ctx, provider, input, amount, payer)
	if err != nil {
		return dbgen.PaymentIntent{}, err
	}
	if created.Status != "succeeded" {
		return queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: payments.IntentStatus(created.Status), ProviderReference: optionalText(created.ProviderReference), CheckoutUrl: optionalText(created.CheckoutURL)})
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

// RecurringAllowed reports whether a checkout through this adapter may offer
// to keep the customer's instrument: the adapter must declare the capability,
// and the operator must have enabled recurring for the default merchant
// account after a passing capability test. It is the same rule the renewal
// worker applies before charging, so a card is never saved that could not
// later be charged.
func (service *Service) RecurringAllowed(ctx context.Context, providerName string) bool {
	provider, ok := service.providers[providerName]
	if !ok || !provider.Capabilities().Recurring {
		return false
	}
	if _, charges := provider.(payments.RecurringCharger); !charges {
		return false
	}
	stored, err := dbgen.New(service.pool).GetPaymentProviderSettings(ctx, dbgen.GetPaymentProviderSettingsParams{Provider: providerName, MerchantID: ""})
	if err != nil {
		return false
	}
	return stored.RecurringEnabled && stored.RecurringTestStatus == "passed"
}

// bindMethod stores the token a provider issued for an instrument the customer
// asked to keep. It runs only after the payment settled, only for an intent
// stamped with the customer's consent, and only from an authenticated provider
// read. It is best effort: the payment has already been applied, and a binding
// that fails leaves the customer able to renew by hand, which is the state
// they were in before.
func (service *Service) bindMethod(ctx context.Context, queries *dbgen.Queries, intent dbgen.PaymentIntent, remote payments.Intent) {
	if !intent.SaveMethodConsentAt.Valid || remote.BoundMethod == nil || remote.Status != "succeeded" || remote.BoundMethod.Token == "" {
		return
	}
	order, err := queries.GetOrder(ctx, intent.OrderID)
	if err != nil {
		return
	}
	_, _ = queries.BindPaymentMethodFromIntent(ctx, dbgen.BindPaymentMethodFromIntentParams{
		UserID: order.UserID, Provider: intent.Provider, MerchantID: "",
		ProviderToken: remote.BoundMethod.Token, DisplayLabel: remote.BoundMethod.Label,
		ConsentAt: intent.SaveMethodConsentAt, SourcePaymentIntentID: intent.ID,
	})
}

// payerMetadata is what a provider may need to identify the payer: Omniflow's
// own customer ID and, when the customer signed in with Telegram, their
// Telegram ID. Several acquirers (Platega, MulenPay) refuse a checkout that
// names no payer. The verified e-mail is added separately, by payerContact,
// and only for an adapter that needs it; a lookup failure only leaves the
// fields out, because the order itself is still payable.
func payerMetadata(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID) map[string]string {
	metadata := map[string]string{"customer_id": uuidString(userID)}
	identities, err := queries.ListCustomerIdentities(ctx, userID)
	if err != nil {
		return metadata
	}
	for _, identity := range identities {
		if identity.Provider == "telegram" && identity.Status == "active" && identity.ProviderSubject != "" {
			metadata["customer_telegram_id"] = identity.ProviderSubject
			break
		}
	}
	return metadata
}

// SynchronousEventID names the payment event a synchronous settlement writes.
// It is derived from the provider's reference so the event reads as the charge
// it records, and falls back to the intent when the provider returned none. A
// webhook that later reports the same charge carries its own event ID, finds
// the same intent already succeeded, and is recorded as a replay: it changes
// nothing and raises no operator incident.
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
	payer map[string]string,
) (payments.Intent, error) {
	if input.SavedMethodToken == "" {
		metadata := map[string]string{"order_id": input.OrderID}
		for key, value := range payer {
			metadata[key] = value
		}
		return provider.Create(ctx, payments.CreateRequest{
			OrderID: input.OrderID, IdempotencyKey: input.IdempotencyKey, Amount: amount,
			Description: input.Description, ReturnURL: input.ReturnURL,
			Metadata: metadata, SavePaymentMethod: input.SaveMethod, Method: input.Method,
		})
	}
	charger, ok := provider.(payments.RecurringCharger)
	if !ok {
		return payments.Intent{}, payments.ErrUnsupported
	}
	return charger.ChargeSaved(ctx, payments.SavedChargeRequest{
		OrderID: input.OrderID, IdempotencyKey: input.IdempotencyKey,
		MethodToken: input.SavedMethodToken, Amount: amount, Description: input.Description,
		Metadata: payer,
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
		service.bindMethod(ctx, queries, intent, remote)
	}
	return queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: payments.IntentStatus(remote.Status), ProviderReference: optionalText(remote.ProviderReference), CheckoutUrl: optionalText(remote.CheckoutURL)})
}

// ErrWebhookRejected marks a notification that will never be applied however
// often it is delivered: a bad signature, an unknown provider, or a payment
// this installation never opened. Any other HandleWebhook error is transient
// (the database, or the provider's API during re-verification), and the HTTP
// layer answers it with a 5xx so providers that retry only on server errors —
// Cashera among them — deliver it again.
var ErrWebhookRejected = errors.New("webhook rejected")

func (service *Service) HandleWebhook(ctx context.Context, providerName string, headers http.Header, body []byte) (string, error) {
	provider, ok := service.providers[providerName]
	if !ok || !provider.Capabilities().Webhooks {
		return "", fmt.Errorf("%w: %w", ErrWebhookRejected, payments.ErrUnsupported)
	}
	digest := sha256.Sum256(body)
	var bound *payments.BoundMethod
	event, verifyErr := provider.VerifyWebhook(headers, body)
	queries := dbgen.New(service.pool)
	if verifyErr != nil {
		// The digest is kept so repeated garbage is recognisable; the body is
		// not. Anyone can post here, and storing whatever they send — up to the
		// request cap, for thirty days — would hand them the database's disk.
		_, _ = queries.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{Provider: providerName, ProviderEventID: "invalid:" + hex.EncodeToString(digest[:]), SignatureValid: false, BodySha256: digest[:], RawBody: []byte{}, Headers: safeHeaders(headers)})
		service.observeWebhook(providerName, "signature_invalid")
		return "", fmt.Errorf("%w: %w", ErrWebhookRejected, verifyErr)
	}
	stored, err := queries.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{Provider: providerName, ProviderEventID: event.ID, SignatureValid: true, BodySha256: digest[:], RawBody: body, Headers: safeHeaders(headers)})
	if err != nil {
		return "", err
	}
	if stored.Status == "processed" || stored.Status == "ignored" {
		return "duplicate", nil
	}
	// Unsigned notifications (YooKassa and every adapter that declares
	// PollVerifiedWebhooks) are untrusted hints. Fetch the payment through the
	// authenticated API and only use that response for financial state changes.
	//
	// The poll happens only for a payment this installation actually opened.
	// The notification is unauthenticated, so without that check anyone could
	// make the API spend a provider request — and a slot in the provider's
	// rate limit — for every well-formed body they post.
	if payments.NeedsWebhookPolling(provider) {
		if _, lookupErr := service.webhookIntent(ctx, queries, providerName, event); lookupErr != nil {
			_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("payment_not_found")})
			service.observeWebhook(providerName, "payment_not_found")
			return "", rejectNotFound(lookupErr)
		}
		trusted, pollErr := provider.Poll(ctx, event.ProviderReference)
		if pollErr != nil {
			_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("reconciliation_failed")})
			return "", pollErr
		}
		event.Status, event.Amount = trusted.Status, trusted.Amount
		event.RefundedMinor = trusted.RefundedMinor
		bound = trusted.BoundMethod
	}
	if payments.IsReversal(event.Status) {
		return service.handleReversal(ctx, queries, providerName, stored.ID, event)
	}
	if event.Status != "succeeded" {
		_, err = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "ignored"})
		return "ignored", err
	}
	intent, err := service.webhookIntent(ctx, queries, providerName, event)
	if err != nil {
		_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("payment_not_found")})
		return "", rejectNotFound(err)
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
	if bound != nil {
		service.bindMethod(ctx, queries, intent, payments.Intent{Status: event.Status, BoundMethod: bound})
	}
	if event.RefundedMinor > 0 {
		// A settled payment the provider has since partly refunded: the
		// authenticated read names the refunded total.
		if _, reverseErr := service.applyReversal(ctx, intent, payments.StatusRefunded, event.RefundedMinor, event.ID); reverseErr != nil {
			return classification, reverseErr
		}
	}
	return classification, completeErr
}

// handleReversal applies a refund or chargeback the provider reported for a
// payment this installation opened.
func (service *Service) handleReversal(
	ctx context.Context, queries *dbgen.Queries, providerName string, storedID pgtype.UUID, event payments.WebhookEvent,
) (string, error) {
	intent, err := service.webhookIntent(ctx, queries, providerName, event)
	if err != nil {
		_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: storedID, Status: "failed", ErrorCode: optionalText("payment_not_found")})
		return "", rejectNotFound(err)
	}
	classification, err := service.applyReversal(ctx, intent, event.Status, event.RefundedMinor, event.ID)
	service.observeWebhook(providerName, event.Status)
	status, code := "processed", pgtype.Text{}
	if err != nil {
		status, code = "failed", optionalText("reversal_failed")
	}
	_, completeErr := queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: storedID, Status: status, ErrorCode: code})
	if err != nil {
		return "", err
	}
	return classification, completeErr
}

// rejectNotFound turns "no such payment" into a permanent rejection and leaves
// any other lookup failure transient.
func rejectNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrWebhookRejected, err)
	}
	return err
}

// WebhookAck returns the provider-specific success body for a verified
// notification, when the adapter's provider requires one.
func (service *Service) WebhookAck(providerName, classification string) (int, string, []byte, bool) {
	provider, ok := service.providers[providerName]
	if !ok {
		return 0, "", nil, false
	}
	acknowledger, ok := provider.(payments.WebhookAcknowledger)
	if !ok {
		return 0, "", nil, false
	}
	status, contentType, body := acknowledger.WebhookAck(classification)
	return status, contentType, body, true
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

// webhookIntent finds the payment intent a webhook refers to: by the provider's
// own reference, or, failing that, by the order the merchant reference names.
func (service *Service) webhookIntent(
	ctx context.Context, queries *dbgen.Queries, providerName string, event payments.WebhookEvent,
) (dbgen.PaymentIntent, error) {
	intent, err := queries.GetPaymentIntentByProviderReference(ctx, dbgen.GetPaymentIntentByProviderReferenceParams{Provider: providerName, ProviderReference: optionalText(event.ProviderReference)})
	if errors.Is(err, pgx.ErrNoRows) && event.MerchantReference != "" {
		orderID, parseErr := parseUUID(event.MerchantReference)
		if parseErr == nil {
			intent, err = queries.GetPaymentIntentByOrderProvider(ctx, dbgen.GetPaymentIntentByOrderProviderParams{OrderID: orderID, Provider: providerName})
		}
	}
	if errors.Is(err, pgx.ErrNoRows) && event.PayerTelegramID != "" {
		intent, err = queries.GetOpenPaymentIntentByPayerTelegram(ctx, dbgen.GetOpenPaymentIntentByPayerTelegramParams{
			Provider: providerName, TelegramSubject: event.PayerTelegramID,
			AmountMinor: event.Amount.Amount, Currency: event.Amount.Currency,
		})
	}
	return intent, err
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
