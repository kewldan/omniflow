package botapp

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// withDailyBilling adds how a daily plan is billed to the subscription
// screen, with the customer's pause or resume beside it. A day the wallet did
// not cover offers no button: it resumes on its own after a top-up.
func withDailyBilling(locale Locale, view View, subscriptionID string, status commercepg.DailyStatus) View {
	price := formatMoney(status.PriceMinor, status.Currency)
	var (
		line   string
		button []models.InlineKeyboardButton
	)
	switch {
	case status.Paused:
		line = text(locale, "daily.paused")
		button = row(actionButton(text(locale, "daily.resume"), "sub-resume:"+subscriptionID))
	case status.Suspended:
		line = text(locale, "daily.suspended", price)
		button = row(callbackButton(text(locale, "menu.topup"), routeTopUp))
	default:
		line = text(locale, "daily.running", price)
		button = row(actionButton(text(locale, "daily.pause"), "sub-pause:"+subscriptionID))
	}
	view.Text += "\n" + line
	if view.Keyboard == nil || len(view.Keyboard.InlineKeyboard) == 0 {
		return view
	}
	rows := view.Keyboard.InlineKeyboard
	updated := make([][]models.InlineKeyboardButton, 0, len(rows)+1)
	updated = append(updated, rows[:len(rows)-1]...)
	updated = append(updated, button, rows[len(rows)-1])
	view.Keyboard = keyboard(updated...)
	return view
}

// setDailyPause pauses or resumes a daily subscription for the customer.
// The tap's own minute names the operation, so a double tap queues one.
func (app *App) setDailyPause(ctx context.Context, session commerceContext, subscriptionID string, pause bool) View {
	if _, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale); err != nil {
		return View{Text: text(session.Locale, "subs.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions)))}
	}
	key := "bot:" + subscriptionID + ":" + strconv.FormatInt(time.Now().UTC().Unix()/60, 10)
	err := app.commerce.orders.SetDailyPause(ctx, session.Customer.ID, subscriptionID, pause, key)
	back := keyboard(row(actionButton(text(session.Locale, "subs.back"), "sub:"+subscriptionID)))
	switch {
	case errors.Is(err, commercepg.ErrNotDaily), errors.Is(err, commercepg.ErrNotPausable), errors.Is(err, commercepg.ErrSubscriptionUnknown):
		return View{Text: text(session.Locale, "daily.refused"), Keyboard: back}
	case err != nil:
		app.logger.Error("daily pause failed", "pause", pause, "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	case pause:
		return View{Text: text(session.Locale, "daily.pausedDone"), Keyboard: back}
	default:
		return View{Text: text(session.Locale, "daily.resumedDone"), Keyboard: back}
	}
}
