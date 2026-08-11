package botapp

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/gifts"
)

// giftHistoryLimit bounds the sender's gift list for the same reason every
// other bot list is bounded: a Telegram message has a length ceiling.
const giftHistoryLimit = 10

// handleGiftRoute serves the gift screens.
func (app *App) handleGiftRoute(ctx context.Context, session commerceContext, route string) (View, bool) {
	switch route {
	case routeGifts:
		return app.giftsScreen(ctx, session), true
	default:
		return View{}, false
	}
}

// handleGiftAction serves the gift callback actions.
func (app *App) handleGiftAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	argument := ""
	if len(parts) > 1 {
		argument = parts[1]
	}
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
		return app.giftAskMessage(ctx, session, argument), true
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

// giftAskMessage collects the note that travels with the gift.
func (app *App) giftAskMessage(ctx context.Context, session commerceContext, token string) View {
	if err := app.customers.BeginSessionState(
		ctx, session.TelegramID, "gift_message", map[string]any{"gift": token},
	); err != nil {
		app.logger.Error("gift message prompt failed", "error", err)
		return app.errorView(session.Locale, routeGifts)
	}
	return giftMessagePromptView(session.Locale)
}

// SubmitGiftMessage completes the gift purchase with the sender's note.
func (app *App) SubmitGiftMessage(
	ctx context.Context, session commerceContext, token, message string,
) View {
	kind, payload, ok := strings.Cut(token, ":")
	if !ok {
		return app.giftsScreen(ctx, session)
	}
	return app.buyGift(ctx, session, kind, payload, message)
}

// giftPurchase buys a gift without a note.
func (app *App) giftPurchase(
	ctx context.Context, session commerceContext, kind, payload string,
) View {
	return app.buyGift(ctx, session, kind, payload, "")
}

// buyGift opens the gift order and shows the claim code exactly once.
//
// The plaintext code exists only in this response. Only its SHA-256 is stored,
// so a database read never yields a redeemable code — and a sender who loses it
// cannot have it recovered, which is the deliberate trade.
func (app *App) buyGift(
	ctx context.Context, session commerceContext, kind, payload, message string,
) View {
	input := commercepg.GiftOrderInput{
		SenderID: session.Customer.ID, Currency: app.settings.Currency,
		SenderMessage: message, Lifetime: gifts.DefaultLifetime,
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
	// The key names the sender, what is being given, and the note. Tapping
	// confirm twice reaches one gift rather than two.
	input.IdempotencyKey = "gift:" + session.Customer.ID + ":" + kind + ":" + payload + ":" +
		strconv.Itoa(len(message))

	purchase, err := app.commerce.BuyGift(ctx, input)
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
	if order.ExternalMinor == 0 {
		// Covered by the wallet, so the gift is already claimable and the code
		// can be shown now.
		return giftCodeView(session.Locale, purchase, order)
	}
	choices := app.commerce.ExternalPaymentChoices(order.Currency)
	if len(choices) == 0 {
		return View{
			Text:     text(session.Locale, "pay.none"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeGifts))),
		}
	}
	if _, err = app.commerce.StartPayment(
		ctx, order, choices[0].Provider, session.TelegramID, text(session.Locale, "gift.paymentLabel"),
	); err != nil {
		app.logger.Error("gift payment intent failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.payment"),
			Keyboard: keyboard(row(actionButton(text(session.Locale, "action.retry"), "order:"+order.ID))),
		}
	}
	// The code is shown with the payment instruction rather than withheld until
	// settlement: the sender needs it to pass on, and it cannot be claimed until
	// the order is paid because the gift is not deliverable before then.
	refreshed, err := app.customers.Order(ctx, session.Customer.ID, order.ID, session.Locale)
	if err != nil {
		refreshed = order
	}
	return giftCodeView(session.Locale, purchase, refreshed)
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
