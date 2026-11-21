package paymentservice

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// A payment the previous bot opened that settles after the cutover.
//
// At the cutover the provider's webhook moves to Omniflow. A customer who
// started a top-up in Bedolaga a minute before and pays a minute after sends
// a notification for a payment Omniflow never opened; refusing it, as any
// unknown payment is refused, means the money was taken and nothing arrived.
// So a notification for an unknown payment whose metadata has Bedolaga's
// shape is looked up through the provider's authenticated API, and the
// looked-up payment — never the notification — decides what happens: the
// customer the import carried over is credited the amount to their wallet,
// which is what Bedolaga would have done with a top-up, and a payment that
// cannot be attributed is queued for an operator with a notice to the group.

// BedolagaRescueWindow bounds how long after the last import run such a
// payment is looked at. Bedolaga's own payments expire within hours; a month
// covers a slow bank and an operator's rehearsal-to-cutover gap, and keeps an
// unauthenticated notification from buying a provider request forever.
const BedolagaRescueWindow = 30 * 24 * time.Hour

// bedolagaRescueProviders are the providers whose Bedolaga metadata is read.
// YooKassa carries the previous bot's revenue; another provider's late
// payment is still refused, as before.
var bedolagaRescueProviders = map[string]bool{"yookassa": true}

// Rescue outcomes, which are also the webhook classification.
const (
	RescueCredited    = "bedolaga_credited"
	RescueNeedsReview = "bedolaga_needs_review"
	RescueNotPaid     = "bedolaga_not_paid"
	RescueDuplicate   = "bedolaga_duplicate"
)

// Reasons a payment is queued rather than credited.
const (
	ReviewGuestPurchase    = "guest_purchase"
	ReviewNoCustomer       = "customer_not_found"
	ReviewTelegramMismatch = "telegram_mismatch"
	ReviewCustomerInactive = "customer_inactive"
)

const bedolagaRescueActor = "bedolaga-payment-rescue"

// bedolagaShaped reports whether a notification's metadata looks like the
// previous bot's: a user it paid for, or a guest purchase token.
func bedolagaShaped(metadata map[string]string) bool {
	if metadata["order_id"] != "" {
		return false
	}
	_, user := metadata["user_id"]
	return user || metadata["purchase_token"] != ""
}

// rescueBedolagaPayment handles a notification for a payment this
// installation never opened. handled is false when it is not a previous bot's
// payment, and the caller refuses it as it always has.
func (service *Service) rescueBedolagaPayment(
	ctx context.Context, queries *dbgen.Queries, providerName string, provider payments.Provider,
	event payments.WebhookEvent,
) (classification string, handled bool, err error) {
	if !bedolagaRescueProviders[providerName] || !payments.NeedsWebhookPolling(provider) || !bedolagaShaped(event.Metadata) {
		return "", false, nil
	}
	latest, err := queries.LatestBedolagaImport(ctx)
	if err != nil {
		return "", false, err
	}
	if !latest.Valid || service.clock().Sub(latest.Time) > BedolagaRescueWindow {
		return "", false, nil
	}
	trusted, err := provider.Poll(ctx, event.ProviderReference)
	if err != nil {
		return "", false, err
	}
	if !bedolagaShaped(trusted.Metadata) {
		// The notification claimed a shape the payment itself does not have.
		return "", false, nil
	}
	if trusted.Status != "succeeded" {
		return RescueNotPaid, true, nil
	}
	amount := trusted.Amount.Amount - trusted.RefundedMinor
	if amount <= 0 {
		return RescueNotPaid, true, nil
	}
	claim := dbgen.InsertBedolagaPaymentClaimParams{
		Provider: providerName, ProviderReference: trusted.ProviderReference, AmountMinor: amount,
		Currency: trusted.Amount.Currency, PaymentType: truncate(firstNonEmpty(trusted.Metadata["type"], trusted.Metadata["payment_purpose"]), 64),
	}
	customer, reason, err := service.bedolagaCustomer(ctx, queries, trusted.Metadata, &claim)
	if err != nil {
		return "", false, err
	}
	if reason != "" {
		claim.Status, claim.Reason = "needs_review", reason
		return service.recordBedolagaClaim(ctx, queries, claim, RescueNeedsReview)
	}
	reference := "bedolaga-" + providerName + ":" + trusted.ProviderReference
	// The adjustment is idempotent on its key, so a notification delivered
	// twice, or a run that dies between the credit and the claim row,
	// credits once.
	if err = service.commerce.AdjustWallet(ctx, customer, trusted.Amount.Currency, amount, "credit",
		reference, "bedolaga:"+providerName+":"+trusted.ProviderReference,
		"Payment started in the previous bot and completed after the move", bedolagaRescueActor); err != nil {
		return "", false, err
	}
	customerID, err := parseUUID(customer)
	if err != nil {
		return "", false, err
	}
	claim.Status, claim.UserID = "credited", customerID
	return service.recordBedolagaClaim(ctx, queries, claim, RescueCredited)
}

// bedolagaCustomer finds the customer a Bedolaga payment was for, or the
// reason it cannot be credited automatically.
func (service *Service) bedolagaCustomer(
	ctx context.Context, queries *dbgen.Queries, metadata map[string]string, claim *dbgen.InsertBedolagaPaymentClaimParams,
) (string, string, error) {
	if metadata["purchase_token"] != "" {
		// A landing or gift purchase is a subscription for somebody, not a
		// top-up; Omniflow cannot deliver Bedolaga's tariff for it.
		return "", ReviewGuestPurchase, nil
	}
	raw := strings.TrimSpace(metadata["user_id"])
	sourceID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || sourceID <= 0 {
		return "", ReviewNoCustomer, nil
	}
	var customer pgtype.UUID
	if sourceID <= math.MaxInt32 {
		claim.BedolagaUserID = pgtype.Int8{Int64: sourceID, Valid: true}
		customer, err = queries.ImportedCustomerForBedolagaUser(ctx, sourceID)
	} else {
		// Bedolaga's older payments carry the Telegram ID in user_id.
		customer, err = queries.CustomerForTelegramSubject(ctx, raw)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ReviewNoCustomer, nil
	}
	if err != nil {
		return "", "", err
	}
	if expected := strings.TrimSpace(metadata["user_telegram_id"]); expected != "" {
		subjects, err := queries.CustomerTelegramSubjects(ctx, customer)
		if err != nil {
			return "", "", err
		}
		if len(subjects) > 0 && !contains(subjects, expected) {
			return "", ReviewTelegramMismatch, nil
		}
	}
	status, err := queries.CustomerStatus(ctx, customer)
	if err != nil {
		return "", "", err
	}
	if status != "active" && status != "suspended" {
		return "", ReviewCustomerInactive, nil
	}
	return uuidString(customer), "", nil
}

// recordBedolagaClaim writes the claim and tells the operator group. A claim
// that already exists means another delivery handled this payment.
func (service *Service) recordBedolagaClaim(
	ctx context.Context, queries *dbgen.Queries, claim dbgen.InsertBedolagaPaymentClaimParams, outcome string,
) (string, bool, error) {
	row, err := queries.InsertBedolagaPaymentClaim(ctx, claim)
	if errors.Is(err, pgx.ErrNoRows) {
		return RescueDuplicate, true, nil
	}
	if err != nil {
		return "", false, err
	}
	event := "bedolaga_payment_credited"
	if row.Status == "needs_review" {
		event = "bedolaga_payment_needs_review"
	}
	payload, _ := json.Marshal(map[string]any{
		"event": event, "provider": row.Provider, "providerReference": row.ProviderReference,
		"amountMinor": row.AmountMinor, "currency": row.Currency, "reason": row.Reason,
		"customerId": uuidString(row.UserID),
	})
	if _, err = service.pool.Exec(ctx, `INSERT INTO operator_notifications (kind, dedupe_key, payload)
		VALUES ('topup', $1, $2) ON CONFLICT DO NOTHING`, "bedolaga-payment:"+row.Provider+":"+row.ProviderReference, payload); err != nil {
		return "", false, err
	}
	if row.Status == "credited" {
		// The customer is told the money arrived, through the bot's ordinary
		// event notices: preferences, quiet hours, and delivery are theirs.
		params, _ := json.Marshal(map[string]any{"amountMinor": row.AmountMinor, "currency": row.Currency})
		if _, err = service.pool.Exec(ctx, `INSERT INTO customer_event_notices (user_id, kind, params, dedupe_key)
			VALUES ($1, 'wallet_credited', $2, $3) ON CONFLICT (dedupe_key) DO NOTHING`,
			row.UserID, params, "bedolaga-payment:"+row.Provider+":"+row.ProviderReference); err != nil {
			return "", false, err
		}
	}
	return outcome, true, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
