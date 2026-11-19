package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Tribute is the Tribute (tribute.tg) donation-link adapter.
//
// Tribute has no API to open a checkout: the customer pays through the
// merchant's fixed donation link, and Tribute reports each donation to the
// webhook signed as hex(HMAC-SHA256(API key, raw body)) in `trbt-signature`.
//
// A donation names the payer's Telegram user and the donation link, never an
// Omniflow order. The event therefore carries PayerTelegramID, and the payment
// service settles the payer's newest open Tribute intent for exactly the
// donated amount. A donation that matches none is recorded and not applied.
type Tribute struct {
	apiKey     string
	donateLink string
	clock      func() time.Time
}

// buildTribute reads APP_TRIBUTE_* and constructs the adapter.
func buildTribute(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_TRIBUTE_API_KEY")
	donateLink := settings.String("APP_TRIBUTE_DONATE_LINK")
	if apiKey == "" && donateLink == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || donateLink == "" {
		return nil, errors.New("tribute: APP_TRIBUTE_API_KEY and APP_TRIBUTE_DONATE_LINK must both be set")
	}
	return &Tribute{apiKey: apiKey, donateLink: donateLink, clock: time.Now}, nil
}

func (provider *Tribute) Name() string { return "tribute" }

func (provider *Tribute) Capabilities() Capabilities {
	return Capabilities{Webhooks: true}
}

// SupportsCurrency reports RUB only, the currency the reference integration
// credits donations in.
func (provider *Tribute) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

// Create hands the customer the merchant's donation link. Nothing is created
// at Tribute, so there is no provider reference and the amount is chosen by
// the payer on Tribute's page.
func (provider *Tribute) Create(_ context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, fmt.Errorf("tribute: currency %s is not supported", request.Amount.Currency)
	}
	return Intent{Status: "requires_action", CheckoutURL: provider.donateLink, Amount: request.Amount}, nil
}

// Poll is unsupported: Tribute offers no status API.
func (provider *Tribute) Poll(context.Context, string) (Intent, error) {
	return Intent{}, ErrUnsupported
}

func (provider *Tribute) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// tributeNumber keeps a JSON number or string as its literal text.
type tributeNumber string

func (number *tributeNumber) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*number = tributeNumber(text)
		return nil
	}
	var literal json.Number
	if err := json.Unmarshal(data, &literal); err != nil {
		if string(data) == "null" {
			*number = ""
			return nil
		}
		return err
	}
	*number = tributeNumber(literal.String())
	return nil
}

type tributeDonation struct {
	DonationRequestID tributeNumber `json:"donation_request_id"`
	Amount            tributeNumber `json:"amount"`
	Currency          string        `json:"currency"`
	TelegramUserID    tributeNumber `json:"telegram_user_id"`
}

func (provider *Tribute) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get("trbt-signature")))
	if err != nil || len(provided) == 0 {
		return WebhookEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(provider.apiKey))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var notification struct {
		Name      string          `json:"name"`
		CreatedAt string          `json:"created_at"`
		Payload   tributeDonation `json:"payload"`
	}
	if err := json.Unmarshal(body, &notification); err != nil || notification.Name == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	donation := notification.Payload
	status := "pending"
	switch notification.Name {
	case "new_donation", "recurrent_donation":
		status = "succeeded"
	case "cancelled_subscription":
		status = "cancelled"
	}
	var money commerce.Money
	if donation.Amount != "" {
		currency := strings.ToUpper(strings.TrimSpace(donation.Currency))
		if currency == "" {
			currency = "RUB"
		}
		if _, err := currencyExponent(currency); err != nil {
			return WebhookEvent{}, err
		}
		// Tribute reports donation amounts already in minor units.
		minor, err := parseMinor(string(donation.Amount), 0)
		if err != nil {
			return WebhookEvent{}, err
		}
		money = commerce.Money{Amount: minor, Currency: currency}
	} else if status == "succeeded" {
		return WebhookEvent{}, fmt.Errorf("%w: tribute donation without amount", ErrProviderResponse)
	}
	// A donation has no payment ID: donation_request_id names the reusable
	// link. The key is per event — created_at is stable across redeliveries
	// and distinct between donations — as in the reference integration.
	reference := ""
	if donation.DonationRequestID != "" && notification.CreatedAt != "" {
		reference = string(donation.DonationRequestID) + "_" + string(donation.TelegramUserID) + "_" + string(donation.Amount) + "_" + notification.CreatedAt
	}
	id := notification.Name + ":" + reference
	if reference == "" {
		digest := sha256.Sum256(body)
		id = notification.Name + ":" + hex.EncodeToString(digest[:16])
	}
	occurred := provider.clock().UTC()
	if parsed, err := time.Parse(time.RFC3339, notification.CreatedAt); err == nil {
		occurred = parsed.UTC()
	}
	return WebhookEvent{
		ID:                id,
		Type:              notification.Name,
		ProviderReference: reference,
		Status:            status,
		Amount:            money,
		OccurredAt:        occurred,
		PayerTelegramID:   strings.TrimSpace(string(donation.TelegramUserID)),
	}, nil
}

var (
	_ Provider        = (*Tribute)(nil)
	_ CurrencySupport = (*Tribute)(nil)
)
