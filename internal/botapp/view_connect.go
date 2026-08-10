package botapp

import (
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// clientApp is one recommended client for a platform, together with the deep
// link that imports a subscription into it.
type clientApp struct {
	name   string
	scheme string
}

// deepLink builds the app's import URL. It is offered as copyable text because
// Telegram inline buttons accept only http, https, and tg links; copying is the
// documented fallback and works on every platform.
func (app clientApp) deepLink(subscriptionURL string) string {
	return app.scheme + subscriptionURL
}

// platformClients maps a platform onto the clients Omniflow documents. Ordering
// is the recommendation order shown to the customer.
var platformClients = map[string][]clientApp{
	"ios":     {{"Happ", "happ://add/"}, {"v2RayTun", "v2raytun://import/"}, {"Streisand", "streisand://import/"}},
	"android": {{"Happ", "happ://add/"}, {"v2RayTun", "v2raytun://import/"}, {"Hiddify", "hiddify://import/"}},
	"windows": {{"Hiddify", "hiddify://import/"}, {"v2RayTun", "v2raytun://import/"}},
	"macos":   {{"Happ", "happ://add/"}, {"Streisand", "streisand://import/"}},
	"linux":   {{"Hiddify", "hiddify://import/"}},
}

var platformOrder = []string{"ios", "android", "windows", "macos", "linux"}

// connectPlatformsView asks which platform the customer is setting up.
func connectPlatformsView(locale Locale, subscription remnawave.Subscription) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(platformOrder)+2)
	pair := make([]models.InlineKeyboardButton, 0, 2)
	for _, platform := range platformOrder {
		pair = append(pair, actionButton(text(locale, "connect.platform."+platform), "connect:"+platform))
		if len(pair) == 2 {
			rows = append(rows, row(pair...))
			pair = pair[:0]
		}
	}
	if len(pair) > 0 {
		rows = append(rows, row(pair...))
	}
	body := text(locale, "connect.title")
	if !safeURL(subscription.SubscriptionURL) {
		body = text(locale, "connect.title") + "\n\n" + text(locale, "connect.noLink")
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routeConnect), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true, RetryRoute: routeConnect}
}

// connectPlatformView gives step-by-step instructions plus per-app deep links
// and the raw link as a manual fallback.
func connectPlatformView(locale Locale, platform string, subscription remnawave.Subscription) View {
	apps, known := platformClients[platform]
	if !known || len(apps) == 0 {
		return connectPlatformsView(locale, subscription)
	}
	body := text(locale, "connect.steps", text(locale, "connect.platform."+platform), apps[0].name)
	rows := make([][]models.InlineKeyboardButton, 0, len(apps)+3)
	if safeURL(subscription.SubscriptionURL) {
		for _, app := range apps {
			rows = append(rows, row(models.InlineKeyboardButton{
				Text:     text(locale, "connect.deepLink", app.name),
				CopyText: &models.CopyTextButton{Text: app.deepLink(subscription.SubscriptionURL)},
			}))
		}
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "connect.copyLink"), CopyText: &models.CopyTextButton{Text: subscription.SubscriptionURL}}))
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "connect.openSubscription"), URL: subscription.SubscriptionURL}))
	} else {
		body = strings.TrimSpace(body) + "\n\n" + text(locale, "connect.noLink")
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeConnect), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true, RetryRoute: routeConnect}
}
