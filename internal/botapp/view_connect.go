package botapp

import (
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// connectPlatformsView asks which platform the customer is setting up.
//
// The platform list is the operator's, read from the same catalogue the web
// panel reads, and each label arrives already resolved to the customer's
// language — an operator who adds a platform cannot add a message key to a
// compiled catalogue, so the label travels as text.
func connectPlatformsView(
	locale Locale, subscription remnawave.Subscription,
	platforms []commerce.ConnectPlatform,
) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(platforms)+2)
	pair := make([]models.InlineKeyboardButton, 0, 2)
	for _, platform := range platforms {
		pair = append(pair, actionButton(platform.Label, "connect:"+platform.Slug))
		if len(pair) == 2 {
			rows = append(rows, row(pair...))
			pair = pair[:0]
		}
	}
	if len(pair) > 0 {
		rows = append(rows, row(pair...))
	}

	body := text(locale, "connect.title")
	switch {
	// An installation whose operator has disabled every platform has no advice
	// to give. Saying so is better than an empty screen, and the raw link
	// remains reachable from the subscription screen either way.
	case len(platforms) == 0:
		body = text(locale, "connect.title") + "\n\n" + text(locale, "connect.noClients")
	case !safeURL(subscription.SubscriptionURL):
		body = text(locale, "connect.title") + "\n\n" + text(locale, "connect.noLink")
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routeConnect), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true, RetryRoute: routeConnect}
}

// connectPlatformView gives step-by-step instructions plus per-app deep links
// and the raw link as a manual fallback.
//
// When the operator has written instructions for the first recommended client
// they replace the generic steps, because an operator who took the trouble to
// describe their own setup knows something the generic copy does not.
func connectPlatformView(
	locale Locale, platform string, subscription remnawave.Subscription,
	platformLabel string, apps []commerce.ClientApp,
	platforms []commerce.ConnectPlatform,
) View {
	if len(apps) == 0 {
		return connectPlatformsView(locale, subscription, platforms)
	}
	body := text(locale, "connect.steps", platformLabel, apps[0].Name)
	if instructions := strings.TrimSpace(apps[0].Instructions); instructions != "" {
		body = instructions
	}

	rows := make([][]models.InlineKeyboardButton, 0, len(apps)*2+3)
	if safeURL(subscription.SubscriptionURL) {
		for _, app := range apps {
			rows = append(rows, row(models.InlineKeyboardButton{
				Text:     text(locale, "connect.deepLink", app.Name),
				CopyText: &models.CopyTextButton{Text: app.DeepLink(subscription.SubscriptionURL)},
			}))
			// A download address is offered as a link rather than as copy text
			// because it is an ordinary https URL, which is one of the three
			// schemes a Telegram inline button accepts.
			if safeURL(app.DownloadURL) {
				rows = append(rows, row(models.InlineKeyboardButton{
					Text: text(locale, "connect.download", app.Name), URL: app.DownloadURL,
				}))
			}
		}
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "connect.copyLink"), CopyText: &models.CopyTextButton{Text: subscription.SubscriptionURL}}))
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "connect.openSubscription"), URL: subscription.SubscriptionURL}))
	} else {
		body = strings.TrimSpace(body) + "\n\n" + text(locale, "connect.noLink")
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeConnect), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true, RetryRoute: routeConnect}
}
