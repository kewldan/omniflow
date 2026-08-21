package botapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

func TestCatalogEveryMessageHasBothLanguages(t *testing.T) {
	t.Parallel()
	for key, message := range catalog {
		if strings.TrimSpace(message.ru) == "" || strings.TrimSpace(message.en) == "" {
			t.Fatalf("message %q is missing a Russian or English translation", key)
		}
		if strings.Count(message.ru, "%") != strings.Count(message.en, "%") {
			t.Fatalf("message %q has mismatched format placeholders", key)
		}
	}
}

func TestPlansViewComparesPeriodTrafficDevicesAndPrice(t *testing.T) {
	t.Parallel()
	traffic := int64(100 * 1024 * 1024 * 1024)
	devices := int32(3)
	view := plansView(LocaleEnglish, []Plan{{
		PlanVersionID: "11111111-1111-1111-1111-111111111111",
		Name:          "Pro <plan>", Kind: "one_time", Duration: 30 * 24 * time.Hour,
		TrafficAllowanceBytes: &traffic, DeviceLimit: &devices,
		Currency: "RUB", AmountMinor: 49900,
	}}, "RUB")
	for _, fragment := range []string{"1 month", "100.0 GiB", "3", "499 RUB", "Pro &lt;plan&gt;"} {
		if !strings.Contains(view.Text, fragment) {
			t.Fatalf("plan comparison is missing %q: %s", fragment, view.Text)
		}
	}
	if strings.Contains(view.Text, "Pro <plan>") {
		t.Fatalf("plan name was not escaped: %s", view.Text)
	}
}

func TestPlansViewEmptyStateNamesTheCurrency(t *testing.T) {
	t.Parallel()
	view := plansView(LocaleRussian, nil, "USD")
	if !strings.Contains(view.Text, "USD") {
		t.Fatalf("empty catalog must name the currency: %s", view.Text)
	}
}

func TestPlanActionsFollowThePlanPolicy(t *testing.T) {
	t.Parallel()
	plan := Plan{PlanVersionID: "a", UpgradePolicy: "forbid", DowngradePolicy: "forbid"}
	active := Entitlement{Found: true, PlanVersionID: "b", Status: "active", EndsAt: time.Now().Add(time.Hour)}
	if actions := planActions(LocaleEnglish, plan, active, ""); len(actions) != 0 {
		t.Fatalf("a plan forbidding both changes must offer nothing: %+v", actions)
	}
	plan.UpgradePolicy = "extend"
	actions := planActions(LocaleEnglish, plan, active, "")
	if len(actions) != 1 || actions[0].operation != "upgrade" {
		t.Fatalf("only the permitted change must be offered: %+v", actions)
	}
	same := Entitlement{Found: true, PlanVersionID: "a", Status: "active", EndsAt: time.Now().Add(time.Hour)}
	if actions = planActions(LocaleEnglish, plan, same, ""); len(actions) != 1 || actions[0].operation != "extension" {
		t.Fatalf("the current plan must offer an extension: %+v", actions)
	}
	trial := Plan{Kind: "trial"}
	if actions = planActions(LocaleEnglish, trial, Entitlement{}, "trial_already_used"); len(actions) != 0 {
		t.Fatalf("an ineligible trial must not be purchasable: %+v", actions)
	}
}

func TestCheckoutViewShowsTheFullBreakdown(t *testing.T) {
	t.Parallel()
	session := CheckoutSession{PlanVersionID: "a", Operation: "purchase", Provider: "yookassa", ApplyWallet: true}
	quote := commerce.CheckoutQuote{
		Subtotal:      commerce.Money{Amount: 49900, Currency: "RUB"},
		DiscountMinor: 5000, WalletBalanceMinor: 10000, WalletAppliedMinor: 10000, ExternalMinor: 34900,
		PromoCode: "WELCOME",
	}
	view := checkoutView(LocaleEnglish, Plan{Name: "Pro", Duration: 30 * 24 * time.Hour}, session, quote, false)
	screen := view.Text + " | " + buttonLabels(view)
	for _, fragment := range []string{"499 RUB", "50 RUB", "100 RUB", "349 RUB", "YooKassa", "WELCOME", "Use wallet"} {
		if !strings.Contains(screen, fragment) {
			t.Fatalf("checkout summary is missing %q: %s", fragment, screen)
		}
	}
	if !view.Protect {
		t.Fatal("the checkout summary must be protected content")
	}
}

func TestCheckoutViewExplainsARejectedPromoCode(t *testing.T) {
	t.Parallel()
	quote := commerce.CheckoutQuote{Subtotal: commerce.Money{Amount: 1000, Currency: "RUB"}, ExternalMinor: 1000, PromoRejection: "promo_exhausted"}
	view := checkoutView(LocaleEnglish, Plan{Name: "Pro", Duration: 24 * time.Hour}, CheckoutSession{}, quote, false)
	if !strings.Contains(view.Text, "redemption limit") {
		t.Fatalf("a refused promo code must explain itself: %s", view.Text)
	}
}

func TestOrderStatusViewCoversEveryPhase(t *testing.T) {
	t.Parallel()
	base := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", Currency: "RUB", ExternalMinor: 34900, RefundedMinor: 34900, PlanName: "Pro"}
	for phase, fragment := range map[commerce.PaymentPhase]string{
		commerce.PaymentPhaseAwaitingAction: "Waiting for payment",
		commerce.PaymentPhaseSucceeded:      "Payment received",
		commerce.PaymentPhaseProvisioning:   "Activating your subscription",
		commerce.PaymentPhaseCompleted:      "All set",
		commerce.PaymentPhaseFailed:         "did not go through",
		commerce.PaymentPhaseCancelled:      "Order cancelled",
		commerce.PaymentPhaseExpired:        "Payment window closed",
		commerce.PaymentPhaseRefunded:       "Refunded",
	} {
		order := base
		order.Phase = phase
		view := orderStatusView(LocaleEnglish, order, nil)
		if !strings.Contains(view.Text, fragment) {
			t.Fatalf("phase %q is missing %q: %s", phase, fragment, view.Text)
		}
		if len(view.Keyboard.InlineKeyboard) == 0 {
			t.Fatalf("phase %q left the customer without an action", phase)
		}
	}
}

func TestOrderStatusViewMentionsDelayedConfirmation(t *testing.T) {
	t.Parallel()
	order := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", Currency: "RUB", ExternalMinor: 100, Phase: commerce.PaymentPhasePending}
	view := orderStatusView(LocaleEnglish, order, nil)
	if !strings.Contains(view.Text, "can take a few minutes") {
		t.Fatalf("a pending payment must explain delayed confirmation: %s", view.Text)
	}
}

func TestConnectViewOffersDeepLinksAndAManualFallback(t *testing.T) {
	t.Parallel()
	subscription := remnawave.Subscription{Found: true, SubscriptionURL: "https://sub.example/secret"}
	view := connectPlatformView(
		LocaleEnglish, "ios", subscription, "iOS",
		[]commerce.ClientApp{
			{Name: "Happ", Scheme: "happ://add/", DownloadURL: "https://apps.example.test/happ"},
			{Name: "Streisand", Scheme: "streisand://import/"},
		},
		[]commerce.ConnectPlatform{{Slug: "ios", Label: "iOS"}},
	)
	copyButtons := 0
	for _, buttonRow := range view.Keyboard.InlineKeyboard {
		for _, button := range buttonRow {
			if button.CopyText != nil {
				copyButtons++
			}
			if button.URL != "" && !strings.HasPrefix(button.URL, "http") {
				t.Fatalf("Telegram only accepts http(s) button links: %q", button.URL)
			}
		}
	}
	if copyButtons < 2 {
		t.Fatalf("expected app deep links plus a manual-copy fallback, got %d copy actions", copyButtons)
	}
	if !view.Protect {
		t.Fatal("connection instructions carry the subscription link and must be protected")
	}
}

func TestConnectViewWithoutASubscriptionExplainsItself(t *testing.T) {
	t.Parallel()
	view := connectPlatformView(
		LocaleRussian, "android", remnawave.Subscription{}, "Android",
		[]commerce.ClientApp{{Name: "Happ", Scheme: "happ://add/"}},
		[]commerce.ConnectPlatform{{Slug: "android", Label: "Android"}},
	)
	if !strings.Contains(view.Text, "появится после активации") {
		t.Fatalf("a customer without access must be told why: %s", view.Text)
	}
	for _, buttonRow := range view.Keyboard.InlineKeyboard {
		for _, button := range buttonRow {
			if button.CopyText != nil {
				t.Fatal("no link may be offered before access exists")
			}
		}
	}
}

func TestPaymentMethodViewOnlyOffersConfiguredAdapters(t *testing.T) {
	t.Parallel()
	view := paymentMethodView(LocaleEnglish, Plan{PlanVersionID: "a"}, nil, "")
	if !strings.Contains(view.Text, "No payment method is available") {
		t.Fatalf("an installation without providers must say so: %s", view.Text)
	}
	view = paymentMethodView(LocaleEnglish, Plan{PlanVersionID: "a"}, []PaymentChoice{{Provider: "telegram_stars", Currency: "XTR", AmountMinor: 250}}, "")
	if !strings.Contains(buttonLabels(view), "Telegram Stars") || !strings.Contains(buttonLabels(view), "⭐ 250") {
		t.Fatalf("the offered method must show its price: %s", buttonLabels(view))
	}
}

func TestWalletViewRendersBalanceAndHistory(t *testing.T) {
	t.Parallel()
	view := walletView(LocaleEnglish, 25000, "RUB", []WalletEntry{{Type: "referral_reward", AmountMinor: 10000, Currency: "RUB", OccurredAt: time.Now()}}, true)
	if !strings.Contains(view.Text, "250 RUB") || !strings.Contains(view.Text, "referral reward") || !strings.Contains(view.Text, "+100 RUB") {
		t.Fatalf("wallet view is incomplete: %s", view.Text)
	}
	empty := walletView(LocaleRussian, 0, "RUB", nil, false)
	if !strings.Contains(empty.Text, "Операций пока нет") {
		t.Fatalf("empty wallet state is missing: %s", empty.Text)
	}
}

func TestSupportViewsCoverEmptyUnreadAndClosedStates(t *testing.T) {
	t.Parallel()
	if !strings.Contains(supportListView(LocaleEnglish, nil, "").Text, "No requests yet") {
		t.Fatal("the empty support state is missing")
	}
	tickets := []Ticket{{ID: "aaaaaaaa-0000-0000-0000-000000000000", Subject: "Cannot connect", Status: "open", UnreadCount: 2, LastMessageAt: time.Now()}}
	if !strings.Contains(supportListView(LocaleEnglish, tickets, "").Text, "2 new") {
		t.Fatal("unread replies must be visible in the ticket list")
	}
	closed := supportTicketView(LocaleEnglish, Ticket{ID: "a", Status: "closed"}, nil)
	if !strings.Contains(buttonLabels(closed), "Reopen") {
		t.Fatalf("a closed ticket must offer reopening: %s", buttonLabels(closed))
	}
	open := supportTicketView(LocaleEnglish, Ticket{ID: "a", Status: "open"}, []TicketMessage{{Sender: "operator", Body: "<b>hi</b>", CreatedAt: time.Now(), Attachments: []Attachment{{Kind: "document", FileName: "log.txt", SizeBytes: 2048}}}})
	if strings.Contains(open.Text, "<b>hi</b>") {
		t.Fatalf("operator content must be escaped: %s", open.Text)
	}
	if !strings.Contains(open.Text, "log.txt") || !strings.Contains(open.Text, "2.0 KiB") {
		t.Fatalf("attachments must be described: %s", open.Text)
	}
}

func TestNewsViewsMarkUnreadAndHandleAnEmptyInbox(t *testing.T) {
	t.Parallel()
	if !strings.Contains(newsListView(LocaleEnglish, nil).Text, "Nothing new") {
		t.Fatal("the empty news state is missing")
	}
	view := newsListView(LocaleEnglish, []NewsItem{{ID: "a", Title: "Maintenance", Category: "maintenance", PublishedAt: time.Now()}})
	if !strings.Contains(view.Text, "🔵") || !strings.Contains(view.Text, "Maintenance") {
		t.Fatalf("unread posts must be marked: %s", view.Text)
	}
}

func TestAttachmentValidationEnforcesSizeAndType(t *testing.T) {
	t.Parallel()
	if err := (Attachment{Kind: "photo", SizeBytes: 1024}).Validate(); err != nil {
		t.Fatalf("a small photo must be accepted: %v", err)
	}
	if err := (Attachment{Kind: "photo", SizeBytes: maxAttachmentBytes + 1}).Validate(); !errors.Is(err, ErrAttachmentTooBig) {
		t.Fatalf("oversized attachment error = %v, want ErrAttachmentTooBig", err)
	}
	if err := (Attachment{Kind: "video", SizeBytes: 10}).Validate(); !errors.Is(err, ErrAttachmentKind) {
		t.Fatalf("unsupported kind error = %v, want ErrAttachmentKind", err)
	}
	if _, err := collectAttachments(&models.Message{Video: &models.Video{FileID: "x"}}); !errors.Is(err, ErrAttachmentKind) {
		t.Fatalf("video attachment error = %v, want ErrAttachmentKind", err)
	}
	attachments, err := collectAttachments(&models.Message{Document: &models.Document{FileID: "file", FileName: "log.txt", FileSize: 100}})
	if err != nil || len(attachments) != 1 || attachments[0].TelegramFileID != "file" {
		t.Fatalf("document attachment = (%+v, %v)", attachments, err)
	}
}

func TestClassifyDeliveryStopsRetryingBlockedAccounts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	blocked := ClassifyDelivery(now, errors.New("Forbidden: bot was blocked by the user"))
	if blocked.Code != "bot_blocked" || retryableDelivery(blocked.Code) {
		t.Fatalf("blocked classification = %+v, want a permanent stop", blocked)
	}
	deactivated := ClassifyDelivery(now, errors.New("Forbidden: user is deactivated"))
	if deactivated.Code != "user_deactivated" || retryableDelivery(deactivated.Code) {
		t.Fatalf("deactivated classification = %+v, want a permanent stop", deactivated)
	}
	flood := ClassifyDelivery(now, &telegram.TooManyRequestsError{Message: "Too Many Requests", RetryAfter: 12})
	if flood.Code != "flood_wait" || !flood.RetryAfter.Equal(now.Add(12*time.Second)) {
		t.Fatalf("flood classification = %+v, want a 12 second wait", flood)
	}
	if !retryableDelivery(flood.Code) {
		t.Fatal("a flood wait must remain retryable")
	}
}

func TestFormatMoneyRendersMinorUnitsAndStars(t *testing.T) {
	t.Parallel()
	if got := formatMoney(49900, "RUB"); got != "499 RUB" {
		t.Fatalf("formatMoney(49900, RUB) = %q", got)
	}
	if got := formatMoney(49950, "RUB"); got != "499.50 RUB" {
		t.Fatalf("formatMoney(49950, RUB) = %q", got)
	}
	if got := formatMoney(250, "XTR"); got != "⭐ 250" {
		t.Fatalf("formatMoney(250, XTR) = %q", got)
	}
}

func TestFormatDurationUsesRussianPluralForms(t *testing.T) {
	t.Parallel()
	for days, want := range map[int]string{1: "1 день", 2: "2 дня", 5: "5 дней", 11: "11 дней", 21: "21 день"} {
		if got := formatDuration(LocaleRussian, time.Duration(days)*24*time.Hour); got != want {
			t.Fatalf("formatDuration(%d days) = %q, want %q", days, got, want)
		}
	}
	if got := formatDuration(LocaleEnglish, 365*24*time.Hour); got != "1 year" {
		t.Fatalf("formatDuration(365 days) = %q", got)
	}
}

func TestPreferredCurrencyFavoursTheInstallationDefault(t *testing.T) {
	t.Parallel()
	option := commerce.PaymentOption{Provider: "cryptobot", Enabled: true, Currencies: []string{"USD", "RUB"}}
	if got := preferredCurrency(option, []string{"RUB", "USD"}, "USD"); got != "USD" {
		t.Fatalf("preferredCurrency = %q, want USD", got)
	}
	if got := preferredCurrency(option, []string{"RUB"}, "USD"); got != "RUB" {
		t.Fatalf("preferredCurrency fallback = %q, want RUB", got)
	}
	stars := commerce.PaymentOption{Provider: "telegram_stars", Enabled: true, Currencies: []string{"XTR"}}
	if got := preferredCurrency(stars, []string{"RUB"}, "RUB"); got != "" {
		t.Fatalf("an incompatible adapter must be skipped, got %q", got)
	}
	if got := preferredCurrency(commerce.PaymentOption{Provider: "manual"}, []string{"RUB"}, "RUB"); got != "" {
		t.Fatalf("a disabled adapter must be skipped, got %q", got)
	}
}

func TestSettingsViewExposesEveryCommunicationControl(t *testing.T) {
	t.Parallel()
	preferences := CustomerPreferences{
		Preferences: Preferences{Locale: "ru", ExpiryNotifications: true},
		QuietHours:  commerce.QuietHours{Configured: true, StartHour: 22, EndHour: 8},
	}
	view := customerSettingsView(LocaleEnglish, preferences, 3)
	labels := buttonLabels(view)
	for _, fragment := range []string{"Subscription expiry", "Traffic limit", "Renewal reminders", "Service news", "Marketing messages", "22:00–08:00"} {
		if !strings.Contains(labels, fragment) {
			t.Fatalf("settings is missing %q: %s", fragment, labels)
		}
	}
	if !strings.Contains(view.Text, "3 per week") {
		t.Fatalf("the marketing frequency cap must be disclosed: %s", view.Text)
	}
}

func buttonLabels(view View) string {
	labels := make([]string, 0, 8)
	if view.Keyboard == nil {
		return ""
	}
	for _, buttonRow := range view.Keyboard.InlineKeyboard {
		for _, button := range buttonRow {
			labels = append(labels, button.Text)
		}
	}
	return strings.Join(labels, " | ")
}
