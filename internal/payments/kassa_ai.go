package payments

import (
	"context"
	"errors"
	"net/http"

	"github.com/omniflow/omniflow/internal/commerce"
)

// KassaAI settles through KassaAI, a separate merchant product served by the
// same api.fk.life order API as FreeKassa. Unlike FreeKassa it has no hosted
// selection form: every checkout is an API order for one payment system — SBP
// (44) by default, Russian cards (36), or SberPay (43). Notifications use the
// FreeKassa format, signed with secret word 2, and are answered with YES.
type KassaAI struct {
	core *freekassaCore
}

func buildKassaAI(settings Settings) (Provider, error) {
	required := map[string]string{
		"APP_KASSA_AI_SHOP_ID":       settings.String("APP_KASSA_AI_SHOP_ID"),
		"APP_KASSA_AI_API_KEY":       settings.String("APP_KASSA_AI_API_KEY"),
		"APP_KASSA_AI_SECRET_WORD_2": settings.String("APP_KASSA_AI_SECRET_WORD_2"),
		"APP_KASSA_AI_SERVER_IP":     settings.String("APP_KASSA_AI_SERVER_IP"),
	}
	if err := freekassaRequire("KassaAI", required); err != nil {
		return nil, err
	}
	paymentSystemID, err := freekassaPaymentSystem(
		"APP_KASSA_AI_PAYMENT_SYSTEM_ID",
		settings.String("APP_KASSA_AI_PAYMENT_SYSTEM_ID"), 44,
		map[string]int{"sbp": 44, "card": 36, "sberpay": 43},
	)
	if err != nil {
		return nil, err
	}
	core, err := freekassaNewCore(
		"kassa_ai", "KassaAI", required["APP_KASSA_AI_SHOP_ID"],
		required["APP_KASSA_AI_API_KEY"], required["APP_KASSA_AI_SECRET_WORD_2"],
		settings.String("APP_KASSA_AI_CURRENCY"),
	)
	if err != nil {
		return nil, err
	}
	core.paymentSystemID = paymentSystemID
	if core.methodOrder, core.methodIDs, err = parseMethodIDs("APP_KASSA_AI_PAYMENT_METHODS",
		settings.String("APP_KASSA_AI_PAYMENT_METHODS"), map[string]int{"sbp": 44, "card": 36, "sberpay": 43}); err != nil {
		return nil, err
	}
	core.serverIP = required["APP_KASSA_AI_SERVER_IP"]
	// The notification URL travels with each order when the installation has a
	// public origin, as Bedolaga did; otherwise the one set in the merchant
	// account applies.
	core.notificationURL = settings.WebhookURL("kassa_ai")
	return &KassaAI{core: core}, nil
}

func (provider *KassaAI) Name() string { return "kassa_ai" }

func (provider *KassaAI) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Polling: true}
}

// SupportsCurrency reports the single currency this shop is configured to take.
func (provider *KassaAI) SupportsCurrency(currency string) bool {
	return provider.core.supportsCurrency(currency)
}

// Methods lists the payment systems shown as separate buttons.
func (provider *KassaAI) Methods() []string { return provider.core.methodOrder }

func (provider *KassaAI) Create(ctx context.Context, request CreateRequest) (Intent, error) {
	if !provider.core.supportsCurrency(request.Amount.Currency) {
		return Intent{}, commerce.ErrCurrencyMismatch
	}
	if request.Amount.Amount <= 0 {
		return Intent{}, errors.New("KassaAI amount must be positive")
	}
	var extra []freekassaParam
	if request.ReturnURL != "" {
		extra = append(extra,
			freekassaParam{key: "success_url", value: request.ReturnURL},
			freekassaParam{key: "fail_url", value: request.ReturnURL},
		)
	}
	if provider.core.notificationURL != "" {
		extra = append(extra, freekassaParam{key: "notification_url", value: provider.core.notificationURL})
	}
	return provider.core.createOrder(ctx, request, extra)
}

func (provider *KassaAI) Poll(ctx context.Context, reference string) (Intent, error) {
	return provider.core.poll(ctx, reference)
}

func (provider *KassaAI) Refund(context.Context, RefundRequest) (Refund, error) {
	return Refund{}, ErrUnsupported
}

func (provider *KassaAI) VerifyWebhook(_ http.Header, body []byte) (WebhookEvent, error) {
	return provider.core.verifyWebhook(body)
}

// WebhookAck answers with the bare word YES, as for FreeKassa.
func (provider *KassaAI) WebhookAck(string) (int, string, []byte) {
	return http.StatusOK, "text/plain; charset=utf-8", []byte("YES")
}

// Probe reads the shop balance, the API's authenticated read-only call.
func (provider *KassaAI) Probe(ctx context.Context) error {
	return provider.core.probe(ctx)
}

var (
	_ Provider            = (*KassaAI)(nil)
	_ CurrencySupport     = (*KassaAI)(nil)
	_ ConnectionProbe     = (*KassaAI)(nil)
	_ WebhookAcknowledger = (*KassaAI)(nil)
)
