package payments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

type TelegramStars struct{}

func (TelegramStars) Name() string { return "telegram_stars" }
func (TelegramStars) Capabilities() Capabilities {
	// Stars updates are accepted only from the authenticated Telegram update
	// stream in the bot process. The public provider-webhook route must not treat
	// an unsigned Bot API update as verified. Refunds are enabled in v0.4 when
	// the bot can supply the customer and Telegram charge identifiers required
	// by refundStarPayment.
	return Capabilities{}
}
func (TelegramStars) Create(_ context.Context, request CreateRequest) (Intent, error) {
	if request.Amount.Currency != "XTR" || request.Amount.Amount <= 0 {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	return Intent{
		ProviderReference: request.OrderID,
		Status:            "requires_action",
		Amount:            request.Amount,
		Metadata: map[string]string{
			"currency": "XTR", "payload": request.OrderID,
			"stars": strconv.FormatInt(request.Amount.Amount, 10),
		},
	}, nil
}
func (TelegramStars) Poll(context.Context, string) (Intent, error) { return Intent{}, ErrUnsupported }
func (TelegramStars) Refund(_ context.Context, request RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}
func (TelegramStars) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	// Telegram payment updates arrive through the authenticated Bot API update
	// stream. Only the minimal successful_payment projection is accepted here.
	var update struct {
		UpdateID int64 `json:"update_id"`
		Message  struct {
			Date              int64 `json:"date"`
			SuccessfulPayment *struct {
				Currency         string `json:"currency"`
				TotalAmount      int64  `json:"total_amount"`
				InvoicePayload   string `json:"invoice_payload"`
				TelegramChargeID string `json:"telegram_payment_charge_id"`
			} `json:"successful_payment"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &update); err != nil || update.Message.SuccessfulPayment == nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	payment := update.Message.SuccessfulPayment
	if payment.Currency != "XTR" || payment.TelegramChargeID == "" || payment.InvoicePayload == "" {
		return WebhookEvent{}, errors.New("invalid Telegram Stars payment")
	}
	return WebhookEvent{ID: strconv.FormatInt(update.UpdateID, 10), Type: "payment.succeeded", ProviderReference: payment.TelegramChargeID, MerchantReference: payment.InvoicePayload, Status: "succeeded", Amount: commerce.Money{Amount: payment.TotalAmount, Currency: "XTR"}, OccurredAt: time.Unix(update.Message.Date, 0).UTC()}, nil
}

var _ Provider = TelegramStars{}
