package botapp

import (
	"net/url"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// connectOptions are the operator's link settings and where the customer
// panel lives, which together decide how a link reaches the customer.
type connectOptions struct {
	Settings  commerce.ConnectSettings
	PublicURL string
}

// redirectLink wraps an application link in the customer panel's /open page.
//
// A Telegram inline button accepts only http, https, and tg addresses, so a
// `happ://` link can only be offered as text to copy. The /open page is an
// https address that forwards the browser to the application. The link travels
// in the fragment, which a browser never sends to a server, so the panel's
// access log does not learn the subscription link.
func (options connectOptions) redirectLink(deepLink string) string {
	base := strings.TrimRight(strings.TrimSpace(options.PublicURL), "/")
	if deepLink == "" || !strings.HasPrefix(strings.ToLower(base), "https://") {
		return ""
	}
	return base + "/open#" + url.QueryEscape(deepLink)
}

// miniAppLink is the customer panel's address for a Telegram Mini App button,
// or empty when the panel has no https address to open.
func (options connectOptions) miniAppLink(path string) string {
	base := strings.TrimRight(strings.TrimSpace(options.PublicURL), "/")
	if !strings.HasPrefix(strings.ToLower(base), "https://") {
		return ""
	}
	return base + path
}

// importButton offers one application link: a one-tap button through the
// /open page when the panel has an https address, and text to copy otherwise.
func (options connectOptions) importButton(label, deepLink string) models.InlineKeyboardButton {
	if redirect := options.redirectLink(deepLink); redirect != "" {
		return models.InlineKeyboardButton{Text: label, URL: redirect}
	}
	return models.InlineKeyboardButton{Text: label, CopyText: &models.CopyTextButton{Text: deepLink}}
}

// rawLinkRows are the bare link and its mirrors, unless the operator hides
// the link from customers altogether.
func (options connectOptions) rawLinkRows(locale Locale, subscriptionURL string) [][]models.InlineKeyboardButton {
	if options.Settings.HideRawLink || !safeURL(subscriptionURL) {
		return nil
	}
	rows := [][]models.InlineKeyboardButton{
		row(models.InlineKeyboardButton{Text: text(locale, "connect.copyLink"), CopyText: &models.CopyTextButton{Text: subscriptionURL}}),
		row(models.InlineKeyboardButton{Text: text(locale, "connect.openSubscription"), URL: subscriptionURL}),
	}
	for _, mirror := range options.Settings.MirrorLinks(subscriptionURL) {
		rows = append(rows, row(models.InlineKeyboardButton{
			Text:     text(locale, "connect.mirror", mirror.Label),
			CopyText: &models.CopyTextButton{Text: mirror.URL},
		}))
	}
	return rows
}

// connectModeView is the Connect screen in the operator's chosen mode. The
// guide mode is the platform picker and is rendered by connectPlatformsView;
// every other mode offers its one-tap path first and the guide second.
func connectModeView(
	locale Locale, subscription remnawave.Subscription, options connectOptions,
) (View, bool) {
	link := subscription.SubscriptionURL
	if !safeURL(link) {
		return View{}, false
	}
	guide := row(actionButton(text(locale, "connect.allApps"), "connect:*"))
	back := row(callbackButton(text(locale, "action.back"), routeHome))
	var rows [][]models.InlineKeyboardButton
	switch options.Settings.ButtonMode {
	case commerce.ConnectModeMiniApp:
		address := options.miniAppLink("/account/devices")
		if address == "" {
			return View{}, false
		}
		rows = append(rows, row(models.InlineKeyboardButton{
			Text: text(locale, "connect.miniApp"), WebApp: &models.WebAppInfo{URL: address},
		}))
	case commerce.ConnectModeLink:
		if options.Settings.HideRawLink {
			return View{}, false
		}
		rows = append(rows, options.rawLinkRows(locale, link)...)
	case commerce.ConnectModeHapp:
		rows = append(rows, row(options.importButton(text(locale, "connect.deepLink", "Happ"), options.Settings.HappLink(link))))
		rows = append(rows, options.rawLinkRows(locale, link)...)
	default:
		return View{}, false
	}
	rows = append(rows, guide, back)
	return View{
		Text: text(locale, "connect.title"), Keyboard: keyboard(rows...),
		RetryRoute: routeConnect,
	}, true
}

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
	return View{Text: body, Keyboard: keyboard(rows...), RetryRoute: routeConnect}
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
	platforms []commerce.ConnectPlatform, options connectOptions,
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
			deepLink := options.Settings.DeepLink(app, subscription.SubscriptionURL)
			rows = append(rows, row(options.importButton(text(locale, "connect.deepLink", app.Name), deepLink)))
			// A download address is offered as a link rather than as copy text
			// because it is an ordinary https URL, which is one of the three
			// schemes a Telegram inline button accepts.
			if safeURL(app.DownloadURL) {
				rows = append(rows, row(models.InlineKeyboardButton{
					Text: text(locale, "connect.download", app.Name), URL: app.DownloadURL,
				}))
			}
		}
		rows = append(rows, options.rawLinkRows(locale, subscription.SubscriptionURL)...)
	} else {
		body = strings.TrimSpace(body) + "\n\n" + text(locale, "connect.noLink")
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeConnect), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), RetryRoute: routeConnect}
}
