package botapp

import (
	"fmt"
	"html"
	"strconv"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/gifts"
)

// giftsView is the sender's gift screen: what to give, and what they have
// already given.
func giftsView(locale Locale, sent []SentGift) View {
	body := text(locale, "gift.title")
	rows := [][]models.InlineKeyboardButton{
		row(actionButton(text(locale, "gift.givePlan"), "gift-plan")),
		row(actionButton(text(locale, "gift.giveCredit"), "gift-credit")),
		row(actionButton(text(locale, "gift.claim"), "gift-claim")),
	}
	if len(sent) > 0 {
		body += "\n\n" + text(locale, "gift.sentTitle")
		for _, gift := range sent {
			// The hint is the last four characters of the code. It is enough for
			// a sender to tell two of their own gifts apart and not enough for
			// anybody to redeem one.
			body += fmt.Sprintf("\n• <code>%s</code> · %s · %s",
				html.EscapeString(gift.CodeHint),
				text(locale, "gift.kind."+gift.Kind),
				text(locale, "gift.status."+gift.Status))
		}
	}
	rows = append(rows, row(callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// giftPlansView lists the plans that can be given.
func giftPlansView(locale Locale, plans []Plan, currency string) View {
	if len(plans) == 0 {
		return View{
			Text:     text(locale, "plans.empty", currency),
			Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeGifts))),
		}
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(plans)+1)
	for _, plan := range plans {
		rows = append(rows, row(actionButton(
			fmt.Sprintf("%s · %s", plan.Name, formatMoney(plan.AmountMinor, plan.Currency)),
			"gift-message:plan:"+plan.PlanVersionID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeGifts)))
	return View{Text: text(locale, "gift.choosePlan"), Keyboard: keyboard(rows...)}
}

// giftCreditView offers the wallet-credit amounts the operator already
// configured for top-ups, because those are the amounts known to be sensible in
// this installation's currency.
func giftCreditView(locale Locale, presets []int64, currency string) View {
	if len(presets) == 0 {
		return View{
			Text:     text(locale, "gift.noCreditPresets"),
			Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeGifts))),
		}
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(presets)+1)
	for _, amount := range presets {
		rows = append(rows, row(actionButton(
			formatMoney(amount, currency),
			"gift-message:credit:"+strconv.FormatInt(amount, 10))))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeGifts)))
	return View{Text: text(locale, "gift.chooseCredit"), Keyboard: keyboard(rows...)}
}

// giftMessagePromptView asks for the note that travels with the gift.
func giftMessagePromptView(locale Locale) View {
	return View{
		Text:     text(locale, "gift.messagePrompt"),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.cancel"), routeGifts))),
	}
}

// giftCodeView shows the claim code — the one and only time it exists outside
// the sender's own message.
func giftCodeView(locale Locale, purchase GiftPurchase, order OrderSummary) View {
	body := text(locale, "gift.created") +
		"\n\n<code>" + html.EscapeString(purchase.Code) + "</code>\n\n" +
		text(locale, "gift.codeNotice") +
		"\n" + text(locale, "gift.expires", formatDate(purchase.ExpiresAt))
	rows := make([][]models.InlineKeyboardButton, 0, 3)
	if order.ExternalMinor > 0 {
		// Unpaid: the code exists but the gift is not deliverable, and saying so
		// is better than letting a recipient try and be refused.
		body += "\n\n" + text(locale, "gift.awaitingPayment")
		rows = append(rows, row(actionButton(text(locale, "menu.orders"), "order:"+order.ID)))
	}
	rows = append(rows,
		row(callbackButton(text(locale, "action.back"), routeGifts)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true}
}

// giftClaimPromptView asks the recipient for a code.
func giftClaimPromptView(locale Locale) View {
	return View{
		Text:     text(locale, "gift.claimPrompt"),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.cancel"), routeGifts))),
	}
}

// giftClaimedView tells the recipient what they were given.
func giftClaimedView(locale Locale, claimed ClaimedGift) View {
	body := text(locale, "gift.claimed")
	switch claimed.Kind {
	case gifts.KindCredit:
		body += "\n\n" + text(locale, "gift.claimedCredit", formatMoney(claimed.CreditMinor, claimed.Currency))
		return View{Text: body, Keyboard: keyboard(
			row(callbackButton(text(locale, "menu.wallet"), routeWallet)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		)}
	default:
		// Provisioning is the worker's job, so the honest word is "activating".
		body += "\n\n" + text(locale, "gift.claimedSubscription")
		return View{Text: body, Keyboard: keyboard(
			row(callbackButton(text(locale, "menu.subs"), routeSubscriptions)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		)}
	}
}
