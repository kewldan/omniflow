package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

type CryptoBot struct {
	token   string
	baseURL string
	http    *http.Client
}

func NewCryptoBot(token string, testnet bool) (*CryptoBot, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("CryptoBot token is required")
	}
	host := "https://pay.crypt.bot/api"
	if testnet {
		host = "https://testnet-pay.crypt.bot/api"
	}
	return &CryptoBot{token: token, baseURL: host, http: tracedClient(15 * time.Second)}, nil
}

func (provider *CryptoBot) Name() string { return "cryptobot" }
func (provider *CryptoBot) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// cryptoBotFiat is the fiat set CryptoBot prices invoices in. Telegram Stars are
// deliberately absent: they never settle through CryptoBot.
var cryptoBotFiat = map[string]bool{
	"USD": true, "EUR": true, "RUB": true, "BYN": true, "UAH": true, "GBP": true,
	"CNY": true, "KZT": true, "UZS": true, "GEL": true, "TRY": true, "AMD": true,
	"INR": true, "AZN": true, "AED": true, "PLN": true,
}

// SupportsCurrency reports whether CryptoBot can invoice the given fiat currency.
func (provider *CryptoBot) SupportsCurrency(currency string) bool {
	return cryptoBotFiat[strings.ToUpper(strings.TrimSpace(currency))]
}

func (provider *CryptoBot) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	exponent, err := currencyExponent(request.Amount.Currency)
	if err != nil {
		return Intent{}, err
	}
	form := url.Values{
		"currency_type": {"fiat"}, "fiat": {request.Amount.Currency},
		"amount": {formatMinor(request.Amount.Amount, exponent)}, "description": {request.Description},
		"payload": {request.OrderID}, "expires_in": {"3600"},
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			InvoiceID int64  `json:"invoice_id"`
			Status    string `json:"status"`
			BotURL    string `json:"bot_invoice_url"`
			WebURL    string `json:"web_app_invoice_url"`
		} `json:"result"`
	}
	if err := provider.call(ctx, "createInvoice", form, &envelope); err != nil {
		return Intent{}, err
	}
	if !envelope.OK || envelope.Result.InvoiceID == 0 {
		return Intent{}, ErrProviderResponse
	}
	checkout := envelope.Result.WebURL
	if checkout == "" {
		checkout = envelope.Result.BotURL
	}
	return Intent{ProviderReference: strconv.FormatInt(envelope.Result.InvoiceID, 10), Status: normalizeCryptoBotStatus(envelope.Result.Status), CheckoutURL: checkout, Amount: request.Amount}, nil
}

func (provider *CryptoBot) Poll(ctx context.Context, reference string) (Intent, error) {
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Items []struct {
				InvoiceID int64  `json:"invoice_id"`
				Status    string `json:"status"`
				Amount    string `json:"amount"`
				Fiat      string `json:"fiat"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := provider.call(ctx, "getInvoices", url.Values{"invoice_ids": {reference}}, &envelope); err != nil {
		return Intent{}, err
	}
	if !envelope.OK || len(envelope.Result.Items) != 1 {
		return Intent{}, ErrProviderResponse
	}
	item := envelope.Result.Items[0]
	exponent, err := currencyExponent(item.Fiat)
	if err != nil {
		return Intent{}, err
	}
	amount, err := parseMinor(item.Amount, exponent)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: strconv.FormatInt(item.InvoiceID, 10), Status: normalizeCryptoBotStatus(item.Status), Amount: commerce.Money{Amount: amount, Currency: item.Fiat}}, nil
}

func (provider *CryptoBot) Recover(ctx context.Context, merchantReference string) (Intent, bool, error) {
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Items []struct {
				InvoiceID int64  `json:"invoice_id"`
				Status    string `json:"status"`
				Amount    string `json:"amount"`
				Fiat      string `json:"fiat"`
				Payload   string `json:"payload"`
				BotURL    string `json:"bot_invoice_url"`
				WebURL    string `json:"web_app_invoice_url"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := provider.call(ctx, "getInvoices", url.Values{"count": {"100"}}, &envelope); err != nil {
		return Intent{}, false, err
	}
	if !envelope.OK {
		return Intent{}, false, ErrProviderResponse
	}
	for _, item := range envelope.Result.Items {
		if item.Payload != merchantReference {
			continue
		}
		exponent, err := currencyExponent(item.Fiat)
		if err != nil {
			return Intent{}, false, err
		}
		amount, err := parseMinor(item.Amount, exponent)
		if err != nil {
			return Intent{}, false, err
		}
		checkout := item.WebURL
		if checkout == "" {
			checkout = item.BotURL
		}
		return Intent{ProviderReference: strconv.FormatInt(item.InvoiceID, 10), Status: normalizeCryptoBotStatus(item.Status), CheckoutURL: checkout, Amount: commerce.Money{Amount: amount, Currency: item.Fiat}}, true, nil
	}
	return Intent{}, false, nil
}

func (provider *CryptoBot) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *CryptoBot) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	provided, err := hex.DecodeString(headers.Get("crypto-pay-api-signature"))
	if err != nil {
		return WebhookEvent{}, ErrInvalidSignature
	}
	secret := sha256.Sum256([]byte(provider.token))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var update struct {
		UpdateID    int64     `json:"update_id"`
		UpdateType  string    `json:"update_type"`
		RequestDate time.Time `json:"request_date"`
		Payload     struct {
			InvoiceID int64  `json:"invoice_id"`
			Status    string `json:"status"`
			Amount    string `json:"amount"`
			Fiat      string `json:"fiat"`
			Payload   string `json:"payload"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &update); err != nil || update.Payload.InvoiceID == 0 {
		return WebhookEvent{}, ErrProviderResponse
	}
	exponent, err := currencyExponent(update.Payload.Fiat)
	if err != nil {
		return WebhookEvent{}, err
	}
	amount, err := parseMinor(update.Payload.Amount, exponent)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{ID: fmt.Sprintf("%s:%d:%d", update.UpdateType, update.Payload.InvoiceID, update.UpdateID), Type: update.UpdateType, ProviderReference: strconv.FormatInt(update.Payload.InvoiceID, 10), MerchantReference: update.Payload.Payload, Status: normalizeCryptoBotStatus(update.Payload.Status), Amount: commerce.Money{Amount: amount, Currency: update.Payload.Fiat}, OccurredAt: update.RequestDate}, nil
}

func (provider *CryptoBot) call(ctx context.Context, method string, form url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Crypto-Pay-API-Token", provider.token)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := provider.http.Do(request)
	if err != nil {
		return fmt.Errorf("call CryptoBot: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrProviderResponse, response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(target)
}

func normalizeCryptoBotStatus(status string) string {
	switch status {
	case "paid":
		return "succeeded"
	case "expired":
		return "expired"
	default:
		return "pending"
	}
}

// Probe verifies the API token with getMe, which describes the authenticated
// application and moves nothing.
func (provider *CryptoBot) Probe(ctx context.Context) error {
	var response struct {
		Ok bool `json:"ok"`
	}
	if err := provider.call(ctx, "getMe", url.Values{}, &response); err != nil {
		return err
	}
	if !response.Ok {
		return fmt.Errorf("%w: getMe was rejected", ErrProviderResponse)
	}
	return nil
}
