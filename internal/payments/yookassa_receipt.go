package payments

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// YooKassaReceipt configures the 54-FZ receipt YooKassa issues for each
// payment. A shop connected to "Чеки от ЮKassa" or to an online cash register
// refuses a payment that carries no receipt, so for such a shop this is not
// optional: every payment and every saved-method charge must name a customer
// contact and describe what was sold.
type YooKassaReceipt struct {
	Enabled bool
	// DefaultEmail receives the receipt when the payment names no customer
	// e-mail. Omniflow never passes a customer's sealed e-mail to a provider,
	// so in practice this is the address every receipt goes to.
	DefaultEmail   string
	VATCode        int
	PaymentSubject string
	PaymentMode    string
}

// YooKassaReceiptFromEnv reads APP_YOOKASSA_RECEIPT* settings. Receipts are on
// when APP_YOOKASSA_RECEIPTS is true or a default receipt e-mail is set, and
// then a contact is required: enabling receipts with no default e-mail is a
// startup error, because every checkout would fail at YooKassa otherwise.
func YooKassaReceiptFromEnv(settings Settings) (YooKassaReceipt, error) {
	receipt := YooKassaReceipt{
		DefaultEmail:   settings.String("APP_YOOKASSA_RECEIPT_EMAIL"),
		VATCode:        settings.Int("APP_YOOKASSA_VAT_CODE", 1),
		PaymentSubject: settings.String("APP_YOOKASSA_PAYMENT_SUBJECT"),
		PaymentMode:    settings.String("APP_YOOKASSA_PAYMENT_MODE"),
	}
	receipt.Enabled = settings.Bool("APP_YOOKASSA_RECEIPTS", receipt.DefaultEmail != "")
	if receipt.PaymentSubject == "" {
		receipt.PaymentSubject = "service"
	}
	if receipt.PaymentMode == "" {
		receipt.PaymentMode = "full_payment"
	}
	if !receipt.Enabled {
		return receipt, nil
	}
	if receipt.DefaultEmail == "" || !strings.Contains(receipt.DefaultEmail, "@") {
		return YooKassaReceipt{}, errors.New("APP_YOOKASSA_RECEIPTS needs APP_YOOKASSA_RECEIPT_EMAIL: YooKassa refuses a receipt without a customer contact")
	}
	if receipt.VATCode < 1 || receipt.VATCode > 12 {
		return YooKassaReceipt{}, errors.New("APP_YOOKASSA_VAT_CODE must be a YooKassa vat_code from 1 to 12")
	}
	return receipt, nil
}

// WithReceipt makes every payment and saved-method charge carry a receipt.
func (provider *YooKassa) WithReceipt(receipt YooKassaReceipt) *YooKassa {
	provider.receipt = receipt
	return provider
}

// receiptBody builds the `receipt` object for one payment of value (already
// formatted in major units) and currency, or nil when receipts are off.
func (provider *YooKassa) receiptBody(description, value, currency string, metadata map[string]string) map[string]any {
	if !provider.receipt.Enabled {
		return nil
	}
	contact := provider.receipt.DefaultEmail
	if email := strings.TrimSpace(metadata["customer_email"]); strings.Contains(email, "@") {
		contact = email
	}
	if description == "" {
		description = "Оплата заказа"
	}
	return map[string]any{
		"customer": map[string]string{"email": contact},
		"items": []map[string]any{{
			"description":     yookassaTruncate(description, 128),
			"quantity":        "1.00",
			"amount":          yooMoney{Value: value, Currency: currency},
			"vat_code":        provider.receipt.VATCode,
			"payment_mode":    provider.receipt.PaymentMode,
			"payment_subject": provider.receipt.PaymentSubject,
		}},
	}
}

// yookassaTruncate cuts to at most limit characters (not bytes), as YooKassa
// counts them.
func yookassaTruncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
