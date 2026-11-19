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

// PayHot settles RUB payments through the HOT Pay API v2.
//
// Requests carry a production key (`phk_v2_…`) as a bearer token, and every
// creating call carries an `Idempotency-Key`. Notifications are signed — an
// HMAC-SHA256 over `<timestamp>.<body>` in the `PayHot-Signature` header —
// and are still only hints: as in the reference integration, the payment is
// re-read through the API before anything settles, so the adapter declares
// PollVerifiedWebhooks as well.
//
// Recurring charges run against a mandate HOT Pay issued when a customer
// consented on a first payment. The mandate ID is the saved-method token;
// the charge is requested by the merchant and tracked as its own object, so
// its provider reference carries a `recurring:` prefix to keep the two
// lookups apart.
type PayHot struct {
	apiKey     string
	secrets    []string
	method     string
	successURL string
	failureURL string
	baseURL    string
	http       *http.Client
	clock      func() time.Time
}

const (
	payhotDefaultURL       = "https://app.pay.hot/api/v2"
	payhotMaxBody          = 65536
	payhotSignatureMaxSkew = 300
	payhotRecurringPrefix  = "recurring:"
)

var (
	payhotOrigins         = map[string]bool{"https://app.pay.hot/api/v2": true, "https://gapi.pay.hot/api/v2": true}
	payhotCheckoutMethods = map[string]bool{"card": true, "sbp": true, "crypto": true, "applepay": true, "googlepay": true}
	payhotStatuses        = map[string]bool{
		"requires_payment_method": true, "requires_action": true, "processing": true, "authorized": true,
		"partially_captured": true, "succeeded": true, "failed": true, "cancelled": true, "refunded": true,
		"partially_refunded": true, "manual_reconciliation": true,
	}
	payhotChargeStatuses = map[string]bool{"processing": true, "succeeded": true, "failed": true, "cancelled": true}
	payhotEventTypes     = map[string]string{
		"payment.succeeded": "payment", "payment.failed": "payment", "payment.manual_reconciliation": "payment",
		"payment.captured": "payment", "payment.cancelled": "payment", "payment.refunded": "payment",
		"recurring_charge.cancelled": "recurring_charge", "recurring_charge.skipped": "recurring_charge",
	}
	payhotSignaturePattern   = regexp.MustCompile(`^t=([0-9]{1,12}),v2=([a-f0-9]{64})$`)
	payhotUUIDPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	payhotMinorPattern       = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	payhotPrintablePattern   = regexp.MustCompile(`^[!-~]+$`)
	payhotIdempotencyPattern = regexp.MustCompile(`^[!-~]{16,128}$`)
	payhotDeliveryIDPattern  = regexp.MustCompile(`^[!-~]{1,128}$`)
	payhotAttemptPattern     = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
)

func buildPayHot(settings Settings) (Provider, error) {
	apiKey := settings.String("APP_PAYHOT_API_KEY")
	secret := settings.String("APP_PAYHOT_WEBHOOK_SECRET")
	if apiKey == "" && secret == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || secret == "" {
		return nil, errors.New("PayHot needs both APP_PAYHOT_API_KEY and APP_PAYHOT_WEBHOOK_SECRET")
	}
	if !strings.HasPrefix(apiKey, "phk_v2_") || !payhotPrintablePattern.MatchString(apiKey) {
		return nil, errors.New("APP_PAYHOT_API_KEY must be a production HOT Pay v2 key (phk_v2_…)")
	}
	baseURL := strings.TrimSuffix(settings.String("APP_PAYHOT_API_URL"), "/")
	if baseURL == "" {
		baseURL = payhotDefaultURL
	}
	if !payhotOrigins[baseURL] {
		return nil, errors.New("APP_PAYHOT_API_URL must be https://app.pay.hot/api/v2 or https://gapi.pay.hot/api/v2")
	}
	method := strings.ToLower(settings.String("APP_PAYHOT_PAYMENT_METHOD"))
	if method == "" {
		method = "card"
	}
	if !payhotCheckoutMethods[method] {
		return nil, fmt.Errorf("APP_PAYHOT_PAYMENT_METHOD %q is not one of card, sbp, crypto, applepay, googlepay", method)
	}
	timeout := settings.Int("APP_PAYHOT_REQUEST_TIMEOUT", 30)
	if timeout <= 0 || timeout > 60 {
		return nil, errors.New("APP_PAYHOT_REQUEST_TIMEOUT must be between 1 and 60 seconds")
	}
	successURL, failureURL := settings.String("APP_PAYHOT_SUCCESS_URL"), settings.String("APP_PAYHOT_FAIL_URL")
	for name, value := range map[string]string{"APP_PAYHOT_SUCCESS_URL": successURL, "APP_PAYHOT_FAIL_URL": failureURL} {
		if value != "" && !payhotHTTPS(value) {
			return nil, fmt.Errorf("%s must be an absolute HTTPS URL", name)
		}
	}
	secrets := []string{secret}
	if previous := settings.String("APP_PAYHOT_PREVIOUS_WEBHOOK_SECRET"); previous != "" {
		secrets = append(secrets, previous)
	}
	client := tracedClient(time.Duration(timeout) * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &PayHot{
		apiKey: apiKey, secrets: secrets, method: method, successURL: successURL, failureURL: failureURL,
		baseURL: baseURL, http: client, clock: time.Now,
	}, nil
}

func (provider *PayHot) Name() string { return "payhot" }

// Capabilities declares mandate-based recurring charges, polling, and signed
// webhooks. Refunds are not declared: HOT Pay marks its refund route
// deprecated and the reference integration never calls it.
func (provider *PayHot) Capabilities() Capabilities {
	return Capabilities{Recurring: true, Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: HOT Pay accepts order amounts in RUB.
func (provider *PayHot) SupportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == "RUB"
}

// WebhooksNeedPolling makes the payment service re-read a notified payment
// before applying it, as the reference integration does after the signature.
func (provider *PayHot) WebhooksNeedPolling() bool { return true }

type payhotPayment struct {
	ID                     string  `json:"id"`
	Status                 string  `json:"status"`
	AmountMinor            string  `json:"amount_minor"`
	Currency               string  `json:"currency"`
	MerchantOrderReference string  `json:"merchant_order_reference"`
	PaymentMethod          string  `json:"payment_method"`
	Environment            *string `json:"environment"`
	ExpiresAt              string  `json:"expires_at"`
	Origin                 string  `json:"origin"`
	RecurringChargeID      *string `json:"recurring_charge_id"`
	// RecurringEnrollment is present on a payment created with consent to
	// keep the instrument. An active enrollment names the mandate a later
	// recurring charge presents.
	RecurringEnrollment *struct {
		MandateID string `json:"mandate_id"`
		Status    string `json:"status"`
	} `json:"recurring_enrollment"`
}

type payhotCheckout struct {
	URL string `json:"url"`
}

type payhotPaymentEnvelope struct {
	Payment  *payhotPayment  `json:"payment"`
	Checkout *payhotCheckout `json:"checkout"`
}

type payhotCharge struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	AmountMinor       string `json:"amount_minor"`
	Currency          string `json:"currency"`
	MandateID         string `json:"mandate_id"`
	PaymentMethod     string `json:"payment_method"`
	MerchantReference string `json:"merchant_recurring_charge_reference"`
}

func (provider *PayHot) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("PayHot requires a positive amount")
	}
	reference := request.OrderID
	if len(reference) == 0 || len(reference) > 128 || strings.TrimSpace(reference) != reference {
		return Intent{}, errors.New("PayHot order reference must be 1 to 128 characters")
	}
	body := map[string]any{
		"merchant_order_reference": reference,
		"amount_minor":             strconv.FormatInt(request.Amount.Amount, 10),
		"currency":                 "RUB",
		"payment_method":           provider.method,
		"customer":                 map[string]string{"id": payhotCustomerID(reference)},
	}
	if customerID := request.Metadata["customer_id"]; request.SavePaymentMethod && customerID != "" && len(customerID) <= 50 {
		// Enrolment needs a customer identifier that stays the same across
		// payments, so the mandate belongs to the person rather than to one
		// order. The empty object is HOT Pay's explicit consent marker.
		body["recurring_enrollment"] = map[string]any{}
		body["customer"] = map[string]string{"id": customerID}
	}
	success, failure := provider.successURL, provider.failureURL
	if payhotHTTPS(request.ReturnURL) {
		success = request.ReturnURL
		if failure == "" {
			failure = request.ReturnURL
		}
	}
	if success != "" {
		body["success_return_url"] = success
	}
	if failure != "" {
		body["failure_return_url"] = failure
	}
	var envelope payhotPaymentEnvelope
	if err := provider.do(ctx, http.MethodPost, "/payments", payhotIdempotencyKey(request.IdempotencyKey), body, &envelope); err != nil {
		return Intent{}, err
	}
	payment, err := payhotValidPayment(envelope.Payment, reference, request.Amount.Amount)
	if err != nil {
		return Intent{}, err
	}
	intent, err := payhotPaymentIntent(payment)
	if err != nil {
		return Intent{}, err
	}
	if payment.Status == "succeeded" || payment.Status == "refunded" || payment.Status == "partially_refunded" {
		// Creation is not proof of payment: the settled state still arrives
		// through a poll. The intent is left processing for that.
		intent.Status = "processing"
		return intent, nil
	}
	checkoutURL := ""
	if envelope.Checkout != nil {
		checkoutURL = envelope.Checkout.URL
	}
	if checkoutURL == "" {
		// A repeated create answers without the one-time checkout link;
		// HOT Pay issues a fresh session for the same payment instead.
		var reissued payhotPaymentEnvelope
		if err := provider.do(ctx, http.MethodPost, "/payments/"+payment.ID+"/checkout-sessions", "", nil, &reissued); err != nil {
			return Intent{}, err
		}
		if reissued.Checkout != nil {
			checkoutURL = reissued.Checkout.URL
		}
	}
	if !payhotHTTPS(checkoutURL) {
		return Intent{}, fmt.Errorf("%w: payment has no HTTPS checkout link", ErrProviderResponse)
	}
	intent.CheckoutURL = checkoutURL
	return intent, nil
}

// Poll re-reads a payment, or a recurring charge when the reference carries
// the `recurring:` prefix.
func (provider *PayHot) Poll(ctx context.Context, reference string) (Intent, error) {
	if chargeID, ok := strings.CutPrefix(reference, payhotRecurringPrefix); ok {
		charge, err := provider.charge(ctx, chargeID)
		if err != nil {
			return Intent{}, err
		}
		return payhotChargeIntent(charge)
	}
	if !payhotUUIDPattern.MatchString(reference) {
		return Intent{}, fmt.Errorf("%w: not a PayHot payment ID", ErrProviderResponse)
	}
	var envelope payhotPaymentEnvelope
	if err := provider.do(ctx, http.MethodGet, "/payments/"+reference, "", nil, &envelope); err != nil {
		return Intent{}, err
	}
	payment, err := payhotValidPayment(envelope.Payment, "", 0)
	if err != nil {
		return Intent{}, err
	}
	if payment.ID != reference {
		return Intent{}, fmt.Errorf("%w: payment mismatch", ErrProviderResponse)
	}
	return payhotPaymentIntent(payment)
}

func (provider *PayHot) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// ChargeSaved charges a mandate without the customer present.
//
// The mandate is read first: it names the instrument family the charge must
// declare, and a mandate that is not active is refused before anything is
// sent. The charge reference is derived from the idempotency key, so a retry
// sends the identical request under the identical key and HOT Pay answers
// with the charge it already made. The charge is then re-read, because the
// creating call's answer is not the canonical state.
func (provider *PayHot) ChargeSaved(ctx context.Context, request SavedChargeRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("PayHot requires a positive amount")
	}
	if !payhotUUIDPattern.MatchString(request.MethodToken) {
		return Intent{}, errors.New("PayHot saved method is not a mandate ID")
	}
	var mandateEnvelope struct {
		Mandate *struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			Currency      string `json:"currency"`
			PaymentMethod string `json:"payment_method"`
		} `json:"mandate"`
	}
	if err := provider.do(ctx, http.MethodGet, "/mandates/"+request.MethodToken, "", nil, &mandateEnvelope); err != nil {
		return Intent{}, err
	}
	mandate := mandateEnvelope.Mandate
	if mandate == nil || mandate.ID != request.MethodToken || mandate.Currency != "RUB" || (mandate.PaymentMethod != "card" && mandate.PaymentMethod != "sbp") {
		return Intent{}, fmt.Errorf("%w: mandate does not match", ErrProviderResponse)
	}
	if mandate.Status != "active" {
		return Intent{}, fmt.Errorf("%w: mandate is %s", ErrProviderResponse, mandate.Status)
	}
	key := payhotIdempotencyKey(request.IdempotencyKey)
	body := map[string]string{
		"merchant_recurring_charge_reference": payhotChargeReference(key),
		"mandate_id":                          mandate.ID,
		"amount_minor":                        strconv.FormatInt(request.Amount.Amount, 10),
		"currency":                            "RUB",
		"payment_method":                      mandate.PaymentMethod,
	}
	var created struct {
		Charge *payhotCharge `json:"recurring_charge"`
	}
	if err := provider.do(ctx, http.MethodPost, "/recurring-charges", key, body, &created); err != nil {
		return Intent{}, err
	}
	charge := created.Charge
	if charge == nil || !payhotUUIDPattern.MatchString(charge.ID) || charge.MerchantReference != body["merchant_recurring_charge_reference"] ||
		charge.MandateID != body["mandate_id"] || charge.AmountMinor != body["amount_minor"] || charge.Currency != "RUB" ||
		charge.PaymentMethod != body["payment_method"] || !payhotChargeStatuses[charge.Status] {
		return Intent{}, fmt.Errorf("%w: recurring charge does not match the request", ErrProviderResponse)
	}
	canonical, err := provider.charge(ctx, charge.ID)
	if err != nil {
		// The charge exists; its state will arrive by webhook or poll.
		return Intent{ProviderReference: payhotRecurringPrefix + charge.ID, Status: "processing", Amount: request.Amount}, nil
	}
	return payhotChargeIntent(canonical)
}

// VerifyWebhook authenticates a HOT Pay notification and extracts the object
// it is about. The status and amount it carries are replaced by a poll
// before anything settles.
func (provider *PayHot) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	if len(body) > payhotMaxBody {
		return WebhookEvent{}, ErrProviderResponse
	}
	for _, name := range []string{"PayHot-Signature", "PayHot-Event-Id", "PayHot-Delivery-Id", "PayHot-Delivery-Attempt", "PayHot-Api-Version"} {
		if len(headers.Values(name)) > 1 {
			return WebhookEvent{}, ErrProviderResponse
		}
	}
	if !payhotSignatureValid(provider.secrets, body, headers.Get("PayHot-Signature"), provider.clock()) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	if payhotStrictJSON(body) != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	var notification struct {
		APIVersion  string          `json:"api_version"`
		EventID     string          `json:"event_id"`
		EventType   string          `json:"event_type"`
		Environment *string         `json:"environment"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &notification); err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	expectedObject, known := payhotEventTypes[notification.EventType]
	if notification.APIVersion != "v2" || !payhotUUIDPattern.MatchString(notification.EventID) ||
		headers.Get("PayHot-Event-Id") != notification.EventID || headers.Get("PayHot-Api-Version") != "v2" ||
		!payhotDeliveryIDPattern.MatchString(headers.Get("PayHot-Delivery-Id")) ||
		!payhotAttemptPattern.MatchString(headers.Get("PayHot-Delivery-Attempt")) ||
		!known || (notification.Environment != nil && *notification.Environment != "production") {
		return WebhookEvent{}, ErrProviderResponse
	}
	var data struct {
		Object string `json:"object"`
		payhotPayment
	}
	if err := json.Unmarshal(notification.Data, &data); err != nil || data.Object != expectedObject || !payhotUUIDPattern.MatchString(data.ID) {
		return WebhookEvent{}, ErrProviderResponse
	}
	if data.Environment != nil && *data.Environment != "production" {
		return WebhookEvent{}, ErrProviderResponse
	}
	event := WebhookEvent{ID: notification.EventID, Type: notification.EventType, OccurredAt: provider.clock().UTC()}
	switch {
	case expectedObject == "recurring_charge":
		event.ProviderReference = payhotRecurringPrefix + data.ID
		event.Status = normalizePayHotStatus(data.Status)
	case data.Origin == "recurring" || data.RecurringChargeID != nil:
		if data.RecurringChargeID == nil || !payhotUUIDPattern.MatchString(*data.RecurringChargeID) {
			return WebhookEvent{}, ErrProviderResponse
		}
		event.ProviderReference = payhotRecurringPrefix + *data.RecurringChargeID
		event.Status = normalizePayHotStatus(data.Status)
	default:
		event.ProviderReference = data.ID
		event.MerchantReference = data.MerchantOrderReference
		event.Status = normalizePayHotStatus(data.Status)
	}
	if data.AmountMinor != "" {
		money, err := payhotMoney(data.AmountMinor, data.Currency)
		if err != nil {
			return WebhookEvent{}, err
		}
		event.Amount = money
	}
	return event, nil
}

// WebhookAck answers with the body the reference integration sends once a
// notification is durably recorded.
func (provider *PayHot) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "application/json", []byte(`{"success":true}`)
}

func (provider *PayHot) charge(ctx context.Context, chargeID string) (payhotCharge, error) {
	if !payhotUUIDPattern.MatchString(chargeID) {
		return payhotCharge{}, fmt.Errorf("%w: not a PayHot charge ID", ErrProviderResponse)
	}
	var envelope struct {
		Charge *payhotCharge `json:"recurring_charge"`
	}
	if err := provider.do(ctx, http.MethodGet, "/recurring-charges/"+chargeID, "", nil, &envelope); err != nil {
		return payhotCharge{}, err
	}
	charge := envelope.Charge
	if charge == nil || charge.ID != chargeID || !payhotChargeStatuses[charge.Status] || charge.Currency != "RUB" || !payhotMinorPattern.MatchString(charge.AmountMinor) {
		return payhotCharge{}, fmt.Errorf("%w: recurring charge does not match", ErrProviderResponse)
	}
	return *charge, nil
}

func (provider *PayHot) do(ctx context.Context, method, path, idempotencyKey string, body, target any) error {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+provider.apiKey)
	if idempotencyKey != "" {
		headers.Set("Idempotency-Key", idempotencyKey)
	}
	return doJSON(ctx, provider.http, method, provider.baseURL+path, headers, body, target)
}

// payhotValidPayment checks the identity and principal of a payment object.
// An empty reference or zero amount skips that comparison.
func payhotValidPayment(payment *payhotPayment, reference string, amount int64) (payhotPayment, error) {
	if payment == nil || !payhotUUIDPattern.MatchString(payment.ID) || payment.Currency != "RUB" ||
		!payhotMinorPattern.MatchString(payment.AmountMinor) || !payhotStatuses[payment.Status] {
		return payhotPayment{}, fmt.Errorf("%w: invalid payment object", ErrProviderResponse)
	}
	if payment.Environment != nil && *payment.Environment != "production" {
		return payhotPayment{}, fmt.Errorf("%w: payment from a non-production environment", ErrProviderResponse)
	}
	if reference != "" && payment.MerchantOrderReference != reference {
		return payhotPayment{}, fmt.Errorf("%w: order mismatch", ErrProviderResponse)
	}
	if amount != 0 && payment.AmountMinor != strconv.FormatInt(amount, 10) {
		return payhotPayment{}, fmt.Errorf("%w: principal mismatch", ErrProviderResponse)
	}
	return *payment, nil
}

func payhotPaymentIntent(payment payhotPayment) (Intent, error) {
	money, err := payhotMoney(payment.AmountMinor, payment.Currency)
	if err != nil {
		return Intent{}, err
	}
	intent := Intent{ProviderReference: payment.ID, Status: normalizePayHotStatus(payment.Status), Amount: money}
	if enrollment := payment.RecurringEnrollment; enrollment != nil && enrollment.Status == "active" &&
		payhotUUIDPattern.MatchString(enrollment.MandateID) && intent.Status == "succeeded" {
		intent.BoundMethod = &BoundMethod{Token: enrollment.MandateID, Label: truncateLabel("HOT Pay " + payment.PaymentMethod)}
	}
	if payment.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, payment.ExpiresAt)
		if err != nil {
			return Intent{}, fmt.Errorf("%w: invalid expiry", ErrProviderResponse)
		}
		utc := expires.UTC()
		intent.ExpiresAt = &utc
	}
	return intent, nil
}

func payhotChargeIntent(charge payhotCharge) (Intent, error) {
	money, err := payhotMoney(charge.AmountMinor, charge.Currency)
	if err != nil {
		return Intent{}, err
	}
	return Intent{ProviderReference: payhotRecurringPrefix + charge.ID, Status: normalizePayHotStatus(charge.Status), Amount: money}, nil
}

func payhotMoney(amountMinor, currency string) (commerce.Money, error) {
	if currency != "RUB" || !payhotMinorPattern.MatchString(amountMinor) {
		return commerce.Money{}, fmt.Errorf("%w: invalid amount", ErrProviderResponse)
	}
	minor, err := strconv.ParseInt(amountMinor, 10, 64)
	if err != nil {
		return commerce.Money{}, fmt.Errorf("%w: invalid amount", ErrProviderResponse)
	}
	return commerce.Money{Amount: minor, Currency: "RUB"}, nil
}

func payhotSignatureValid(secrets []string, body []byte, header string, now time.Time) bool {
	match := payhotSignaturePattern.FindStringSubmatch(header)
	if match == nil {
		return false
	}
	timestamp, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return false
	}
	if skew := now.Unix() - timestamp; skew > payhotSignatureMaxSkew || skew < -payhotSignatureMaxSkew {
		return false
	}
	signed := append([]byte(match[1]+"."), body...)
	valid := false
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(signed)
		// Every secret is checked, so timing does not reveal which matched.
		valid = hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(match[2])) || valid
	}
	return valid
}

// payhotStrictJSON rejects a body with a repeated object key at any depth, or
// anything after the top-level value, which an ordinary decode would accept
// by silently keeping one of the values.
func payhotStrictJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := payhotStrictValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func payhotStrictValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate field")
			}
			seen[key] = true
			if err := payhotStrictValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := payhotStrictValue(decoder, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token()
	return err
}

// payhotIdempotencyKey passes a key HOT Pay accepts (16–128 printable ASCII
// characters) through unchanged and maps any other to a stable digest, so the
// same Omniflow key always reaches HOT Pay as the same key.
func payhotIdempotencyKey(key string) string {
	if payhotIdempotencyPattern.MatchString(key) {
		return key
	}
	digest := sha256.Sum256([]byte(key))
	return "omniflow-" + hex.EncodeToString(digest[:])
}

// payhotChargeReference derives the opaque merchant reference HOT Pay
// requires for a recurring charge: letters only after the prefix, in groups
// short enough not to look like a card number or a token.
func payhotChargeReference(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	var reference strings.Builder
	reference.WriteString("renew-")
	for index, value := range digest {
		if index > 0 && index%8 == 0 {
			reference.WriteByte('.')
		}
		reference.WriteByte('a' + value%26)
	}
	return reference.String()
}

// payhotCustomerID is the stable customer identifier HOT Pay asks for. The
// order reference is used, as the reference integration does for a buyer it
// has no account for, so no customer identifier leaves Omniflow.
func payhotCustomerID(reference string) string {
	if payhotDeliveryIDPattern.MatchString(reference) {
		return reference
	}
	digest := sha256.Sum256([]byte(reference))
	return hex.EncodeToString(digest[:])
}

func payhotHTTPS(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.ContainsFunc(raw, func(r rune) bool { return r < 33 }) {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil
}

// normalizePayHotStatus maps payment and recurring-charge statuses. A
// refunded payment is not a payment to settle; an authorisation or a
// partial capture is not full payment.
func normalizePayHotStatus(status string) string {
	switch status {
	case "succeeded":
		return "succeeded"
	case "requires_action":
		return "requires_action"
	case "processing", "authorized", "partially_captured", "manual_reconciliation":
		return "processing"
	case "failed":
		return "failed"
	case "refunded":
		return StatusRefunded
	case "cancelled", "partially_refunded":
		return "cancelled"
	default:
		return "pending"
	}
}

var (
	_ Provider             = (*PayHot)(nil)
	_ CurrencySupport      = (*PayHot)(nil)
	_ RecurringCharger     = (*PayHot)(nil)
	_ PollVerifiedWebhooks = (*PayHot)(nil)
	_ WebhookAcknowledger  = (*PayHot)(nil)
)
