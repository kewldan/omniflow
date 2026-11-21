package botapp

import (
	"context"
	"encoding/base64"
	"errors"
	"html"
	"slices"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/gifts"
)

// Addressing a gift and telling its recipient.
//
// After choosing what to give, the sender is asked who it is for: a Telegram
// @username or an e-mail address, or nobody, in which case the code is theirs
// to hand over as before. Once the gift is paid it is delivered: a customer of
// this bot under that username, or with that verified e-mail address, has it
// claimed for them and is told here; an address nobody here holds is mailed a
// claim code by the worker.

// ContactSealer seals an e-mail address the way contact_channels stores one.
// It is customerpg.Service, narrowed; without one the bot addresses gifts to
// @usernames only.
type ContactSealer interface {
	SealContact(kind, value string) (ciphertext, fingerprint []byte, err error)
}

// EnableGiftEmail lets a gift be addressed to an e-mail address. It needs the
// installation's data key, which the bot is not required to hold.
func (app *App) EnableGiftEmail(sealer ContactSealer) {
	app.giftEmail = sealer
}

// giftAskRecipient asks who the gift is for. The flow's idempotency key is
// minted here, at its first step, and travels with it.
func (app *App) giftAskRecipient(ctx context.Context, session commerceContext, token string) View {
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		return channelGateView(session.Locale, gate, "gift-message:"+token)
	}
	key, err := newIdempotencyKey()
	if err != nil {
		app.logError(ctx, "gift key could not be minted", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	if err := app.customers.BeginSessionState(
		ctx, session.TelegramID, "gift_recipient", map[string]any{"gift": token, "key": key},
	); err != nil {
		app.logError(ctx, "gift recipient prompt failed", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	return giftRecipientPromptView(session.Locale, app.giftEmail != nil)
}

func giftRecipientPromptView(locale Locale, email bool) View {
	prompt := "gift.recipientPrompt"
	if !email {
		prompt = "gift.recipientPromptTelegram"
	}
	return View{
		Text: text(locale, prompt),
		Keyboard: keyboard(
			row(actionButton(text(locale, "gift.recipientNone"), "gift-anyone")),
			row(callbackButton(text(locale, "action.cancel"), routeGifts)),
		),
	}
}

// giftWithoutRecipient continues an open gift flow with nobody named: the
// sender hands over the code themselves.
func (app *App) giftWithoutRecipient(ctx context.Context, session commerceContext) View {
	state, flow := openFlowFrom(ctx)
	if state != "gift_recipient" {
		return app.giftsScreen(ctx, session)
	}
	token, _ := flow["gift"].(string)
	key, _ := flow["key"].(string)
	return app.giftAskNote(ctx, session, map[string]any{"gift": token, "key": key})
}

// SubmitGiftRecipient reads the @username or e-mail address the sender typed.
// A value that is neither, an e-mail address on a bot without the data key,
// the sender's own address, and a username nobody here has written from are
// refused with the prompt left open.
func (app *App) SubmitGiftRecipient(ctx context.Context, session commerceContext, token, key, input string) View {
	retry := func(reason string) View {
		if err := app.customers.BeginSessionState(
			ctx, session.TelegramID, "gift_recipient", map[string]any{"gift": token, "key": key},
		); err != nil {
			app.logError(ctx, "gift recipient prompt failed", "error", err)
			return app.errorView(session.Locale, routeGifts)
		}
		view := giftRecipientPromptView(session.Locale, app.giftEmail != nil)
		view.Text = text(session.Locale, reason) + "\n\n" + view.Text
		return view
	}
	username, email, err := commercepg.ParseGiftRecipient(input)
	if err != nil || username == "" && email == "" {
		return retry("gift.recipientInvalid")
	}
	flow := map[string]any{"gift": token, "key": key}
	if username != "" {
		if username == commercepg.NormalizeTelegramUsername(session.Username) {
			return retry("gift.recipientSelf")
		}
		known, err := app.commerce.orders.TelegramUsernameKnown(ctx, username)
		if err != nil {
			app.logError(ctx, "gift recipient lookup failed", "error", err)
			return app.errorView(session.Locale, routeGifts)
		}
		flow["recipientUsername"] = username
		// Somebody who has not started the bot yet still gets the gift: it
		// waits for their first message. The sender is told so now.
		flow["recipientWaiting"] = !known
		return app.giftAskNote(ctx, session, flow)
	}
	if app.giftEmail == nil {
		return retry("gift.recipientNoEmail")
	}
	ciphertext, fingerprint, err := app.giftEmail.SealContact("email", email)
	if err != nil {
		app.logError(ctx, "gift recipient could not be sealed", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	// The address travels sealed: the flow's context holds no readable
	// personal data.
	flow["recipientEmail"] = base64.StdEncoding.EncodeToString(ciphertext)
	flow["recipientEmailFingerprint"] = base64.StdEncoding.EncodeToString(fingerprint)
	return app.giftAskNote(ctx, session, flow)
}

// giftAskNote opens the note prompt with the flow so far.
func (app *App) giftAskNote(ctx context.Context, session commerceContext, flow map[string]any) View {
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "gift_message", flow); err != nil {
		app.logError(ctx, "gift message prompt failed", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	view := giftMessagePromptView(session.Locale, "")
	if username, _ := flow["recipientUsername"].(string); username != "" {
		header := text(session.Locale, "gift.recipientFor", html.EscapeString("@"+username))
		if waiting, _ := flow["recipientWaiting"].(bool); waiting {
			header += "\n" + text(session.Locale, "gift.recipientPhantom", html.EscapeString("@"+username))
		}
		view.Text = header + "\n\n" + view.Text
	} else if _, ok := flow["recipientEmail"].(string); ok {
		view.Text = text(session.Locale, "gift.recipientForEmail") + "\n\n" + view.Text
	}
	return view
}

// giftWithoutNote buys the gift of the open flow with no note.
func (app *App) giftWithoutNote(ctx context.Context, session commerceContext) View {
	state, flow := openFlowFrom(ctx)
	if state != "gift_message" {
		return app.giftsScreen(ctx, session)
	}
	token, _ := flow["gift"].(string)
	key, _ := flow["key"].(string)
	return app.SubmitGiftMessage(ctx, session, token, key, "-", giftRecipientFrom(flow, session.Locale))
}

// giftRecipientFrom reads the recipient a gift flow carries.
func giftRecipientFrom(flow map[string]any, locale Locale) commercepg.GiftRecipient {
	recipient := commercepg.GiftRecipient{Locale: string(locale)}
	if username, _ := flow["recipientUsername"].(string); username != "" {
		recipient.Username = username
		return recipient
	}
	sealed, _ := flow["recipientEmail"].(string)
	fingerprint, _ := flow["recipientEmailFingerprint"].(string)
	if sealed == "" || fingerprint == "" {
		return commercepg.GiftRecipient{}
	}
	var err error
	if recipient.EmailCiphertext, err = base64.StdEncoding.DecodeString(sealed); err != nil {
		return commercepg.GiftRecipient{}
	}
	if recipient.EmailFingerprint, err = base64.StdEncoding.DecodeString(fingerprint); err != nil {
		return commercepg.GiftRecipient{}
	}
	return recipient
}

// giftRecipientRefusal renders a recipient the store refused at purchase.
func giftRecipientRefusal(locale Locale, err error) (View, bool) {
	reason := ""
	switch {
	case errors.Is(err, commercepg.ErrGiftRecipientSelf):
		reason = "gift.recipientSelf"
	case errors.Is(err, commercepg.ErrGiftRecipientInvalid):
		reason = "gift.recipientInvalid"
	default:
		return View{}, false
	}
	return View{
		Text:     text(locale, reason),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeGifts))),
	}, true
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

// giftDeliveryBatch bounds one delivery pass.
const giftDeliveryBatch = 50

// deliverGifts claims every paid gift addressed to a Telegram customer for
// them and tells them. Claiming and recording the delivery are one
// transaction, so a gift is claimed once however many passes see it; the
// notice is keyed on the gift, so it is sent once too.
func (notifier *Notifier) deliverGifts(ctx context.Context) {
	if notifier.commerce == nil {
		return
	}
	orders := notifier.commerce.orders
	waiting, err := orders.WaitingAddressedGifts(ctx, giftDeliveryBatch)
	if err != nil {
		notifier.logger.Error("addressed gift lookup failed", "error", err)
		return
	}
	lookup := notifier.candidateLookup()
	for _, gift := range waiting {
		if ctx.Err() != nil {
			return
		}
		customerID, found, err := orders.GiftRecipientCustomer(ctx, gift)
		if err != nil {
			notifier.logger.Error("gift recipient lookup failed", "error", err)
			continue
		}
		if !found {
			// Nobody here holds the address yet: an e-mail address is mailed
			// by the worker.
			continue
		}
		claimed, err := orders.DeliverAddressedGift(ctx, gift.GiftID, customerID)
		if errors.Is(err, commercepg.ErrGiftNotFound) {
			continue
		}
		if err != nil {
			notifier.logger.Error("addressed gift claim failed", "error", err)
			continue
		}
		candidate, reachable, err := lookup(ctx, customerID)
		if err != nil || !reachable {
			// The gift is theirs either way; the web panel lists it.
			continue
		}
		view := giftDeliveredView(localeFrom(candidate.Locale), gift, ClaimedGift{
			Kind: claimed.Kind, CreditMinor: claimed.CreditMinor.Int64, Currency: claimed.Currency,
		})
		notifier.deliver(ctx, candidate, "", "fulfillment", "gift:"+gift.GiftID, view)
		notifier.tellGiftSender(ctx, lookup, gift)
	}
}

// tellGiftSender lets the sender know their addressed gift reached its
// recipient — the answer to "did they get it?", which matters most for a gift
// that waited for somebody to start the bot.
func (notifier *Notifier) tellGiftSender(ctx context.Context, lookup func(context.Context, string) (notificationCandidate, bool, error), gift commercepg.AddressedGift) {
	candidate, reachable, err := lookup(ctx, gift.SenderID)
	if err != nil || !reachable {
		return
	}
	locale := localeFrom(candidate.Locale)
	recipient := text(locale, "gift.deliveredToEmail")
	if gift.Username != "" {
		recipient = html.EscapeString("@" + gift.Username)
	}
	notifier.deliver(ctx, candidate, "", "fulfillment", "gift-sent:"+gift.GiftID, View{
		Text:     text(locale, "gift.deliveredToSender", recipient),
		Keyboard: keyboard(row(callbackButton(text(locale, "menu.gifts"), routeGifts))),
	})
}

// giftDeliveredView tells a recipient what they were given and that it is
// already theirs.
func giftDeliveredView(locale Locale, gift commercepg.AddressedGift, claimed ClaimedGift) View {
	view := giftClaimedView(locale, claimed)
	what := gift.PlanName
	if gift.Kind != gifts.KindSubscription || what == "" {
		what = formatMoney(gift.CreditMinor, gift.Currency)
	}
	header := text(locale, "gift.receivedTitle", html.EscapeString(what))
	if gift.Message != "" {
		header += "\n\n<i>«" + html.EscapeString(gift.Message) + "»</i>"
	}
	view.Text = header + "\n\n" + view.Text
	return view
}

// openFlowKey carries the flow that was open when a button was tapped. The
// tap discards it before the button is handled, so a button that continues a
// flow reads it from here.
type openFlowKey struct{}

type openFlow struct {
	state string
	data  map[string]any
}

// flowContinuations are the buttons that continue an open flow.
var flowContinuations = []string{actionPrefix + "gift-anyone", actionPrefix + "gift-nonote"}

func (app *App) rememberOpenFlow(ctx context.Context, telegramID int64, data string) context.Context {
	if app.customers == nil || !slices.Contains(flowContinuations, data) {
		return ctx
	}
	state, flow, err := app.customers.SessionState(ctx, telegramID)
	if err != nil || state == "" {
		return ctx
	}
	return context.WithValue(ctx, openFlowKey{}, openFlow{state: state, data: flow})
}

func openFlowFrom(ctx context.Context) (string, map[string]any) {
	flow, _ := ctx.Value(openFlowKey{}).(openFlow)
	return flow.state, flow.data
}
