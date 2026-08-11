package botapp

import (
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// connectPlatformsView asks which platform the customer is setting up.
func connectPlatformsView(locale Locale, subscription remnawave.Subscription) View {
	platforms := commerce.ConnectPlatforms()
	rows := make([][]models.InlineKeyboardButton, 0, len(platforms)+2)
	pair := make([]models.InlineKeyboardButton, 0, 2)
	for _, platform := range platforms {
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
	apps := commerce.ClientsForPlatform(platform)
	if len(apps) == 0 {
		return connectPlatformsView(locale, subscription)
	}
	body := text(locale, "connect.steps", text(locale, "connect.platform."+platform), apps[0].Name)
	rows := make([][]models.InlineKeyboardButton, 0, len(apps)+3)
	if safeURL(subscription.SubscriptionURL) {
		for _, app := range apps {
			rows = append(rows, row(models.InlineKeyboardButton{
				Text:     text(locale, "connect.deepLink", app.Name),
				CopyText: &models.CopyTextButton{Text: app.DeepLink(subscription.SubscriptionURL)},
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
