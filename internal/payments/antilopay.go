package payments

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Antilopay is the adapter for the Antilopay payment API
// (lk.antilopay.com/api/v1, AntilopayAPI.pdf sections 3.3, 5.1–5.2 and 10).
//
// Every request body is signed SHA256withRSA with the merchant's private key;
// every notification is signed by Antilopay and checked with the project's
// public key. Amounts travel as roubles with kopek precision, and only RUB is
// accepted.
//
// Antilopay looks payments up by the merchant's order ID, never by its own
// payment ID, so the order ID is the provider reference Omniflow stores and
// polls with. Antilopay's own payment ID is kept in the intent metadata.
type Antilopay struct {
	secretID    string
	projectID   string
	privateKey  *rsa.PrivateKey
	publicKey   *rsa.PublicKey
	productName string
	productType string
	preferred   string
	baseURL     string
	http        *http.Client
	clock       func() time.Time
}

const antilopayDefaultBaseURL = "https://lk.antilopay.com/api/v1"

// antilopayRefusalCodes are the documented creation refusals. A duplicate
// order (5), a server error, or an unknown reply proves nothing about whether
// a payment exists, so none of those is a refusal.
var antilopayRefusalCodes = map[int]bool{
	1: true, 2: true, 3: true, 4: true, 6: true, 7: true, 9: true, 10: true,
	11: true, 12: true, 13: true, 14: true, 15: true, 16: true, 17: true,
	18: true, 19: true, 20: true, 21: true, 22: true, 23: true, 25: true,
	26: true, 27: true, 29: true, 30: true, 31: true, 32: true, 100: true,
}

const (
	antilopayCodeDuplicateOrder = 5
	antilopayCodeNotFound       = 8
)

func buildAntilopay(settings Settings) (Provider, error) {
	secretID := settings.String("APP_ANTILOPAY_SECRET_ID")
	projectID := settings.String("APP_ANTILOPAY_PROJECT_ID")
	privatePEM := settings.String("APP_ANTILOPAY_PRIVATE_KEY")
	publicPEM := settings.String("APP_ANTILOPAY_PUBLIC_KEY")
	if secretID == "" && projectID == "" && privatePEM == "" && publicPEM == "" {
		return nil, ErrNotConfigured
	}
	if secretID == "" || projectID == "" || privatePEM == "" || publicPEM == "" {
		return nil, errors.New("Antilopay needs APP_ANTILOPAY_SECRET_ID, APP_ANTILOPAY_PROJECT_ID, APP_ANTILOPAY_PRIVATE_KEY and APP_ANTILOPAY_PUBLIC_KEY together")
	}
	privateKey, err := antilopayParsePrivateKey(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("APP_ANTILOPAY_PRIVATE_KEY: %w", err)
	}
	publicKey, err := antilopayParsePublicKey(publicPEM)
	if err != nil {
		return nil, fmt.Errorf("APP_ANTILOPAY_PUBLIC_KEY: %w", err)
	}
	preferred := strings.ToUpper(settings.String("APP_ANTILOPAY_PREFER_METHOD"))
	switch preferred {
	case "", "SBP", "CARD_RU", "SBER_PAY":
	default:
		return nil, fmt.Errorf("APP_ANTILOPAY_PREFER_METHOD must be SBP, CARD_RU or SBER_PAY, got %q", preferred)
	}
	productName := settings.String("APP_ANTILOPAY_PRODUCT_NAME")
	if productName == "" {
		productName = "VPN подписка"
	}
	productType := settings.String("APP_ANTILOPAY_PRODUCT_TYPE")
	if productType == "" {
		productType = "services"
	}
	return &Antilopay{
		secretID: secretID, projectID: projectID, privateKey: privateKey, publicKey: publicKey,
		productName: productName, productType: productType, preferred: preferred,
		baseURL: antilopayDefaultBaseURL, http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *Antilopay) Name() string { return "antilopay" }

// Capabilities declares no refunds and no recurring charges: the integration
// this adapter ports uses only payment/create and payment/check.
func (provider *Antilopay) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only.
func (provider *Antilopay) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

type antilopayCustomer struct {
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type antilopayCreateBody struct {
	ProjectID     string            `json:"project_identificator"`
	Amount        json.Number       `json:"amount"`
	OrderID       string            `json:"order_id"`
	Currency      string            `json:"currency"`
	ProductName   string            `json:"product_name"`
	ProductType   string            `json:"product_type"`
	Description   string            `json:"description"`
	Customer      antilopayCustomer `json:"customer"`
	PreferMethods []string          `json:"prefer_methods,omitempty"`
	SuccessURL    string            `json:"success_url,omitempty"`
	FailURL       string            `json:"fail_url,omitempty"`
	MerchantExtra string            `json:"merchant_extra,omitempty"`
}

type antilopayPayment struct {
	Code           *int         `json:"code"`
	Type           string       `json:"type"`
	PaymentID      string       `json:"payment_id"`
	OrderID        string       `json:"order_id"`
	Status         string       `json:"status"`
	Amount         *json.Number `json:"amount"`
	OriginalAmount *json.Number `json:"original_amount"`
	Currency       string       `json:"currency"`
	PaymentURL     string       `json:"payment_url"`
}

// UsesCustomerContact asks for the payer's verified e-mail: Antilopay refuses
// a payment that names no customer contact.
func (provider *Antilopay) UsesCustomerContact() bool { return true }

// Create opens a hosted payment. Antilopay requires a real customer contact
// and Bedolaga deliberately stopped inventing one, so a request without
// `customer_email` or `customer_phone` metadata is refused before any call.
func (provider *Antilopay) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) || request.Amount.Amount <= 0 {
		return Intent{}, fmt.Errorf("%w: Antilopay settles positive RUB amounts only", ErrUnsupported)
	}
	customer := antilopayCustomer{
		Email: strings.TrimSpace(request.Metadata["customer_email"]),
		Phone: strings.TrimSpace(request.Metadata["customer_phone"]),
	}
	if customer.Email == "" && customer.Phone == "" {
		return Intent{}, fmt.Errorf("%w: Antilopay requires the customer's email or phone", ErrUnsupported)
	}
	body := antilopayCreateBody{
		ProjectID:     provider.projectID,
		Amount:        json.Number(formatMinor(request.Amount.Amount, 2)),
		OrderID:       request.OrderID,
		Currency:      "RUB",
		ProductName:   provider.productName,
		ProductType:   provider.productType,
		Description:   request.Description,
		Customer:      customer,
		SuccessURL:    request.ReturnURL,
		FailURL:       request.ReturnURL,
		MerchantExtra: antilopayTruncate(request.OrderID, 255),
	}
	if provider.preferred != "" {
		body.PreferMethods = []string{provider.preferred}
	}
	var payment antilopayPayment
	if err := provider.post(ctx, "/payment/create", body, &payment); err != nil {
		return Intent{}, err
	}
	checkout, err := url.Parse(payment.PaymentURL)
	if payment.PaymentID == "" || err != nil || checkout.Scheme != "https" || checkout.Host == "" {
		return Intent{}, fmt.Errorf("%w: Antilopay returned no payment ID or no HTTPS payment URL", ErrProviderResponse)
	}
	return Intent{
		ProviderReference: request.OrderID,
		Status:            "pending",
		CheckoutURL:       payment.PaymentURL,
		Amount:            request.Amount,
		Metadata:          map[string]string{"antilopay_payment_id": payment.PaymentID},
	}, nil
}

// Poll reads payment/check by the merchant order ID, which is the reference
// this adapter stores.
func (provider *Antilopay) Poll(ctx context.Context, reference string) (Intent, error) {
	payment, err := provider.check(ctx, reference)
	if err != nil {
		return Intent{}, err
	}
	return antilopayIntent(payment)
}

// Recover finds a payment Antilopay already holds for the order, so a create
// whose answer was lost is not repeated: Antilopay rejects a second payment
// with the same order ID, and that rejection does not say whether the first
// one exists. Code 8 is Antilopay's documented "payment not found".
func (provider *Antilopay) Recover(ctx context.Context, orderID string) (Intent, bool, error) {
	payment, err := provider.check(ctx, orderID)
	var apiErr *antilopayAPIError
	if errors.As(err, &apiErr) && apiErr.code == antilopayCodeNotFound {
		return Intent{}, false, nil
	}
	if err != nil {
		return Intent{}, false, err
	}
	intent, err := antilopayIntent(payment)
	if err != nil {
		return Intent{}, false, err
	}
	return intent, true, nil
}

func (provider *Antilopay) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook checks X-Apay-Callback, a base64 SHA256withRSA signature of
// the exact body made with Antilopay's key, against the project's public key.
// A notification whose type is not "payment" is accepted and ignored.
func (provider *Antilopay) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(headers.Get("X-Apay-Callback")))
	if err != nil || len(signature) == 0 {
		return WebhookEvent{}, ErrInvalidSignature
	}
	digest := sha256.Sum256(body)
	if rsa.VerifyPKCS1v15(provider.publicKey, crypto.SHA256, digest[:], signature) != nil {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var payment antilopayPayment
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payment); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed Antilopay notification", ErrProviderResponse)
	}
	if payment.Type != "payment" {
		return WebhookEvent{
			ID: "ignored:" + payment.Type + ":" + hex.EncodeToString(digest[:8]), Type: payment.Type,
			ProviderReference: payment.OrderID, MerchantReference: payment.OrderID,
			Status: "pending", OccurredAt: provider.clock().UTC(),
		}, nil
	}
	if payment.PaymentID == "" || payment.OrderID == "" {
		return WebhookEvent{}, fmt.Errorf("%w: Antilopay notification has no payment or order ID", ErrProviderResponse)
	}
	if _, statusErr := antilopayStatus(payment.Status); statusErr != nil {
		// A status this adapter does not know is acknowledged and ignored, as
		// Bedolaga does: it is never a settlement.
		return WebhookEvent{
			ID: "unknown:" + payment.PaymentID + ":" + hex.EncodeToString(digest[:8]), Type: "payment.unknown",
			ProviderReference: payment.OrderID, MerchantReference: payment.OrderID,
			Status: "pending", OccurredAt: provider.clock().UTC(),
		}, nil
	}
	intent, err := antilopayIntent(payment)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID:                strings.ToLower(payment.Status) + ":" + payment.PaymentID,
		Type:              "payment." + strings.ToLower(payment.Status),
		ProviderReference: payment.OrderID,
		MerchantReference: payment.OrderID,
		Status:            intent.Status,
		Amount:            intent.Amount,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

// Probe checks an order ID that cannot exist. Answering it requires a valid
// secret ID and signature, and the documented "payment not found" (code 8)
// is the expected success; nothing is created.
func (provider *Antilopay) Probe(ctx context.Context) error {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	_, err := provider.check(ctx, "omniflow-probe-"+hex.EncodeToString(nonce))
	var apiErr *antilopayAPIError
	if errors.As(err, &apiErr) && apiErr.code == antilopayCodeNotFound {
		return nil
	}
	return err
}

func (provider *Antilopay) check(ctx context.Context, orderID string) (antilopayPayment, error) {
	var payment antilopayPayment
	body := map[string]string{"project_identificator": provider.projectID, "order_id": orderID}
	if err := provider.post(ctx, "/payment/check", body, &payment); err != nil {
		return antilopayPayment{}, err
	}
	if payment.OrderID != orderID {
		return antilopayPayment{}, fmt.Errorf("%w: Antilopay answered for another order", ErrProviderResponse)
	}
	return payment, nil
}

// antilopayAPIError is an answer Antilopay gave with a non-zero code. It wraps
// a StatusError so Refused can classify it: a documented refusal reads as a
// client error, a duplicate order as a conflict, anything else as the HTTP
// status Antilopay actually sent.
type antilopayAPIError struct {
	code   int
	status *StatusError
}

func (err *antilopayAPIError) Error() string {
	return fmt.Sprintf("Antilopay API code %d: %v", err.code, err.status)
}

func (err *antilopayAPIError) Unwrap() error { return err.status }

func (provider *Antilopay) post(ctx context.Context, path string, payload, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Antilopay request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	signature, err := rsa.SignPKCS1v15(nil, provider.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return fmt.Errorf("sign Antilopay request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create Antilopay request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Apay-Secret-Id", provider.secretID)
	request.Header.Set("X-Apay-Sign", base64.StdEncoding.EncodeToString(signature))
	request.Header.Set("X-Apay-Sign-Version", "1")
	client := *provider.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call Antilopay: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody))
	if err != nil {
		return fmt.Errorf("read Antilopay response: %w", err)
	}
	var envelope struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &StatusError{StatusCode: response.StatusCode}
		}
		return fmt.Errorf("%w: Antilopay answered without a code", ErrProviderResponse)
	}
	code := *envelope.Code
	if response.StatusCode == http.StatusOK && code == 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("decode Antilopay response: %w", err)
		}
		return nil
	}
	status := response.StatusCode
	switch {
	case code == antilopayCodeDuplicateOrder:
		status = http.StatusConflict
	case code == antilopayCodeNotFound && status == http.StatusOK:
		status = http.StatusNotFound
	case antilopayRefusalCodes[code] && (status == http.StatusOK || status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusUnprocessableEntity):
		if status == http.StatusOK {
			status = http.StatusUnprocessableEntity
		}
	case status == http.StatusOK:
		// An undocumented non-zero code proves nothing either way.
		status = http.StatusBadGateway
	}
	return &antilopayAPIError{code: code, status: &StatusError{StatusCode: status}}
}

// antilopayIntent maps a payment notification or check answer. Only
// `original_amount` is what the customer was charged for the order; `amount`
// is net of Antilopay's fee and is never used for settlement.
func antilopayIntent(payment antilopayPayment) (Intent, error) {
	status, err := antilopayStatus(payment.Status)
	if err != nil {
		return Intent{}, err
	}
	intent := Intent{ProviderReference: payment.OrderID, Status: status}
	if payment.PaymentID != "" {
		intent.Metadata = map[string]string{"antilopay_payment_id": payment.PaymentID}
	}
	if payment.OriginalAmount != nil {
		amount, parseErr := parseMinor(payment.OriginalAmount.String(), 2)
		if parseErr != nil || amount <= 0 {
			return Intent{}, fmt.Errorf("%w: invalid Antilopay original_amount", ErrProviderResponse)
		}
		intent.Amount = commerce.Money{Amount: amount, Currency: strings.ToUpper(payment.Currency)}
	}
	if status == "succeeded" && (payment.OriginalAmount == nil || intent.Amount.Currency != "RUB") {
		return Intent{}, fmt.Errorf("%w: Antilopay success without a RUB original_amount", ErrProviderResponse)
	}
	return intent, nil
}

// antilopayStatus normalises Antilopay's payment statuses. CHARGEBACK is a
// disputed payment that was reversed, which Omniflow reports as failed.
func antilopayStatus(status string) (string, error) {
	switch status {
	case "PENDING":
		return "pending", nil
	case "SUCCESS":
		return "succeeded", nil
	case "FAIL":
		return "failed", nil
	case "CHARGEBACK":
		// A disputed payment the bank pulled back after it settled.
		return StatusChargeback, nil
	case "CANCEL":
		return "cancelled", nil
	case "EXPIRED":
		return "expired", nil
	default:
		return "", fmt.Errorf("%w: unknown Antilopay status", ErrProviderResponse)
	}
}

// antilopayKeyBytes accepts a key as Bedolaga stores it — base64 of a PEM or
// DER key — and also a bare PEM block.
func antilopayKeyBytes(value string) ([]byte, error) {
	raw := []byte(value)
	if !strings.Contains(value, "-----BEGIN") {
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
		if err != nil {
			return nil, errors.New("key is neither PEM nor base64")
		}
		raw = decoded
	}
	if block, _ := pem.Decode(raw); block != nil {
		return block.Bytes, nil
	}
	return raw, nil
}

func antilopayParsePrivateKey(value string) (*rsa.PrivateKey, error) {
	der, err := antilopayKeyBytes(value)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("not an RSA private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA private key")
	}
	return key, nil
}

func antilopayParsePublicKey(value string) (*rsa.PublicKey, error) {
	der, err := antilopayKeyBytes(value)
	if err != nil {
		return nil, err
	}
	if parsed, err := x509.ParsePKIXPublicKey(der); err == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			return key, nil
		}
		return nil, errors.New("not an RSA public key")
	}
	key, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		return nil, errors.New("not an RSA public key")
	}
	return key, nil
}

func antilopayTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

var _ Provider = (*Antilopay)(nil)
