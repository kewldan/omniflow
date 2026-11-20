package botapp

import (
	"context"
	"encoding/base64"
	"errors"
	"html"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/gifts"
)

// giftHistoryLimit bounds the sender's gift list for the same reason every
// other bot list is bounded: a Telegram message has a length ceiling.
const giftHistoryLimit = 10

// giftActions is the closed set of callback actions the gift surface owns. It
// is folded into commerceActions, which is what lets a tap reach the handler
// below at all.
var giftActions = map[string]bool{
	"gift-plan": true, "gift-credit": true, "gift-buy": true, "gift-message": true, "gift-claim": true,
	"gift-anyone": true, "gift-nonote": true,
}

// handleGiftAction serves the gift callback actions.
//
// `gift-message` and `gift-buy` both carry the gift as two segments, a kind
// and its payload — `plan:<plan version>` or `credit:<amount>` — so the
// callback has three parts in all. The note prompt used to receive only the
// second one, `plan`, and the note that followed could never be turned back
// into a gift: every attempt landed on the gift list.
func (app *App) handleGiftAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	switch parts[0] {
	case "gift-plan":
		return app.giftPlanScreen(ctx, session), true
	case "gift-credit":
		return app.giftCreditScreen(ctx, session), true
	case "gift-buy":
		if len(parts) != 3 {
			return app.giftsScreen(ctx, session), true
		}
		return app.giftPurchase(ctx, session, parts[1], parts[2]), true
	case "gift-message":
		if len(parts) != 3 {
			return app.giftsScreen(ctx, session), true
		}
		// The flow's first question is who the gift is for; the note follows.
		return app.giftAskRecipient(ctx, session, parts[1]+":"+parts[2]), true
	case "gift-anyone":
		return app.giftWithoutRecipient(ctx, session), true
	case "gift-nonote":
		return app.giftWithoutNote(ctx, session), true
	case "gift-claim":
		return app.giftAskCode(ctx, session), true
	default:
		return View{}, false
	}
}

func (app *App) giftsScreen(ctx context.Context, session commerceContext) View {
	sent, err := app.customers.GiftsSent(ctx, session.Customer.ID, giftHistoryLimit)
	if err != nil {
		app.logger.Error("gift history lookup failed", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	return giftsView(session.Locale, sent)
}

// giftPlanScreen lists the plans that can be given.
//
// It is the ordinary catalog: a gifted subscription is the same product bought
// for somebody else, and offering a separate gift catalog would mean two price
// lists that can disagree.
func (app *App) giftPlanScreen(ctx context.Context, session commerceContext) View {
	plans, err := app.customers.Plans(ctx, session.Locale, app.settings.Currency)
	if err != nil {
		app.logger.Error("plan catalog lookup failed", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	return giftPlansView(session.Locale, plans, app.settings.Currency)
}

// giftCreditScreen offers the wallet-credit amounts an operator configured for
// top-ups, because those are the amounts already known to be sensible in this
// installation's currency.
func (app *App) giftCreditScreen(ctx context.Context, session commerceContext) View {
	limits := app.commerce.TopUpLimits()
	return giftCreditView(session.Locale, limits.Presets, app.settings.Currency)
}

// SubmitGiftMessage completes the gift purchase with the sender's note, under
// the key the flow was opened with, addressed to the recipient the flow named.
func (app *App) SubmitGiftMessage(
	ctx context.Context, session commerceContext, token, key, message string, recipient commercepg.GiftRecipient,
) View {
	kind, payload, ok := strings.Cut(token, ":")
	if !ok {
		return app.giftsScreen(ctx, session)
	}
	if key == "" {
		// A flow opened before keys travelled in the context. Rather than guess
		// a key, refuse the old flow; the customer starts the gift again.
		return app.giftsScreen(ctx, session)
	}
	note, fits := giftNote(message)
	if !fits {
		// The note is refused here rather than by the database, whose check on
		// its length turned a long note into "the order could not be created".
		// The prompt stays open under the same key, so the customer shortens
		// it and sends again rather than starting the gift over.
		flow := map[string]any{"gift": token, "key": key}
		if recipient.Username != "" {
			flow["recipientUsername"] = recipient.Username
		} else if len(recipient.EmailCiphertext) > 0 {
			flow["recipientEmail"] = base64.StdEncoding.EncodeToString(recipient.EmailCiphertext)
			flow["recipientEmailFingerprint"] = base64.StdEncoding.EncodeToString(recipient.EmailFingerprint)
		}
		view := app.giftAskNote(ctx, session, flow)
		view.Text = text(session.Locale, "gift.messageTooLong", giftNoteLimit) + "\n\n" + view.Text
		return view
	}
	return app.buyGift(ctx, session, kind, payload, note, key, recipient)
}

// giftNoteLimit is the longest note a gift carries, in characters. The gift
// record refuses a longer one, so the bot refuses it first and says why.
const giftNoteLimit = 200

// giftNote reads what the sender typed as the note. A lone dash is the prompt's
// documented way to send no note, and is not delivered to the recipient as one.
// It reports false for a note longer than the gift record accepts.
func giftNote(input string) (string, bool) {
	note := strings.TrimSpace(input)
	switch note {
	case "-", "—", "–":
		return "", true
	}
	return note, utf8.RuneCountInString(note) <= giftNoteLimit
}

// giftPurchase buys a gift without a note, under the tap's own key: a
// duplicate of the tap reaches the same gift rather than buying a second one.
func (app *App) giftPurchase(
	ctx context.Context, session commerceContext, kind, payload string,
) View {
	key, err := keyOrNew(session.TapKey)
	if err != nil {
		app.logger.Error("gift key could not be minted", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	return app.buyGift(ctx, session, kind, payload, "", key, commercepg.GiftRecipient{})
}

// buyGift opens the gift order and shows the claim code exactly once.
//
// The plaintext code exists only in this response. Only its SHA-256 is stored,
// so a database read never yields a redeemable code — and a sender who loses it
// cannot have it recovered, which is the deliberate trade.
func (app *App) buyGift(
	ctx context.Context, session commerceContext, kind, payload, message, key string, recipient commercepg.GiftRecipient,
) View {
	input := commercepg.GiftOrderInput{
		SenderID: session.Customer.ID, Currency: app.settings.Currency,
		SenderMessage: message, Lifetime: gifts.DefaultLifetime, Recipient: recipient,
	}
	switch kind {
	case "plan":
		input.Kind, input.PlanVersionID = gifts.KindSubscription, payload
	case "credit":
		amount, err := strconv.ParseInt(payload, 10, 64)
		if err != nil || amount <= 0 {
			return app.giftCreditScreen(ctx, session)
		}
		input.Kind, input.CreditMinor = gifts.KindCredit, amount
	default:
		return app.giftsScreen(ctx, session)
	}
	// The key is the flow's own, minted when the gift was started. A replay of
	// the same flow reaches the same order; a new gift is a new order.
	input.IdempotencyKey = key
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		return channelGateView(session.Locale, gate, "gift-message:"+kind+":"+payload)
	}

	purchase, err := app.commerce.BuyGift(ctx, input)
	if view, refused := giftRecipientRefusal(session.Locale, err); refused {
		return view
	}
	switch {
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case err != nil:
		app.logger.Error("gift order creation failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.order"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeGifts))),
		}
	}

	order, err := app.customers.Order(ctx, session.Customer.ID, purchase.OrderID, session.Locale)
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	// The code is shown now in either case: it exists only in this response.
	// An order the wallet covered is already claimable. One with an amount left
	// offers to pay it, and the payment method is the customer's to pick on the
	// order's own method screen — not the first adapter in alphabetical order,
	// which is what used to be chosen for them.
	view := giftCodeView(session.Locale, purchase, order)
	recipientWaiting := false
	if recipient.Username != "" {
		known, knownErr := app.commerce.orders.TelegramUsernameKnown(ctx, recipient.Username)
		recipientWaiting = knownErr == nil && !known
	}
	switch {
	case recipient.Username != "" && recipientWaiting:
		view.Text += "\n\n" + text(session.Locale, "gift.recipientDeliveryPhantom",
			html.EscapeString("@"+recipient.Username), formatDate(purchase.ExpiresAt))
	case recipient.Username != "":
		view.Text += "\n\n" + text(session.Locale, "gift.recipientDelivery", html.EscapeString("@"+recipient.Username))
	case len(recipient.EmailCiphertext) > 0:
		view.Text += "\n\n" + text(session.Locale, "gift.recipientDeliveryEmail")
	}
	return view
}

// giftAskCode opens the redemption prompt.
func (app *App) giftAskCode(ctx context.Context, session commerceContext) View {
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "gift_claim", nil); err != nil {
		app.logger.Error("gift claim prompt failed", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	return giftClaimPromptView(session.Locale)
}

// SubmitGiftCode redeems a code the recipient typed.
//
// Every refusal renders the same message. A recipient learning the difference
// between "no such code", "already claimed", and "expired" would also be an
// attacker learning which codes exist.
func (app *App) SubmitGiftCode(ctx context.Context, session commerceContext, code string) View {
	if !app.allow(ctx, "bot:gift-claim", session.TelegramID, promoBudget) {
		return View{Text: text(session.Locale, "menu.rateLimit")}
	}
	claimed, err := app.commerce.ClaimGift(ctx, code, session.Customer.ID)
	if err != nil {
		return View{
			Text:     text(session.Locale, "gift.claimRefused"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeGifts))),
		}
	}
	return giftClaimedView(session.Locale, claimed)
}
