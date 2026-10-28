package botapp

import (
	"fmt"
	"time"

	"github.com/go-telegram/bot/models"
)

// quietWindows are the presets offered for quiet hours. An explicit list keeps
// callback data closed and avoids a free-text time parser.
var quietWindows = [][2]int{{22, 8}, {23, 9}, {0, 0}}

// customerSettingsView renders language, per-kind notification switches,
// marketing consent, and quiet hours in one screen.
func customerSettingsView(locale Locale, preferences CustomerPreferences, marketingCap int, now time.Time) View {
	body := text(locale, "settings.title")
	if marketingCap > 0 {
		body += text(locale, "settings.marketingNote", marketingCap)
	}
	// The window is only meaningful with the zone it is counted in, so the
	// zone is named beside it rather than on a screen of its own.
	quiet := text(locale, "settings.quietOff")
	if preferences.QuietHours.Configured {
		quiet = text(locale, "settings.quietOn", preferences.QuietHours.StartHour, preferences.QuietHours.EndHour) +
			" · " + timezoneLabel(locale, preferences.Timezone, now)
	}
	rows := [][]models.InlineKeyboardButton{
		row(
			actionButton(languageMark(preferences.Locale, "auto")+" "+autoLabel(locale), "lang:auto"),
			actionButton(languageMark(preferences.Locale, "ru")+" RU", "lang:ru"),
			actionButton(languageMark(preferences.Locale, "en")+" EN", "lang:en"),
		),
		row(actionButton(text(locale, "settings.expiry", toggleLabel(preferences.ExpiryNotifications)), "notify:expiry")),
		row(actionButton(text(locale, "settings.traffic", toggleLabel(preferences.TrafficNotifications)), "notify:traffic")),
		row(actionButton(text(locale, "settings.renewal", toggleLabel(preferences.RenewalNotifications)), "notify:renewal")),
		row(actionButton(text(locale, "settings.news", toggleLabel(preferences.NewsNotifications)), "notify:news")),
		row(actionButton(text(locale, "settings.marketing", toggleLabel(preferences.MarketingEnabled)), "notify:marketing")),
		row(actionButton(text(locale, "settings.quiet", quiet), "quiet-menu")),
		row(actionButton(text(locale, "settings.timezone", timezoneLabel(locale, preferences.Timezone, now)), "tz-menu")),
		row(callbackButton(text(locale, "menu.renew"), routeAutoRenew)),
		row(callbackButton(text(locale, "action.back"), routeHome)),
	}
	return View{Text: body, Keyboard: keyboard(rows...), RetryRoute: routeSettings}
}

// quietHoursView offers the quiet-hour presets, including turning them off,
// and says which time zone they are counted in. A customer still on the UTC
// default is told so in as many words: "22:00–08:00" in UTC is the middle of
// the evening in Vladivostok.
func quietHoursView(locale Locale, preferences CustomerPreferences, now time.Time) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(quietWindows)+2)
	for _, window := range quietWindows {
		label := text(locale, "settings.quietOn", window[0], window[1])
		if window[0] == window[1] {
			label = text(locale, "settings.quietOff")
		}
		mark := "○"
		if preferences.QuietHours.Configured && preferences.QuietHours.StartHour == window[0] && preferences.QuietHours.EndHour == window[1] {
			mark = "●"
		} else if !preferences.QuietHours.Configured && window[0] == window[1] {
			mark = "●"
		}
		rows = append(rows, row(actionButton(mark+" "+label, fmt.Sprintf("quiet:%d-%d", window[0], window[1]))))
	}
	rows = append(rows,
		row(actionButton(text(locale, "settings.timezone", timezoneLabel(locale, preferences.Timezone, now)), "tz-menu")),
		row(callbackButton(text(locale, "action.back"), routeSettings)))
	body := text(locale, "settings.quietTitle") + "\n\n" + text(locale, "settings.quietZone", timezoneLabel(locale, preferences.Timezone, now))
	if preferences.Timezone == "" || preferences.Timezone == "UTC" {
		body += "\n" + text(locale, "settings.quietUTC")
	}
	return View{Text: body, Keyboard: keyboard(rows...)}
}

func autoLabel(locale Locale) string {
	if locale == LocaleRussian {
		return "Авто"
	}
	return "Auto"
}

// commerceMainKeyboard extends the v0.2 menu with the purchase, order, wallet,
// and news entries, showing unread badges where they exist.
func commerceMainKeyboard(locale Locale, menu MenuState) *models.InlineKeyboardMarkup {
	if !menu.CommerceEnabled {
		return mainKeyboard(locale)
	}
	supportLabel := text(locale, "menu.support")
	if menu.UnreadSupport > 0 {
		supportLabel += text(locale, "menu.badge", menu.UnreadSupport)
	}
	newsLabel := text(locale, "menu.news")
	if menu.UnreadNews > 0 {
		newsLabel += text(locale, "menu.badge", menu.UnreadNews)
	}
	subscription, connect, devices, referral, settings := "📊 Subscription", "🚀 Connect", "📱 Devices", "🎁 Invite", "⚙️ Settings"
	if locale == LocaleRussian {
		subscription, connect, devices, referral, settings = "📊 Подписка", "🚀 Подключиться", "📱 Устройства", "🎁 Пригласить", "⚙️ Настройки"
	}
	// With concurrent subscriptions enabled the first row opens the switcher
	// instead of the single subscription screen. A single-subscription
	// installation keeps exactly the menu it had before.
	subscriptionRoute := routeSubscription
	if menu.MultiSubscription {
		subscription, subscriptionRoute = text(locale, "menu.subs"), routeSubscriptions
	}
	rows := make([][]models.InlineKeyboardButton, 0, 10)
	if menu.RecoverSubscriptionID != "" {
		// The lifecycle notice above the keyboard says access can be restored in
		// one tap. This is the tap: a renewal of the lapsed subscription, which
		// keeps its plan and settings rather than opening a new one.
		rows = append(rows, row(actionButton(text(locale, "life.recover"), "sub-renew:"+menu.RecoverSubscriptionID)))
	}
	rows = append(rows,
		row(callbackButton(text(locale, "menu.plans"), routePlans), callbackButton(subscription, subscriptionRoute)),
		row(callbackButton(connect, routeConnect), callbackButton(devices, routeDevices)),
		row(callbackButton(text(locale, "menu.orders"), routeOrders), callbackButton(text(locale, "menu.wallet"), routeWallet)),
	)
	// The wallet and cart rows only appear when they can actually be used.
	walletRow := make([]models.InlineKeyboardButton, 0, 2)
	if menu.TopUpEnabled {
		walletRow = append(walletRow, callbackButton(text(locale, "menu.topup"), routeTopUp))
	}
	if menu.HasCart {
		walletRow = append(walletRow, callbackButton(text(locale, "menu.cart"), routeCart))
	}
	if len(walletRow) > 0 {
		rows = append(rows, walletRow)
	}
	// The shop appears only when an operator has published something to sell,
	// so an installation that sells no digital goods keeps exactly the menu it
	// had before.
	shopRow := make([]models.InlineKeyboardButton, 0, 2)
	if menu.ShopEnabled {
		shopRow = append(shopRow, callbackButton(text(locale, "menu.shop"), routeShop))
	}
	shopRow = append(shopRow, callbackButton(text(locale, "menu.gifts"), routeGifts))
	rows = append(rows, shopRow)
	if menu.OfferCount > 0 {
		rows = append(rows, row(callbackButton(text(locale, "menu.offers"), routeOffers)))
	}
	rows = append(rows,
		row(callbackButton(supportLabel, routeSupport), callbackButton(newsLabel, routeNews)),
		row(callbackButton(referral, routeReferral), callbackButton(settings, routeSettings)),
		row(callbackButton(text(locale, "action.refresh"), routeHome)),
	)
	return keyboard(rows...)
}
