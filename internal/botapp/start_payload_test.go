package botapp

import (
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestParseStartPayloadRecognisesEveryDeepLink(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want startPayload
	}{
		{"bare start", "/start", startPayload{}},
		{"menu", "/menu", startPayload{}},
		{"referral", "/start ref_ABCDEFGHIJ", startPayload{Kind: startPayloadReferral, Value: "ABCDEFGHIJ"}},
		{"empty referral", "/start ref_", startPayload{}},
		{"login", "/start login", startPayload{Kind: startPayloadLogin}},
		// Anything else shaped like a campaign code is a campaign candidate;
		// a code no active campaign holds is then ignored by the store.
		{"login is exact", "/start login_now", startPayload{Kind: startPayloadCampaign, Value: "login_now"}},
		{"pay", "/start pay_0c3b1a8e-9d4f-4b2a-8c1e-5f6a7b8c9d0e", startPayload{Kind: startPayloadPay, Value: "0c3b1a8e-9d4f-4b2a-8c1e-5f6a7b8c9d0e"}},
		{"pay normalises case", "/start pay_0C3B1A8E-9D4F-4B2A-8C1E-5F6A7B8C9D0E", startPayload{Kind: startPayloadPay, Value: "0c3b1a8e-9d4f-4b2a-8c1e-5f6a7b8c9d0e"}},
		{"pay needs a uuid", "/start pay_not-an-order", startPayload{}},
		{"pay needs a value", "/start pay_", startPayload{}},
		{"bare campaign code", "/start stars_elite", startPayload{Kind: startPayloadCampaign, Value: "stars_elite"}},
		{"bedolaga invite", "/start refAbCd1234", startPayload{Kind: startPayloadLegacyReferral, Value: "refAbCd1234"}},
		{"bedolaga timestamp invite", "/start ref123456", startPayload{Kind: startPayloadLegacyReferral, Value: "ref123456"}},
		{"too long for a bedolaga invite", "/start refAbCd12345", startPayload{Kind: startPayloadCampaign, Value: "refabcd12345"}},
		{"prefixed campaign code", "/start c_Stars_Elite", startPayload{Kind: startPayloadCampaign, Value: "stars_elite"}},
		{"not a campaign code", "/start c_!!", startPayload{}},
		{"utm", "/start utm_vk__cpc__black_friday", startPayload{Kind: startPayloadTags, Value: "utm_vk__cpc__black_friday"}},
		{"yclid", "/start yclid_1234567890", startPayload{Kind: startPayloadTags, Value: "yclid_1234567890"}},
		{"utm too many parts", "/start utm_a__b__c__d", startPayload{}},
		{"unknown shape", "/start some-thing.else", startPayload{}},
		{"two arguments", "/start ref_A ref_B", startPayload{}},
		{"coupon", "/start coupon_SPRING-2026", startPayload{Kind: startPayloadCoupon, Value: "SPRING-2026"}},
		{"coupon needs a code", "/start coupon_", startPayload{}},
		{"surrounding whitespace", "  /start   login  ", startPayload{Kind: startPayloadLogin}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseStartPayload(tc.text); got != tc.want {
				t.Fatalf("parseStartPayload(%q) = %+v, want %+v", tc.text, got, tc.want)
			}
		})
	}
}

func TestStarsInvoiceDueOnlyForOpenUnsettledStarsOrders(t *testing.T) {
	t.Parallel()
	due := OrderSummary{State: commerce.OrderPending, Currency: "XTR", ExternalMinor: 250}
	if !starsInvoiceDue(due, nil) {
		t.Fatal("an open Stars order with an amount left must get an invoice")
	}
	draft := due
	draft.State = commerce.OrderDraft
	if !starsInvoiceDue(draft, nil) {
		t.Fatal("a draft order is still open")
	}
	for name, mutate := range map[string]func(*OrderSummary){
		"paid":          func(o *OrderSummary) { o.State = commerce.OrderPaid },
		"cancelled":     func(o *OrderSummary) { o.State = commerce.OrderCancelled },
		"expired":       func(o *OrderSummary) { o.State = commerce.OrderExpired },
		"not stars":     func(o *OrderSummary) { o.Currency = "RUB" },
		"wallet funded": func(o *OrderSummary) { o.ExternalMinor = 0 },
		"settled":       func(o *OrderSummary) { o.PaymentStatus = "succeeded" },
	} {
		order := due
		mutate(&order)
		if starsInvoiceDue(order, nil) {
			t.Fatalf("%s: no invoice may be sent", name)
		}
	}
}

func TestParseStartTagsReadsUTMAndClickIDs(t *testing.T) {
	t.Parallel()
	tags, ok := parseStartTags("utm_VK__cpc__black_friday")
	if !ok || tags.Source != "vk" || tags.Medium != "cpc" || tags.Campaign != "black_friday" {
		t.Fatalf("utm = %+v %v", tags, ok)
	}
	if tags, ok := parseStartTags("yclid_abc123def"); !ok || tags.ClickSource != "yandex" || tags.ClickID != "abc123def" {
		t.Fatalf("yclid = %+v %v", tags, ok)
	}
	if _, ok := parseStartTags("gclid_abc"); ok {
		t.Fatal("a click ID shorter than six characters was accepted")
	}
}
