package payments

import (
	"strings"
	"testing"
)

func TestPurposeTemplates(t *testing.T) {
	t.Parallel()
	var none PurposeTemplates
	if got := none.Render("purchase", "Monthly", "6a0f3c1e-2b4d"); got != "Monthly" {
		t.Fatalf("no template changed the purpose: %q", got)
	}
	templates := PurposeTemplatesFromEnv(Settings{Env: func(key string) string {
		return map[string]string{
			"APP_PAYMENT_SERVICE_NAME":   "Acme VPN",
			"APP_PAYMENT_ORDER_TEMPLATE": "{service}: {description} #{order}",
			"APP_PAYMENT_TOPUP_TEMPLATE": "{service} balance top-up",
		}[key]
	}})
	if got := templates.Render("purchase", "Monthly", "6a0f3c1e-2b4d"); got != "Acme VPN: Monthly #6a0f3c1e" {
		t.Fatalf("order purpose = %q", got)
	}
	if got := templates.Render("topup", "Wallet top-up", "x"); got != "Acme VPN balance top-up" {
		t.Fatalf("top-up purpose = %q", got)
	}
	if got := templates.Render("renewal", "Subscription renewal", "x"); got != "Subscription renewal" {
		t.Fatalf("an operation without a template = %q", got)
	}
	long := PurposeTemplates{Order: strings.Repeat("ж", 300)}
	if got := long.Render("purchase", "", ""); len([]rune(got)) != maxPurposeLength {
		t.Fatalf("an overlong purpose is %d characters", len([]rune(got)))
	}
}
