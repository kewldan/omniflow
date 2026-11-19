package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// Anore is the hosted-checkout adapter for the Anore API (api.anore.cc/v1).
//
// Requests carry the API key as a Bearer token and an Anore-Signature header,
// a hex HMAC-SHA256 of the exact body keyed with the secret. Notifications are
// signed the same way. Amounts are roubles on the wire; invoices are RUB only.
//
// A key prefixed `an_test_` is a test key. Bedolaga never credits a payment
// made with one, and neither does this adapter: such a payment is reported
// failed, never succeeded.
type Anore struct {
	apiKey      string
	secret      string
	shopID      int
	methods     []string
	baseURL     string
	callbackURL string
	http        *http.Client
	clock       func() time.Time
}

const anoreDefaultBaseURL = "https://api.anore.cc/v1"

var (
	anoreAllowedMethods = []string{"sbp", "card", "yoomoney", "crypto"}
	anoreIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func buildAnore(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_ANORE_API_KEY")
	secret := settings.String("APP_ANORE_SECRET_KEY")
	rawShop := settings.String("APP_ANORE_SHOP_ID")
	if apiKey == "" && secret == "" && rawShop == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || secret == "" {
		return nil, errors.New("Anore needs both APP_ANORE_API_KEY and APP_ANORE_SECRET_KEY")
	}
	shopID := 0
	if rawShop != "" {
		parsed, err := strconv.Atoi(rawShop)
		if err != nil || parsed <= 0 {
			return nil, errors.New("APP_ANORE_SHOP_ID must be a positive integer")
		}
		shopID = parsed
	}
	// An account key serves several cash desks, so it needs to be told which.
	if strings.HasPrefix(apiKey, "an_") && shopID == 0 {
		return nil, errors.New("an Anore account key (an_…) requires APP_ANORE_SHOP_ID")
	}
	methods, err := anoreParseMethods(settings.String("APP_ANORE_PAYMENT_METHODS"))
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSuffix(settings.String("APP_ANORE_API_URL"), "/")
	if baseURL == "" {
		baseURL = anoreDefaultBaseURL
	}
	return &Anore{
		apiKey: apiKey, secret: secret, shopID: shopID, methods: methods, baseURL: baseURL,
		callbackURL: settings.WebhookURL("anore"), http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func anoreParseMethods(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return append([]string(nil), anoreAllowedMethods...), nil
	}
	seen := map[string]bool{}
	var methods []string
	for _, part := range strings.Split(value, ",") {
		method := strings.ToLower(strings.TrimSpace(part))
		if method == "" || seen[method] {
			continue
		}
		known := false
		for _, allowed := range anoreAllowedMethods {
			known = known || allowed == method
		}
		if !known {
			return nil, fmt.Errorf("APP_ANORE_PAYMENT_METHODS: unknown method %q", method)
		}
		seen[method] = true
		methods = append(methods, method)
	}
	if len(methods) == 0 {
		return nil, errors.New("APP_ANORE_PAYMENT_METHODS lists no method")
	}
	return methods, nil
}

func (provider *Anore) Name() string { return "anore" }

// Capabilities declares no refunds and no recurring charges: Bedolaga's
// integration covers hosted invoices only.
func (provider *Anore) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only.
func (provider *Anore) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

func (provider *Anore) isTest() bool { return strings.HasPrefix(provider.apiKey, "an_test_") }

type anoreCreateBody struct {
	Amount      json.Number `json:"amount"`
	Currency    string      `json:"currency"`
	Description string      `json:"description"`
	OrderID     string      `json:"orderId"`
	ShopID      int         `json:"shopId,omitempty"`
	Methods     []string    `json:"methods,omitempty"`
	Email       string      `json:"email,omitempty"`
	SuccessURL  string      `json:"successurl,omitempty"`
	FailURL     string      `json:"failurl,omitempty"`
	CallbackURL string      `json:"callbackUrl,omitempty"`
}

type anorePayment struct {
	Success    *bool        `json:"success"`
	Event      string       `json:"event"`
	ID         string       `json:"id"`
	OrderID    *string      `json:"orderId"`
	Amount     *json.Number `json:"amount"`
	RubAmount  *json.Number `json:"rubAmount"`
	Currency   string       `json:"currency"`
	Status     string       `json:"status"`
	Paid       *bool        `json:"paid"`
	PaymentURL string       `json:"paymentUrl"`
	ExpiresIn  *int64       `json:"expiresIn"`
}

func (provider *Anore) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) || request.Amount.Amount <= 0 {
		return Intent{}, fmt.Errorf("%w: Anore settles positive RUB amounts only", ErrUnsupported)
	}
	body := anoreCreateBody{
		Amount:      json.Number(formatMinor(request.Amount.Amount, 2)),
		Currency:    "RUB",
		Description: request.Description,
		OrderID:     request.OrderID,
		ShopID:      provider.shopID,
		Methods:     provider.methods,
		Email:       strings.TrimSpace(request.Metadata["customer_email"]),
		SuccessURL:  request.ReturnURL,
		FailURL:     request.ReturnURL,
		// Without a public URL the delivery address has to be set on the cash
		// desk instead, which Anore allows.
		CallbackURL: provider.callbackURL,
	}
	var payment anorePayment
	if err := provider.do(ctx, http.MethodPost, "/payments", body, &payment); err != nil {
		return Intent{}, err
	}
	if payment.OrderID == nil || *payment.OrderID != request.OrderID {
		return Intent{}, fmt.Errorf("%w: Anore answered for another order", ErrProviderResponse)
	}
	checkout, err := url.Parse(payment.PaymentURL)
	if err != nil || len(payment.PaymentURL) > 4096 || checkout.Scheme != "https" || checkout.Host == "" || checkout.User != nil {
		return Intent{}, fmt.Errorf("%w: Anore returned no usable payment URL", ErrProviderResponse)
	}
	intent, err := provider.intent(payment)
	if err != nil {
		return Intent{}, err
	}
	if intent.Amount != request.Amount {
		return Intent{}, fmt.Errorf("%w: Anore invoice amount differs from the order", ErrProviderResponse)
	}
	expires := int64(14400)
	if payment.ExpiresIn != nil && *payment.ExpiresIn > 0 && *payment.ExpiresIn <= 31_536_000 {
		expires = *payment.ExpiresIn
	}
	deadline := provider.clock().UTC().Add(time.Duration(expires) * time.Second)
	intent.ExpiresAt = &deadline
	return intent, nil
}

// Poll reads GET /payments/{id}. The answer does not document orderId, so the
// payment is matched on Anore's ID alone.
func (provider *Anore) Poll(ctx context.Context, reference string) (Intent, error) {
	if !anoreIDPattern.MatchString(reference) {
		return Intent{}, fmt.Errorf("%w: invalid Anore payment ID", ErrProviderResponse)
	}
	var payment anorePayment
	if err := provider.do(ctx, http.MethodGet, "/payments/"+reference, nil, &payment); err != nil {
		return Intent{}, err
	}
	if payment.ID != reference {
		return Intent{}, fmt.Errorf("%w: Anore answered for another payment", ErrProviderResponse)
	}
	return provider.intent(payment)
}

func (provider *Anore) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// VerifyWebhook checks Anore-Signature over the exact body, then requires the
// Anore-Event header to match the signed `event` and a delivery ID to be
// present. Payout events are acknowledged and ignored; only
// payment.succeeded (status paid) and payment.expired (status expired) are
// payment events.
func (provider *Anore) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get("Anore-Signature")))
	if err != nil || len(provided) != sha256.Size {
		return WebhookEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(provider.secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	var payment anorePayment
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payment); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed Anore notification", ErrProviderResponse)
	}
	event := headers.Get("Anore-Event")
	delivery := headers.Get("Anore-Delivery-Id")
	if event == "" || payment.Event != event || delivery == "" || len(delivery) > 128 {
		return WebhookEvent{}, fmt.Errorf("%w: Anore event headers do not match the signed body", ErrProviderResponse)
	}
	switch event {
	case "payout.created", "payout.processing", "payout.succeeded", "payout.failed":
		digest := sha256.Sum256(body)
		return WebhookEvent{
			ID: event + ":" + hex.EncodeToString(digest[:8]), Type: event, Status: "pending",
			OccurredAt: provider.clock().UTC(),
		}, nil
	}
	expected := map[string]string{"payment.succeeded": "paid", "payment.expired": "expired"}[event]
	if expected == "" || payment.Status != expected {
		return WebhookEvent{}, fmt.Errorf("%w: Anore event and status disagree", ErrProviderResponse)
	}
	// An absent orderId is allowed: the payment is then found by the Anore ID
	// stored at creation, never bound to an order it was not created for.
	merchant := ""
	if payment.OrderID != nil {
		merchant = *payment.OrderID
		if merchant == "" || len(merchant) > 128 {
			return WebhookEvent{}, fmt.Errorf("%w: invalid Anore order ID", ErrProviderResponse)
		}
	}
	intent, err := provider.intent(payment)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		ID:                event + ":" + payment.ID,
		Type:              event,
		ProviderReference: payment.ID,
		MerchantReference: merchant,
		Status:            intent.Status,
		Amount:            intent.Amount,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

func (provider *Anore) do(ctx context.Context, method, path string, payload any, target *anorePayment) error {
	var body io.Reader
	var encoded []byte
	if payload != nil {
		var err error
		if encoded, err = json.Marshal(payload); err != nil {
			return fmt.Errorf("encode Anore request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, provider.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create Anore request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+provider.apiKey)
	request.Header.Set("Accept", "application/json")
	if encoded != nil {
		request.Header.Set("Content-Type", "application/json")
		mac := hmac.New(sha256.New, []byte(provider.secret))
		_, _ = mac.Write(encoded)
		request.Header.Set("Anore-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	client := *provider.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call Anore: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderBody))
		return &StatusError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxProviderBody))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: undecodable Anore response", ErrProviderResponse)
	}
	if target.Success == nil || !*target.Success {
		return fmt.Errorf("%w: Anore did not report success", ErrProviderResponse)
	}
	return nil
}

// intent maps an Anore payment. `amount` is the invoice principal; a larger
// `rubAmount` is the customer-paid fee and is never settled, but one below the
// principal is a payment that did not cover the order.
func (provider *Anore) intent(payment anorePayment) (Intent, error) {
	if !anoreIDPattern.MatchString(payment.ID) {
		return Intent{}, fmt.Errorf("%w: invalid Anore payment ID", ErrProviderResponse)
	}
	if !strings.EqualFold(payment.Currency, "RUB") {
		return Intent{}, fmt.Errorf("%w: Anore payment is not in RUB", ErrProviderResponse)
	}
	if payment.Amount == nil {
		return Intent{}, fmt.Errorf("%w: Anore payment has no amount", ErrProviderResponse)
	}
	amount, err := parseMinor(payment.Amount.String(), 2)
	if err != nil || amount <= 0 {
		return Intent{}, fmt.Errorf("%w: invalid Anore amount", ErrProviderResponse)
	}
	if payment.RubAmount != nil {
		rubAmount, err := parseMinor(payment.RubAmount.String(), 2)
		if err != nil || rubAmount < amount {
			return Intent{}, fmt.Errorf("%w: Anore rouble total is below the invoice", ErrProviderResponse)
		}
	}
	var status string
	switch payment.Status {
	case "new":
		status = "pending"
	case "paid":
		status = "succeeded"
		if provider.isTest() {
			status = "failed"
		}
	case "expired":
		status = "expired"
	default:
		return Intent{}, fmt.Errorf("%w: unknown Anore status", ErrProviderResponse)
	}
	if payment.Paid != nil && *payment.Paid != (payment.Status == "paid") {
		return Intent{}, fmt.Errorf("%w: Anore paid flag contradicts its status", ErrProviderResponse)
	}
	return Intent{
		ProviderReference: payment.ID,
		Status:            status,
		CheckoutURL:       payment.PaymentURL,
		Amount:            commerce.Money{Amount: amount, Currency: "RUB"},
	}, nil
}

var _ Provider = (*Anore)(nil)
