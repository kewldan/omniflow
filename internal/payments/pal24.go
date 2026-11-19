package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

const pal24DefaultBaseURL = "https://pal24.pro/api/v1/"

// pal24PaymentMethods maps the configured method to the value bill/create
// takes in `payment_method`.
var pal24PaymentMethods = map[string]string{"sbp": "SBP", "card": "BANK_CARD"}

// Pal24 is the PayPalych (pal24.pro) bill adapter.
//
// Bills are created with a bearer API token. PayPalych notifies the shop's
// Result URL with a postback signed as upper(md5(OutSum:InvId:token)), where
// the token is the shop's signature token or, when none is set, the API
// token. Only OutSum and InvId are covered by that signature, as the
// reference integration accepts it.
type Pal24 struct {
	apiToken       string
	signatureToken string
	shopID         string
	paymentMethod  string
	baseURL        string
	http           *http.Client
	clock          func() time.Time
}

// buildPal24 reads APP_PAL24_* and constructs the adapter.
func buildPal24(settings Settings) (Provider, error) {
	apiToken := settings.String("APP_PAL24_API_TOKEN")
	shopID := settings.String("APP_PAL24_SHOP_ID")
	signatureToken := settings.String("APP_PAL24_SIGNATURE_TOKEN")
	if apiToken == "" && shopID == "" && signatureToken == "" {
		return nil, ErrNotConfigured
	}
	if apiToken == "" || shopID == "" {
		return nil, errors.New("pal24: APP_PAL24_API_TOKEN and APP_PAL24_SHOP_ID must both be set")
	}
	if signatureToken == "" {
		signatureToken = apiToken
	}
	method := strings.ToLower(settings.String("APP_PAL24_PAYMENT_METHOD"))
	if method == "" {
		method = "sbp"
	}
	apiMethod, ok := pal24PaymentMethods[method]
	if !ok {
		return nil, errors.New("pal24: APP_PAL24_PAYMENT_METHOD must be sbp or card")
	}
	baseURL := settings.String("APP_PAL24_BASE_URL")
	if baseURL == "" {
		baseURL = pal24DefaultBaseURL
	}
	timeout := settings.Int("APP_PAL24_REQUEST_TIMEOUT", 30)
	if timeout <= 0 {
		timeout = 30
	}
	return &Pal24{
		apiToken:       apiToken,
		signatureToken: signatureToken,
		shopID:         shopID,
		paymentMethod:  apiMethod,
		baseURL:        strings.TrimSuffix(baseURL, "/") + "/",
		http:           tracedClient(time.Duration(timeout) * time.Second),
		clock:          time.Now,
	}, nil
}

func (provider *Pal24) Name() string { return "pal24" }

func (provider *Pal24) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports RUB only: bills are issued with currency_in RUB.
func (provider *Pal24) SupportsCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "RUB")
}

// pal24Flag reads PayPalych's `success`, which arrives as a JSON boolean or
// as the string "true"/"false".
type pal24Flag struct {
	set   bool
	value bool
}

func (flag *pal24Flag) UnmarshalJSON(data []byte) error {
	flag.set = true
	switch strings.ToLower(strings.Trim(string(bytes.TrimSpace(data)), `"`)) {
	case "true", "1":
		flag.value = true
	case "false", "0", "null", "":
		flag.value = false
	default:
		return fmt.Errorf("pal24: unexpected success flag %s", data)
	}
	return nil
}

// pal24Text reads a field PayPalych may send as a string or as a number.
type pal24Text string

func (text *pal24Text) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*text = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil {
		*text = pal24Text(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return err
	}
	*text = pal24Text(number.String())
	return nil
}

type pal24Bill struct {
	Success    pal24Flag `json:"success"`
	ID         pal24Text `json:"id"`
	BillID     pal24Text `json:"bill_id"`
	Status     string    `json:"status"`
	Amount     pal24Text `json:"amount"`
	CurrencyIn string    `json:"currency_in"`
	LinkURL    string    `json:"link_url"`
	LinkPage   string    `json:"link_page_url"`
	Bill       *struct {
		Status     string    `json:"status"`
		Amount     pal24Text `json:"amount"`
		CurrencyIn string    `json:"currency_in"`
	} `json:"bill"`
}

func (provider *Pal24) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.SupportsCurrency(request.Amount.Currency) {
		return Intent{}, fmt.Errorf("pal24: currency %s is not supported", request.Amount.Currency)
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("pal24: amount must be positive")
	}
	body := map[string]any{
		"amount":         formatMinor(request.Amount.Amount, 2),
		"shop_id":        provider.shopID,
		"currency_in":    "RUB",
		"type":           "normal",
		"order_id":       request.OrderID,
		"payment_method": provider.paymentMethod,
	}
	if request.Description != "" {
		body["description"] = request.Description
	}
	var bill pal24Bill
	if err := provider.do(ctx, http.MethodPost, "bill/create", nil, body, &bill); err != nil {
		return Intent{}, err
	}
	if bill.BillID == "" {
		return Intent{}, fmt.Errorf("%w: pal24 bill without bill_id", ErrProviderResponse)
	}
	checkout := bill.LinkPage
	if checkout == "" {
		checkout = bill.LinkURL
	}
	status := "pending"
	if bill.Status != "" {
		status = pal24Status(bill.Status)
	}
	return Intent{ProviderReference: string(bill.BillID), Status: status, CheckoutURL: checkout, Amount: request.Amount}, nil
}

func (provider *Pal24) Poll(ctx context.Context, reference string) (Intent, error) {
	var bill pal24Bill
	if err := provider.do(ctx, http.MethodGet, "bill/status", url.Values{"id": {reference}}, nil, &bill); err != nil {
		return Intent{}, err
	}
	status, amount, currency := bill.Status, bill.Amount, bill.CurrencyIn
	if bill.Bill != nil {
		if status == "" {
			status = bill.Bill.Status
		}
		if amount == "" {
			amount = bill.Bill.Amount
		}
		if currency == "" {
			currency = bill.Bill.CurrencyIn
		}
	}
	if status == "" {
		return Intent{}, fmt.Errorf("%w: pal24 bill status is empty", ErrProviderResponse)
	}
	intent := Intent{ProviderReference: reference, Status: pal24Status(status)}
	if amount != "" {
		money, err := pal24Money(string(amount), currency)
		if err != nil {
			return Intent{}, err
		}
		intent.Amount = money
	} else if intent.Status == "succeeded" {
		// A settled bill without an amount cannot be classified safely.
		return Intent{}, fmt.Errorf("%w: pal24 paid bill without amount", ErrProviderResponse)
	}
	return intent, nil
}

func (provider *Pal24) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

// CancelIntent deactivates the bill through bill/toggle_activity, so its
// payment page stops accepting money.
func (provider *Pal24) CancelIntent(ctx context.Context, reference string) error {
	var response pal24Bill
	return provider.do(ctx, http.MethodPost, "bill/toggle_activity", nil, map[string]any{"id": reference, "active": 0}, &response)
}

// Probe reads the merchant balance: authenticated, and moves nothing.
func (provider *Pal24) Probe(ctx context.Context) error {
	var response pal24Bill
	return provider.do(ctx, http.MethodGet, "merchant/balance", nil, nil, &response)
}

func (provider *Pal24) VerifyWebhook(headers http.Header, body []byte) (WebhookEvent, error) {
	fields := pal24ParsePostback(headers.Get("Content-Type"), body)
	invID, outSum, status, signature := fields["InvId"], fields["OutSum"], fields["Status"], fields["SignatureValue"]
	if _, ok := fields["InvId"]; !ok {
		return WebhookEvent{}, ErrProviderResponse
	}
	for _, key := range []string{"OutSum", "Status", "SignatureValue"} {
		if _, ok := fields[key]; !ok {
			return WebhookEvent{}, ErrProviderResponse
		}
	}
	expected := pal24Sign(outSum, invID, provider.signatureToken)
	if !hmac.Equal([]byte(expected), []byte(strings.ToUpper(signature))) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	currency := fields["CurrencyIn"]
	if currency == "" {
		// Bills are only ever issued in RUB by this adapter.
		currency = "RUB"
	}
	money, err := pal24Money(outSum, currency)
	if err != nil {
		return WebhookEvent{}, err
	}
	billID := ""
	for _, key := range []string{"bill_id", "billId", "BillId", "BillID"} {
		if value := fields[key]; value != "" {
			billID = value
			break
		}
	}
	transactionID := fields["TrsId"]
	if transactionID == "" {
		transactionID = fields["TrsID"]
	}
	upper := strings.ToUpper(status)
	return WebhookEvent{
		ID:                upper + ":" + invID + ":" + transactionID,
		Type:              upper,
		ProviderReference: billID,
		MerchantReference: invID,
		Status:            pal24Status(upper),
		Amount:            money,
		OccurredAt:        provider.clock().UTC(),
	}, nil
}

// pal24ParsePostback reads a postback sent as a form or as JSON, as the
// reference integration accepts both.
func pal24ParsePostback(contentType string, body []byte) map[string]string {
	trimmed := bytes.TrimSpace(body)
	if strings.HasPrefix(strings.ToLower(contentType), "application/json") || (len(trimmed) > 0 && trimmed[0] == '{') {
		if fields, ok := pal24ParseJSON(trimmed); ok {
			return fields
		}
	}
	values, err := url.ParseQuery(string(trimmed))
	if err != nil {
		return map[string]string{}
	}
	fields := make(map[string]string, len(values))
	for key, list := range values {
		if len(list) > 0 {
			fields[key] = list[len(list)-1]
		}
	}
	return fields
}

func pal24ParseJSON(body []byte) (map[string]string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}
	fields := make(map[string]string, len(raw))
	for key, value := range raw {
		var text pal24Text
		if err := json.Unmarshal(value, &text); err != nil {
			fields[key] = string(value)
			continue
		}
		fields[key] = string(text)
	}
	return fields, true
}

func (provider *Pal24) do(ctx context.Context, method, path string, query url.Values, body any, target *pal24Bill) error {
	endpoint := provider.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+provider.apiToken)
	if err := doJSON(ctx, provider.http, method, endpoint, headers, body, target); err != nil {
		return err
	}
	if target.Success.set && !target.Success.value {
		return fmt.Errorf("%w: pal24 answered success=false", ErrProviderResponse)
	}
	return nil
}

func pal24Status(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESS", "OVERPAID", "PAID":
		return "succeeded"
	case "FAIL":
		return "failed"
	case "CANCELLED":
		return "cancelled"
	case "PROCESS":
		return "processing"
	default:
		// NEW, UNDERPAID and anything unrecognised: not settled.
		return "pending"
	}
}

func pal24Money(value, currency string) (commerce.Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := currencyExponent(currency)
	if err != nil {
		return commerce.Money{}, err
	}
	amount, err := parseMinor(strings.TrimSpace(value), exponent)
	if err != nil {
		return commerce.Money{}, err
	}
	return commerce.Money{Amount: amount, Currency: currency}, nil
}

func pal24Sign(outSum, invID, token string) string {
	digest := md5.Sum([]byte(outSum + ":" + invID + ":" + token))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}

var (
	_ Provider        = (*Pal24)(nil)
	_ CurrencySupport = (*Pal24)(nil)
	_ ConnectionProbe = (*Pal24)(nil)
	_ Canceller       = (*Pal24)(nil)
)
