package botapp

import (
	"fmt"
	"html"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/recurring"
)

// leadChoices are the lead times a customer may pick.
//
// They are days rather than a free-text duration because the only question a
// customer is really answering is "how much warning do I want if the card
// fails", and every option here leaves room for the full retry schedule to run
// before access lapses.
var leadChoices = []int{1, 3, 7}

// autoRenewSettingsView is the customer's full recurring-billing screen.
//
// It states what will be charged, from where, and when, because those are the
// three things a person needs to know before agreeing to a charge they will not
// be present for. Nothing here is on by default.
func autoRenewSettingsView(
	locale Locale, settings RenewalSettings, methods []SavedMethod, planName string, supported bool,
) View {
	if !supported {
		return View{
			Text:     text(locale, "renew.unsupported"),
			Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeSettings))),
		}
	}
	if !settings.Enabled {
		rows := make([][]models.InlineKeyboardButton, 0, 3)
		if planName != "" {
			rows = append(rows, row(actionButton(text(locale, "renew.enable"), "autorenew:on")))
		}
		if len(methods) > 0 {
			rows = append(rows, row(callbackButton(text(locale, "renew.methods"), routeMethods)))
		}
		rows = append(rows, row(callbackButton(text(locale, "action.back"), routeSettings)))
		return View{Text: text(locale, "renew.off"), Keyboard: keyboard(rows...)}
	}

	body := text(locale, "renew.on", html.EscapeString(planName), providerLabel(locale, settings.Provider)) +
		"\n\n" + text(locale, "renew.funding", fundingLabel(locale, settings, methods)) +
		"\n" + text(locale, "renew.lead", leadDays(settings.LeadTime))
	if settings.State == recurring.StateDunning {
		// A customer whose card just failed should not have to work out from a
		// silent screen why nothing happened.
		body += "\n\n" + text(locale, "renew.dunning")
	}
	if settings.State == recurring.StateSuspended {
		body += "\n\n" + text(locale, "renew.suspended")
	}

	rows := make([][]models.InlineKeyboardButton, 0, 6)
	rows = append(rows, row(
		actionButton(fundingButton(locale, settings.Funding == recurring.FundingWallet, "renew.fromWallet"), "renew-funding:wallet"),
		actionButton(fundingButton(locale, settings.Funding == recurring.FundingSavedMethod, "renew.fromMethod"), "renew-funding:saved_method"),
	))
	leadRow := make([]models.InlineKeyboardButton, 0, len(leadChoices))
	for _, days := range leadChoices {
		label := text(locale, "renew.leadDays", days)
		if leadDays(settings.LeadTime) == days {
			label = "• " + label
		}
		leadRow = append(leadRow, actionButton(label, fmt.Sprintf("renew-lead:%d", days)))
	}
	rows = append(rows, leadRow)
	if len(methods) > 0 {
		rows = append(rows, row(callbackButton(text(locale, "renew.methods"), routeMethods)))
	}
	rows = append(rows,
		row(actionButton(text(locale, "renew.disable"), "autorenew:off")),
		row(callbackButton(text(locale, "action.back"), routeSettings)),
	)
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// savedMethodsView lists the customer's stored payment methods.
//
// The label is the provider's own masked description. Omniflow holds no card
// data to render even if the screen wanted to show it, and the token that makes
// a charge possible never reaches the message.
func savedMethodsView(locale Locale, methods []SavedMethod) View {
	if len(methods) == 0 {
		return View{
			Text: text(locale, "methods.empty"),
			Keyboard: keyboard(
				row(callbackButton(text(locale, "action.back"), routeAutoRenew)),
			),
		}
	}
	body := text(locale, "methods.title") + "\n"
	rows := make([][]models.InlineKeyboardButton, 0, len(methods)*2+1)
	for _, method := range methods {
		marker := ""
		if method.IsDefault {
			marker = " · " + text(locale, "methods.default")
		}
		status := ""
		if method.Status != "active" {
			status = " · " + text(locale, "methods.status."+method.Status)
		}
		body += fmt.Sprintf("\n<b>%s</b>%s%s\n%s",
			html.EscapeString(methodLabel(locale, method)),
			marker, status, providerLabel(locale, method.Provider))
		actions := make([]models.InlineKeyboardButton, 0, 2)
		if !method.IsDefault && method.Status == "active" {
			actions = append(actions,
				actionButton(text(locale, "methods.makeDefault"), "method-default:"+method.ID))
		}
		actions = append(actions, actionButton(text(locale, "methods.remove"), "method-remove:"+method.ID))
		rows = append(rows, actions)
	}
	body += "\n\n" + text(locale, "methods.notice")
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeAutoRenew)))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// methodLabel falls back to the provider name when the provider supplied no
// masked description, so a method is never rendered as an empty line.
func methodLabel(locale Locale, method SavedMethod) string {
	if method.Label != "" {
		return method.Label
	}
	return providerLabel(locale, method.Provider)
}

// fundingLabel describes what the next renewal will actually be charged to.
func fundingLabel(locale Locale, settings RenewalSettings, methods []SavedMethod) string {
	if settings.Funding != recurring.FundingSavedMethod {
		return text(locale, "renew.fromWallet")
	}
	if settings.MethodLabel != "" {
		return settings.MethodLabel
	}
	for _, method := range methods {
		if method.IsDefault {
			return methodLabel(locale, method)
		}
	}
	return text(locale, "renew.fromMethod")
}

func fundingButton(locale Locale, selected bool, key string) string {
	if selected {
		return "• " + text(locale, key)
	}
	return text(locale, key)
}

// leadDays renders a stored lead time as whole days, rounding up so a value
// just under a day never displays as zero.
func leadDays(lead time.Duration) int {
	if lead <= 0 {
		lead = recurring.DefaultLeadTime
	}
	days := int((lead + 23*time.Hour) / (24 * time.Hour))
	if days < 1 {
		return 1
	}
	return days
}
