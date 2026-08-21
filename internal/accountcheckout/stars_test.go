package accountcheckout

import (
	"context"
	"testing"
)

func TestStarsInvoiceLinkOpensTheBotWithThePayPayload(t *testing.T) {
	t.Parallel()
	const order = "2f1c0c2e-0000-4000-8000-000000000000"
	if got := StarsInvoiceLink("omniflow_bot", order); got != "https://t.me/omniflow_bot?start=pay_"+order {
		t.Fatalf("link = %q", got)
	}
	if got := StarsInvoiceLink("@omniflow_bot", order); got != "https://t.me/omniflow_bot?start=pay_"+order {
		t.Fatalf("a leading @ was not stripped: %q", got)
	}
	if got := StarsInvoiceLink("", order); got != "" {
		t.Fatalf("no bot produced a link: %q", got)
	}
	if got := StarsInvoiceLink("omniflow_bot", ""); got != "" {
		t.Fatalf("no order produced a link: %q", got)
	}
}

// A Stars intent carries no URL of its own. With the bot's name known it is a
// Telegram invoice reachable through the bot; without it the handoff is
// honestly "none", never a hosted page.
func TestHandoffBuildsTheStarsLinkFromTheBotName(t *testing.T) {
	t.Parallel()
	const order = "2f1c0c2e-0000-4000-8000-000000000000"
	service := &Service{}
	kind, link := service.Handoff(context.Background(), ProviderTelegramStars, "", order)
	if kind != HandoffNone || link != "" {
		t.Fatalf("without a bot name: %s %q", kind, link)
	}

	service.SetBotUsername(func(context.Context) string { return "omniflow_bot" })
	kind, link = service.Handoff(context.Background(), ProviderTelegramStars, "", order)
	if kind != HandoffTelegram || link != StarsInvoiceLink("omniflow_bot", order) {
		t.Fatalf("with a bot name: %s %q", kind, link)
	}

	// A resolver that cannot reach the bot API yields no name, and no link.
	service.SetBotUsername(func(context.Context) string { return "" })
	if kind, link = service.Handoff(context.Background(), ProviderTelegramStars, "", order); kind != HandoffNone || link != "" {
		t.Fatalf("with an unresolved bot name: %s %q", kind, link)
	}

	// Other adapters are untouched by the resolver.
	service.SetBotUsername(func(context.Context) string { return "omniflow_bot" })
	if kind, link = service.Handoff(context.Background(), "yookassa", "https://pay.test/x", order); kind != HandoffHosted || link != "https://pay.test/x" {
		t.Fatalf("hosted: %s %q", kind, link)
	}
	if kind, _ = service.Handoff(context.Background(), "manual", "", order); kind != HandoffManual {
		t.Fatalf("manual: %s", kind)
	}
}

func TestWithoutStarsKeepsEverythingElseInOrder(t *testing.T) {
	t.Parallel()
	choices := []PaymentChoice{
		{Provider: "cryptobot"}, {Provider: ProviderTelegramStars}, {Provider: "yookassa"},
	}
	kept := WithoutStars(choices)
	if len(kept) != 2 || kept[0].Provider != "cryptobot" || kept[1].Provider != "yookassa" {
		t.Fatalf("WithoutStars = %+v", kept)
	}
	if got := WithoutStars(nil); got == nil || len(got) != 0 {
		t.Fatalf("WithoutStars(nil) = %v, want an empty list", got)
	}
}
