package payments

import (
	"strings"
	"unicode/utf8"
)

// PurposeTemplates are the operator's wording of the payment purpose a
// provider shows on its page, a receipt, or a bank statement (Bedolaga's
// PAYMENT_SERVICE_NAME and PAYMENT_*_TEMPLATE). Each template may use
// {service}, {description} (the plan name, or what the surface wrote), and
// {order} (the order's short reference). An empty template keeps the
// description as it was.
type PurposeTemplates struct {
	Service string
	Order   string
	TopUp   string
	Renewal string
}

// PurposeTemplatesFromEnv reads APP_PAYMENT_SERVICE_NAME and
// APP_PAYMENT_ORDER_TEMPLATE, APP_PAYMENT_TOPUP_TEMPLATE,
// APP_PAYMENT_RENEWAL_TEMPLATE.
func PurposeTemplatesFromEnv(settings Settings) PurposeTemplates {
	return PurposeTemplates{
		Service: settings.String("APP_PAYMENT_SERVICE_NAME"),
		Order:   settings.String("APP_PAYMENT_ORDER_TEMPLATE"),
		TopUp:   settings.String("APP_PAYMENT_TOPUP_TEMPLATE"),
		Renewal: settings.String("APP_PAYMENT_RENEWAL_TEMPLATE"),
	}
}

// maxPurposeLength bounds the rendered purpose: providers cut descriptions at
// 128 to 255 characters, and a cut in the middle of a word reads worse than
// a short purpose.
const maxPurposeLength = 128

// Render words the purpose of one payment for an order operation.
func (templates PurposeTemplates) Render(operation, description, orderID string) string {
	template := templates.Order
	switch operation {
	case "topup":
		template = templates.TopUp
	case "renewal":
		template = templates.Renewal
	}
	if template == "" {
		return description
	}
	short := orderID
	if len(short) > 8 {
		short = short[:8]
	}
	rendered := strings.NewReplacer(
		"{service}", templates.Service, "{description}", description, "{order}", short,
	).Replace(template)
	rendered = strings.TrimSpace(rendered)
	if utf8.RuneCountInString(rendered) > maxPurposeLength {
		rendered = string([]rune(rendered)[:maxPurposeLength])
	}
	if rendered == "" {
		return description
	}
	return rendered
}
