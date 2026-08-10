package paymentservice

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// providerDisplayOrder keeps customer-facing provider lists stable regardless of
// map iteration order.
var providerDisplayOrder = map[string]int{"telegram_stars": 1, "yookassa": 2, "cryptobot": 3, "manual": 9}

// Options lists the configured adapters for customer-facing provider selection.
func (service *Service) Options() []commerce.PaymentOption {
	options := payments.Options(service.providers, providerDisplayOrder)
	sort.SliceStable(options, func(left, right int) bool { return options[left].Provider < options[right].Provider })
	return options
}

// Enabled reports whether an adapter is configured for this installation.
func (service *Service) Enabled(provider string) bool {
	_, found := service.providers[provider]
	return found
}

// StarsSettlement is a successful Telegram Stars payment as reported by the
// authenticated Bot API update stream.
type StarsSettlement struct {
	// OrderID is the invoice payload the bot generated. It is the merchant
	// reference and is never taken from an untrusted source.
	OrderID string
	// ChargeID is telegram_payment_charge_id, the reference refunds need.
	ChargeID string
	// AmountMinor is the whole number of Stars charged.
	AmountMinor int64
	// UpdateID makes the stored raw event traceable to one Telegram update.
	UpdateID int64
}

// SettleTelegramStars applies a Stars payment received on the bot's own update
// stream. Telegram does not sign these updates for a public webhook route, so
// settlement never enters HandleWebhook; instead the charge identifier is the
// deduplication key, which makes a redelivered update a no-op.
func (service *Service) SettleTelegramStars(ctx context.Context, settlement StarsSettlement) (string, error) {
	provider, ok := service.providers["telegram_stars"]
	if !ok {
		return "", payments.ErrUnsupported
	}
	if settlement.ChargeID == "" || settlement.AmountMinor <= 0 {
		return "", errors.New("Telegram Stars settlement requires a charge identifier and a positive amount")
	}
	orderID, err := parseUUID(settlement.OrderID)
	if err != nil {
		return "", err
	}
	// The stored raw body is Omniflow's own minimal projection of the update:
	// enough to audit the settlement, with no message content or account data.
	raw, err := json.Marshal(map[string]any{"updateId": settlement.UpdateID, "orderId": settlement.OrderID, "chargeId": settlement.ChargeID, "amountMinor": settlement.AmountMinor, "currency": "XTR"})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	queries := dbgen.New(service.pool)
	stored, err := queries.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{Provider: provider.Name(), ProviderEventID: "charge:" + settlement.ChargeID, SignatureValid: true, BodySha256: digest[:], RawBody: raw, Headers: []byte(`{"source":"telegram_bot_api"}`)})
	if err != nil {
		return "", err
	}
	if stored.Status == "processed" || stored.Status == "ignored" {
		return "duplicate", nil
	}
	intent, err := queries.GetPaymentIntentByProviderReference(ctx, dbgen.GetPaymentIntentByProviderReferenceParams{Provider: provider.Name(), ProviderReference: optionalText(settlement.ChargeID)})
	if errors.Is(err, pgx.ErrNoRows) {
		intent, err = queries.GetPaymentIntentByOrderProvider(ctx, dbgen.GetPaymentIntentByOrderProviderParams{OrderID: orderID, Provider: provider.Name()})
	}
	if err != nil {
		_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("payment_not_found")})
		return "", err
	}
	if intent.OrderID != orderID {
		_, _ = queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: "failed", ErrorCode: optionalText("order_mismatch")})
		return "", errors.New("Telegram Stars charge does not belong to the referenced order")
	}
	// Record the charge reference before settling so a retry can find the intent
	// again and so refundStarPayment has the identifier it requires.
	if !intent.ProviderReference.Valid {
		if intent, err = queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: intent.Status, ProviderReference: optionalText(settlement.ChargeID)}); err != nil {
			return "", err
		}
	}
	order, err := queries.GetOrder(ctx, intent.OrderID)
	if err != nil {
		return "", err
	}
	late := order.ExpiresAt.Valid && service.clock().After(order.ExpiresAt.Time)
	_, classification, err := service.commerce.RecordProviderPayment(ctx, uuidString(intent.ID), "stars:"+settlement.ChargeID, commerce.Money{Amount: settlement.AmountMinor, Currency: intent.Currency}, late)
	status, errorCode := "processed", pgtype.Text{}
	if err != nil {
		status, errorCode = "failed", optionalText(classification)
	}
	if _, completeErr := queries.CompleteWebhookEvent(ctx, dbgen.CompleteWebhookEventParams{WebhookEventID: stored.ID, Status: status, ErrorCode: errorCode}); completeErr != nil && err == nil {
		return classification, completeErr
	}
	return classification, err
}

// NewStarsPayerResolver resolves the Telegram account that paid a Stars charge
// from the payment intent's non-secret receipt metadata. refundStarPayment
// needs it, and Omniflow never stores it anywhere else.
func NewStarsPayerResolver(pool *pgxpool.Pool) payments.StarsCustomerResolver {
	return func(ctx context.Context, chargeID string) (int64, error) {
		intent, err := dbgen.New(pool).GetPaymentIntentByProviderReference(ctx, dbgen.GetPaymentIntentByProviderReferenceParams{Provider: "telegram_stars", ProviderReference: optionalText(chargeID)})
		if err != nil {
			return 0, fmt.Errorf("resolve Telegram Stars payer: %w", err)
		}
		var metadata struct {
			TelegramUserID int64 `json:"telegramUserId"`
		}
		if err := json.Unmarshal(intent.ReceiptMetadata, &metadata); err != nil {
			return 0, fmt.Errorf("resolve Telegram Stars payer: %w", err)
		}
		return metadata.TelegramUserID, nil
	}
}
