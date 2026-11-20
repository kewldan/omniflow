package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// FreeKassa settles through FreeKassa (freekassa.com).
//
// A checkout is opened one of two ways, exactly as Bedolaga does it: by
// signing a link to the hosted payment-selection form at pay.fk.money with
// secret word 1, or — when a payment system is pinned or the API is forced —
// by creating an order through the HMAC-signed API at api.fk.life and handing
// the customer the location it returns. Settlement notifications are form
// posts signed with secret word 2 and must be answered with the bare word YES.
type FreeKassa struct {
	core        *freekassaCore
	secretWord1 string
	useAPI      bool
	formURL     string
}

const (
	freekassaAPIBaseURL = "https://api.fk.life/v1"
	freekassaFormURL    = "https://pay.fk.money/"
	// freekassaFormLanguage is the payment form language. Bedolaga passed the
	// customer's language; Omniflow's create request does not carry one.
	freekassaFormLanguage = "ru"
	// freekassaStatusPaid is the order status the API reports for a paid order.
	freekassaStatusPaid = "1"
)

// freekassaCore is the part FreeKassa shares with KassaAI: both are served by
// the same api.fk.life order API and send the same MERCHANT_ID / AMOUNT /
// MERCHANT_ORDER_ID / SIGN notification.
type freekassaCore struct {
	slug            string
	shopID          int64
	apiKey          string
	secretWord2     string
	currency        string
	exponent        int
	paymentSystemID int
	// methodOrder and methodIDs are APP_*_PAYMENT_METHODS: the payment
	// systems an installation shows as separate buttons.
	methodOrder     []string
	methodIDs       map[string]int
	serverIP        string
	notificationURL string
	baseURL         string
	http            *http.Client
	clock           func() time.Time
}

func buildFreeKassa(settings Settings) (Provider, error) {
	required := map[string]string{
		"APP_FREEKASSA_SHOP_ID":       settings.String("APP_FREEKASSA_SHOP_ID"),
		"APP_FREEKASSA_API_KEY":       settings.String("APP_FREEKASSA_API_KEY"),
		"APP_FREEKASSA_SECRET_WORD_1": settings.String("APP_FREEKASSA_SECRET_WORD_1"),
		"APP_FREEKASSA_SECRET_WORD_2": settings.String("APP_FREEKASSA_SECRET_WORD_2"),
	}
	if err := freekassaRequire("FreeKassa", required); err != nil {
		return nil, err
	}
	paymentSystemID, err := freekassaPaymentSystem(
		"APP_FREEKASSA_PAYMENT_SYSTEM_ID",
		settings.String("APP_FREEKASSA_PAYMENT_SYSTEM_ID"), 0,
		map[string]int{"sbp": 44, "card": 36},
	)
	if err != nil {
		return nil, err
	}
	methodOrder, methodIDs, err := parseMethodIDs("APP_FREEKASSA_PAYMENT_METHODS",
		settings.String("APP_FREEKASSA_PAYMENT_METHODS"), map[string]int{"sbp": 44, "card": 36})
	if err != nil {
		return nil, err
	}
	useAPI := settings.Bool("APP_FREEKASSA_USE_API", false) || paymentSystemID != 0 || len(methodIDs) > 0
	core, err := freekassaNewCore(
		"freekassa", "FreeKassa", required["APP_FREEKASSA_SHOP_ID"],
		required["APP_FREEKASSA_API_KEY"], required["APP_FREEKASSA_SECRET_WORD_2"],
		settings.String("APP_FREEKASSA_CURRENCY"),
	)
	if err != nil {
		return nil, err
	}
	core.serverIP = settings.String("APP_FREEKASSA_SERVER_IP")
	if useAPI && core.serverIP == "" {
		return nil, errors.New("FreeKassa API orders require APP_FREEKASSA_SERVER_IP")
	}
	if useAPI && paymentSystemID == 0 {
		// Bedolaga falls back to payment system 1 when the API is forced
		// without a pinned method.
		paymentSystemID = 1
	}
	core.paymentSystemID = paymentSystemID
	core.methodOrder, core.methodIDs = methodOrder, methodIDs
	return &FreeKassa{core: core, secretWord1: required["APP_FREEKASSA_SECRET_WORD_1"], useAPI: useAPI, formURL: freekassaFormURL}, nil
}

// freekassaRequire returns ErrNotConfigured when none of the values is set and
// a descriptive error naming the missing ones when only some are.
func freekassaRequire(label string, values map[string]string) error {
	var missing []string
	for name, value := range values {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == len(values) {
		return ErrNotConfigured
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%s is partially configured: %s must also be set", label, strings.Join(missing, ", "))
	}
	return nil
}

// freekassaPaymentSystem reads a payment system ID: a number, one of the
// named aliases, or empty for the fallback.
func freekassaPaymentSystem(name, value string, fallback int, aliases map[string]int) (int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback, nil
	}
	if id, ok := aliases[value]; ok {
		return id, nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		names := make([]string, 0, len(aliases))
		for alias := range aliases {
			names = append(names, alias)
		}
		sort.Strings(names)
		return 0, fmt.Errorf("%s must be a positive payment system ID or one of %s", name, strings.Join(names, ", "))
	}
	return id, nil
}

func freekassaNewCore(slug, label, shopID, apiKey, secretWord2, currency string) (*freekassaCore, error) {
	shop, err := strconv.ParseInt(shopID, 10, 64)
	if err != nil || shop <= 0 {
		return nil, fmt.Errorf("%s shop ID must be a positive integer", label)
	}
	currency = strings.ToUpper(currency)
	if currency == "" {
		currency = "RUB"
	}
	exponent, err := currencyExponent(currency)
	if err != nil {
		return nil, fmt.Errorf("%s currency %q is not supported", label, currency)
	}
	return &freekassaCore{
		slug: slug, shopID: shop, apiKey: apiKey, secretWord2: secretWord2,
		currency: currency, exponent: exponent, baseURL: freekassaAPIBaseURL,
		http: tracedClient(30 * time.Second), clock: time.Now,
	}, nil
}

func (provider *FreeKassa) Name() string { return "freekassa" }

func (provider *FreeKassa) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the single currency this shop is configured to take.
func (provider *FreeKassa) SupportsCurrency(currency string) bool {
	return provider.core.supportsCurrency(currency)
}

func (provider *FreeKassa) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.core.supportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("FreeKassa amount must be positive")
	}
	if provider.useAPI {
		return provider.core.createOrder(ctx, request, nil)
	}
	amount := freekassaAmount(request.Amount.Amount, provider.core.exponent)
	shop := strconv.FormatInt(provider.core.shopID, 10)
	sum := md5.Sum([]byte(shop + ":" + amount + ":" + provider.secretWord1 + ":" + provider.core.currency + ":" + request.OrderID))
	query := url.Values{
		"m": {shop}, "oa": {amount}, "currency": {provider.core.currency},
		"o": {request.OrderID}, "s": {hex.EncodeToString(sum[:])}, "lang": {freekassaFormLanguage},
	}
	return Intent{
		ProviderReference: request.OrderID, Status: "pending",
		CheckoutURL: provider.formURL + "?" + query.Encode(), Amount: request.Amount,
	}, nil
}

func (provider *FreeKassa) Poll(ctx context.Context, reference string) (Intent, error) {
	return provider.core.poll(ctx, reference)
}

func (provider *FreeKassa) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *FreeKassa) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	return provider.core.verifyWebhook(body)
}

// WebhookAck answers with the bare word FreeKassa waits for; anything else is
// treated as a failed delivery and retried.
func (provider *FreeKassa) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "text/plain; charset=utf-8", []byte("YES")
}

// Probe reads the shop balance, the API's authenticated read-only call.
func (provider *FreeKassa) Probe(ctx context.Context) error {
	return provider.core.probe(ctx)
}

func (core *freekassaCore) supportsCurrency(currency string) bool {
	return strings.ToUpper(strings.TrimSpace(currency)) == core.currency
}

// freekassaParam is one field of an API request. Numbers are sent as JSON
// numbers and signed by their literal text, so the signed string and the body
// can never disagree.
type freekassaParam struct {
	key    string
	value  string
	number bool
}

// freekassaSignature is the API signature: values ordered by key, joined with
// "|", HMAC-SHA256 with the API key, hex encoded.
func freekassaSignature(apiKey string, params []freekassaParam) string {
	ordered := append([]freekassaParam(nil), params...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].key < ordered[right].key })
	values := make([]string, len(ordered))
	for index, param := range ordered {
		values[index] = param.value
	}
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte(strings.Join(values, "|")))
	return hex.EncodeToString(mac.Sum(nil))
}

func (core *freekassaCore) call(ctx context.Context, path string, params []freekassaParam, target any) error {
	params = append(params,
		freekassaParam{key: "shopId", value: strconv.FormatInt(core.shopID, 10), number: true},
		freekassaParam{key: "nonce", value: strconv.FormatInt(core.clock().UnixNano(), 10), number: true},
	)
	body := make(map[string]any, len(params)+1)
	for _, param := range params {
		if param.number {
			body[param.key] = json.Number(param.value)
		} else {
			body[param.key] = param.value
		}
	}
	body["signature"] = freekassaSignature(core.apiKey, params)
	return doJSON(ctx, core.http, http.MethodPost, core.baseURL+path, make(http.Header), body, target)
}

type freekassaEnvelope struct {
	Type     string           `json:"type"`
	Message  string           `json:"message"`
	Location string           `json:"location"`
	Orders   []freekassaOrder `json:"orders"`
}

type freekassaOrder struct {
	MerchantOrderID freekassaText `json:"merchant_order_id"`
	PaymentID       freekassaText `json:"paymentId"`
	Status          freekassaText `json:"status"`
	Amount          json.Number   `json:"amount"`
	Currency        string        `json:"currency"`
}

// freekassaText accepts a JSON string or number, since the API is not
// consistent about which it sends for identifiers and statuses.
type freekassaText string

func (text *freekassaText) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		*text = ""
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		*text = freekassaText(value)
		return nil
	}
	*text = freekassaText(raw)
	return nil
}

func (core *freekassaCore) createOrder(ctx context.Context, request CreateRequest, extra []freekassaParam) (Intent, error) {
	params := []freekassaParam{
		{key: "paymentId", value: request.OrderID},
		{key: "i", value: strconv.Itoa(core.systemFor(request.Method)), number: true},
		{key: "email", value: freekassaPayerEmail(request)},
		{key: "ip", value: core.serverIP},
		{key: "amount", value: freekassaAmount(request.Amount.Amount, core.exponent), number: true},
		{key: "currency", value: core.currency},
	}
	params = append(params, extra...)
	var envelope freekassaEnvelope
	if err := core.call(ctx, "/orders/create", params, &envelope); err != nil {
		return Intent{}, err
	}
	if envelope.Type == "error" || envelope.Location == "" {
		return Intent{}, fmt.Errorf("%w: %s order was not created", ErrProviderResponse, core.slug)
	}
	return Intent{ProviderReference: request.OrderID, Status: "pending", CheckoutURL: envelope.Location, Amount: request.Amount}, nil
}

// systemFor is the payment system a chosen method maps to, or the pinned
// default for the default method and for a method this adapter does not know.
func (core *freekassaCore) systemFor(method string) int {
	if id, ok := core.methodIDs[method]; ok {
		return id
	}
	return core.paymentSystemID
}

// Methods lists the payment systems shown as separate buttons.
func (provider *FreeKassa) Methods() []string { return provider.core.methodOrder }

// freekassaPayerEmail is the email the order API requires. Omniflow does not
// pass a customer's address to the provider; it sends the placeholder form
// Bedolaga used for KassaAI orders, derived from the merchant order ID.
func freekassaPayerEmail(request CreateRequest) string {
	return "user_" + request.OrderID + "@telegram.org"
}

func (core *freekassaCore) poll(ctx context.Context, reference string) (Intent, error) {
	var envelope freekassaEnvelope
	if err := core.call(ctx, "/orders", []freekassaParam{{key: "paymentId", value: reference}}, &envelope); err != nil {
		return Intent{}, err
	}
	if envelope.Type == "error" {
		return Intent{}, fmt.Errorf("%w: %s order lookup was rejected", ErrProviderResponse, core.slug)
	}
	for _, order := range envelope.Orders {
		key := string(order.MerchantOrderID)
		if key == "" {
			key = string(order.PaymentID)
		}
		if key != reference {
			continue
		}
		if string(order.Status) != freekassaStatusPaid {
			return Intent{ProviderReference: reference, Status: "pending"}, nil
		}
		currency := strings.ToUpper(order.Currency)
		if currency == "" {
			currency = core.currency
		}
		exponent, err := currencyExponent(currency)
		if err != nil {
			return Intent{}, err
		}
		if order.Amount == "" {
			return Intent{}, fmt.Errorf("%w: paid %s order carries no amount", ErrProviderResponse, core.slug)
		}
		amount, err := parseMinor(order.Amount.String(), exponent)
		if err != nil {
			return Intent{}, err
		}
		return Intent{ProviderReference: reference, Status: "succeeded", Amount: commerce.Money{Amount: amount, Currency: currency}}, nil
	}
	// The order API knows an order only once the customer reached the form,
	// so an absent order is a checkout nobody has paid yet.
	return Intent{ProviderReference: reference, Status: "pending"}, nil
}

func (core *freekassaCore) probe(ctx context.Context) error {
	var envelope freekassaEnvelope
	if err := core.call(ctx, "/balance", nil, &envelope); err != nil {
		return err
	}
	if envelope.Type == "error" {
		return fmt.Errorf("%w: %s balance was rejected", ErrProviderResponse, core.slug)
	}
	return nil
}

// verifyWebhook checks a settlement notification:
// SIGN = md5(MERCHANT_ID:AMOUNT:secret word 2:MERCHANT_ORDER_ID).
//
// The amount is signed as FreeKassa sent it. Bedolaga signed it after a float
// round trip ("100.00" became "100"), so that spelling is accepted as well;
// both spell the same number, and that number is what is settled.
func (core *freekassaCore) verifyWebhook(body []byte) (WebhookEvent, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return WebhookEvent{}, ErrProviderResponse
	}
	merchantID, rawAmount := form.Get("MERCHANT_ID"), form.Get("AMOUNT")
	orderID, sign, intID := form.Get("MERCHANT_ORDER_ID"), form.Get("SIGN"), form.Get("intid")
	if merchantID == "" || rawAmount == "" || orderID == "" || sign == "" || intID == "" {
		return WebhookEvent{}, ErrProviderResponse
	}
	if merchantID != strconv.FormatInt(core.shopID, 10) {
		return WebhookEvent{}, ErrInvalidSignature
	}
	amount, amountErr := parseMinor(rawAmount, core.exponent)
	candidates := []string{rawAmount}
	if amountErr == nil {
		candidates = append(candidates, freekassaAmount(amount, core.exponent))
	}
	provided := []byte(strings.ToLower(sign))
	valid := false
	for _, candidate := range candidates {
		sum := md5.Sum([]byte(merchantID + ":" + candidate + ":" + core.secretWord2 + ":" + orderID))
		if hmac.Equal(provided, []byte(hex.EncodeToString(sum[:]))) {
			valid = true
		}
	}
	if !valid {
		return WebhookEvent{}, ErrInvalidSignature
	}
	if amountErr != nil {
		return WebhookEvent{}, amountErr
	}
	return WebhookEvent{
		ID: "paid:" + intID, Type: "payment", ProviderReference: orderID, MerchantReference: orderID,
		Status: "succeeded", Amount: commerce.Money{Amount: amount, Currency: core.currency},
		OccurredAt: core.clock().UTC(),
	}, nil
}

// freekassaAmount spells an amount the way the API and the form are signed:
// an integer when whole ("100"), otherwise without trailing zeros ("100.5").
func freekassaAmount(amount int64, exponent int) string {
	value := formatMinor(amount, exponent)
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	return value
}

var (
	_ Provider            = (*FreeKassa)(nil)
	_ CurrencySupport     = (*FreeKassa)(nil)
	_ ConnectionProbe     = (*FreeKassa)(nil)
	_ WebhookAcknowledger = (*FreeKassa)(nil)
)
