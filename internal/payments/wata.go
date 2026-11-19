package payments

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Wata settles through WATA (wata.pro) payment links.
//
// Links are created and read with a bearer access token. Notifications are
// signed with RSA PKCS#1 v1.5 over SHA-512 of the raw body, base64 in
// X-Signature; the verifying public key is published by WATA at
// `<base>/public-key` and cached.
type Wata struct {
	token          string
	baseURL        string
	paymentType    string
	description    string
	successURL     string
	failURL        string
	linkTTL        int
	publicKeyURL   string
	publicKeyCache time.Duration
	timeout        time.Duration
	http           *http.Client
	clock          func() time.Time

	keyMu      sync.Mutex
	key        *rsa.PublicKey
	keyExpires time.Time
}

const (
	wataDefaultBaseURL = "https://api.wata.pro/api/h2h"
	// wataDefaultPaymentType is Bedolaga's default link type.
	wataDefaultPaymentType = "OneTime"
	// wataMinimumTTL is the shortest link lifetime sent: WATA rejects an
	// expiration within ten minutes, and Bedolaga keeps a five-minute margin.
	wataMinimumTTL = 15
	wataCurrency   = "RUB"
)

func buildWata(settings Settings) (Provider, error) {
	token := settings.String("APP_WATA_ACCESS_TOKEN")
	if token == "" {
		return nil, ErrNotConfigured
	}
	baseURL := strings.TrimSuffix(settings.String("APP_WATA_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = wataDefaultBaseURL
	}
	if parsed, err := url.Parse(baseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("APP_WATA_BASE_URL must be an absolute URL")
	}
	paymentType := settings.String("APP_WATA_PAYMENT_TYPE")
	if paymentType == "" {
		paymentType = wataDefaultPaymentType
	}
	ttl := 0
	if raw := settings.String("APP_WATA_LINK_TTL_MINUTES"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return nil, errors.New("APP_WATA_LINK_TTL_MINUTES must be a non-negative number of minutes")
		}
		ttl = value
	}
	timeout := settings.Int("APP_WATA_REQUEST_TIMEOUT", 30)
	if timeout <= 0 {
		return nil, errors.New("APP_WATA_REQUEST_TIMEOUT must be a positive number of seconds")
	}
	cacheSeconds := settings.Int("APP_WATA_PUBLIC_KEY_CACHE_SECONDS", 3600)
	if cacheSeconds < 0 {
		return nil, errors.New("APP_WATA_PUBLIC_KEY_CACHE_SECONDS must not be negative")
	}
	publicKeyURL := settings.String("APP_WATA_PUBLIC_KEY_URL")
	if publicKeyURL == "" {
		publicKeyURL = baseURL + "/public-key"
	}
	return &Wata{
		token: token, baseURL: baseURL, paymentType: paymentType,
		description: settings.String("APP_WATA_PAYMENT_DESCRIPTION"),
		successURL:  settings.String("APP_WATA_SUCCESS_REDIRECT_URL"),
		failURL:     settings.String("APP_WATA_FAIL_REDIRECT_URL"),
		linkTTL:     ttl, publicKeyURL: publicKeyURL,
		publicKeyCache: time.Duration(cacheSeconds) * time.Second,
		timeout:        time.Duration(timeout) * time.Second,
		http:           tracedClient(time.Duration(timeout) * time.Second), clock: time.Now,
	}, nil
}

func (provider *Wata) Name() string { return "wata" }

func (provider *Wata) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB, the only currency Bedolaga opens WATA links in.
func (provider *Wata) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == wataCurrency
}

type wataLink struct {
	ID                 string      `json:"id"`
	URL                string      `json:"url"`
	PaymentURL         string      `json:"paymentUrl"`
	Status             string      `json:"status"`
	OrderID            string      `json:"orderId"`
	Amount             json.Number `json:"amount"`
	Currency           string      `json:"currency"`
	ExpirationDateTime string      `json:"expirationDateTime"`
}

type wataTransaction struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	StatusName string      `json:"statusName"`
	Amount     json.Number `json:"amount"`
	Currency   string      `json:"currency"`
}

func (provider *Wata) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("WATA amount must be positive")
	}
	exponent, err := currencyExponent(wataCurrency)
	if err != nil {
		return Intent{}, err
	}
	description := request.Description
	if description == "" {
		description = provider.description
	}
	if description == "" {
		description = request.OrderID
	}
	body := map[string]any{
		"amount":      json.Number(formatMinor(request.Amount.Amount, exponent)),
		"currency":    wataCurrency,
		"description": description,
		"orderId":     request.OrderID,
		"type":        provider.paymentType,
	}
	success := request.ReturnURL
	if success == "" {
		success = provider.successURL
	}
	if success != "" {
		body["successRedirectUrl"] = success
	}
	fail := provider.failURL
	if fail == "" {
		fail = request.ReturnURL
	}
	if fail != "" {
		body["failRedirectUrl"] = fail
	}
	if provider.linkTTL > 0 {
		minutes := max(provider.linkTTL, wataMinimumTTL)
		body["expirationDateTime"] = provider.clock().UTC().Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339)
	}
	var link wataLink
	if err := doJSON(ctx, provider.http, http.MethodPost, provider.baseURL+"/links", provider.headers(), body, &link); err != nil {
		return Intent{}, err
	}
	checkout := link.URL
	if checkout == "" {
		checkout = link.PaymentURL
	}
	if link.ID == "" || checkout == "" {
		return Intent{}, fmt.Errorf("%w: WATA returned no payment link", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: link.ID, Status: "pending", CheckoutURL: checkout, Amount: request.Amount}
	if expires, err := time.Parse(time.RFC3339, link.ExpirationDateTime); err == nil {
		expires = expires.UTC()
		intent.ExpiresAt = &expires
	}
	return intent, nil
}

// Poll reads the link and, once WATA reports it closed or paid, looks for the
// paid transaction behind it — the link alone carries no settlement, so only
// a paid transaction settles, with that transaction's amount.
func (provider *Wata) Poll(ctx context.Context, reference string) (Intent, error) {
	var link wataLink
	if err := doJSON(ctx, provider.http, http.MethodGet, provider.baseURL+"/links/"+url.PathEscape(reference), provider.headers(), nil, &link); err != nil {
		return Intent{}, err
	}
	checkout := link.URL
	if checkout == "" {
		checkout = link.PaymentURL
	}
	intent := Intent{ProviderReference: reference, Status: "pending", CheckoutURL: checkout}
	switch strings.ToLower(link.Status) {
	case "closed", "paid":
	default:
		return intent, nil
	}
	query := url.Values{"skipCount": {"0"}, "maxResultCount": {"5"}, "statuses": {"Paid"}, "paymentLinkId": {reference}}
	if link.OrderID != "" {
		query.Set("orderId", link.OrderID)
	}
	var page struct {
		Items []wataTransaction `json:"items"`
	}
	if err := doJSON(ctx, provider.http, http.MethodGet, provider.baseURL+"/transactions?"+query.Encode(), provider.headers(), nil, &page); err != nil {
		return Intent{}, err
	}
	for _, transaction := range page.Items {
		status := transaction.Status
		if status == "" {
			status = transaction.StatusName
		}
		if !strings.EqualFold(status, "paid") {
			continue
		}
		amount, err := wataAmount(transaction.Amount, transaction.Currency)
		if err != nil {
			return Intent{}, err
		}
		intent.Status, intent.Amount = "succeeded", amount
		return intent, nil
	}
	return intent, nil
}

func (provider *Wata) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *Wata) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(headers.Get("X-Signature")))
	if err != nil || len(signature) == 0 {
		return WebhookEvent{}, ErrInvalidSignature
	}
	key, err := provider.publicKey()
	if err != nil {
		return WebhookEvent{}, err
	}
	digest := sha512.Sum512(body)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA512, digest[:], signature); err != nil {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var notification struct {
		OrderID           string      `json:"orderId"`
		PaymentLinkID     string      `json:"paymentLinkId"`
		TransactionID     string      `json:"transactionId"`
		TransactionStatus string      `json:"transactionStatus"`
		TransactionType   string      `json:"transactionType"`
		Amount            json.Number `json:"amount"`
		Currency          string      `json:"currency"`
	}
	if err := json.Unmarshal(body, &notification); err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	transactionStatus := strings.TrimSpace(notification.TransactionStatus)
	if transactionStatus == "" || (notification.OrderID == "" && notification.PaymentLinkID == "") {
		return WebhookEvent{}, ErrProviderResponse
	}
	status := wataStatus(transactionStatus)
	var amount commerce.Money
	if notification.Amount != "" || status == "succeeded" {
		amount, err = wataAmount(notification.Amount, notification.Currency)
		if err != nil {
			return WebhookEvent{}, err
		}
	}
	id := transactionStatus + ":" + notification.TransactionID
	if notification.TransactionID == "" {
		id = transactionStatus + ":" + notification.PaymentLinkID + ":" + notification.OrderID
	}
	return WebhookEvent{
		ID: id, Type: notification.TransactionType, ProviderReference: notification.PaymentLinkID,
		MerchantReference: notification.OrderID, Status: status, Amount: amount,
		OccurredAt: provider.clock().UTC(),
	}, nil
}

// WebhookAck answers as Bedolaga did.
func (provider *Wata) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"status":"ok"}`)
}

// Probe lists one transaction, an authenticated read that moves nothing.
func (provider *Wata) Probe(ctx context.Context) error {
	var page struct {
		Items []wataTransaction `json:"items"`
	}
	return doJSON(ctx, provider.http, http.MethodGet, provider.baseURL+"/transactions?skipCount=0&maxResultCount=1", provider.headers(), nil, &page)
}

func (provider *Wata) headers() http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+provider.token)
	return headers
}

// publicKey returns the cached verifying key, fetching it when the cache is
// empty or stale. A failed refresh falls back to the key already held, as
// Bedolaga does, so a WATA outage on the key endpoint does not reject
// genuine notifications.
func (provider *Wata) publicKey() (*rsa.PublicKey, error) {
	provider.keyMu.Lock()
	defer provider.keyMu.Unlock()
	now := provider.clock()
	if provider.key != nil && (provider.keyExpires.IsZero() || now.Before(provider.keyExpires)) {
		return provider.key, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), provider.timeout)
	defer cancel()
	key, err := provider.fetchPublicKey(ctx)
	if err != nil {
		if provider.key != nil {
			return provider.key, nil
		}
		return nil, err
	}
	provider.key = key
	provider.keyExpires = time.Time{}
	if provider.publicKeyCache > 0 {
		provider.keyExpires = now.Add(provider.publicKeyCache)
	}
	return key, nil
}

func (provider *Wata) fetchPublicKey(ctx context.Context) (*rsa.PublicKey, error) {
	var response struct {
		Value string `json:"value"`
	}
	if err := doJSON(ctx, provider.http, http.MethodGet, provider.publicKeyURL, make(http.Header), nil, &response); err != nil {
		return nil, fmt.Errorf("fetch WATA public key: %w", err)
	}
	block, _ := pem.Decode([]byte(response.Value))
	if block == nil {
		return nil, fmt.Errorf("%w: WATA public key is not PEM", ErrProviderResponse)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		if rsaKey, rsaErr := x509.ParsePKCS1PublicKey(block.Bytes); rsaErr == nil {
			return rsaKey, nil
		}
		return nil, fmt.Errorf("%w: WATA public key cannot be parsed", ErrProviderResponse)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: WATA public key is not RSA", ErrProviderResponse)
	}
	return key, nil
}

// wataStatus maps a transaction status; an unknown one stays pending.
func wataStatus(status string) string {
	switch strings.ToLower(status) {
	case "paid":
		return "succeeded"
	case "declined":
		return "failed"
	default:
		return "pending"
	}
}

func wataAmount(raw json.Number, currency string) (commerce.Money, error) {
	if raw == "" {
		return commerce.Money{}, fmt.Errorf("%w: WATA payment carries no amount", ErrProviderResponse)
	}
	currency = strings.ToUpper(currency)
	if currency == "" {
		currency = wataCurrency
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	amount, err := parseMinor(raw.String(), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

var (
	_ Provider            = (*Wata)(nil)
	_ CurrencySupport     = (*Wata)(nil)
	_ ConnectionProbe     = (*Wata)(nil)
	_ WebhookAcknowledger = (*Wata)(nil)
)
