package botapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/referralplus"
)

// partnerActions is the closed set of callback actions the partner programme
// owns. It is folded into commerceActions, which is what lets a tap reach the
// handler below at all.
var partnerActions = map[string]bool{"partner": true, "partner-apply": true}

func (app *App) handlePartnerAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if app.partners == nil {
		return View{}, false
	}
	switch parts[0] {
	case "partner":
		return app.partnerScreen(ctx, session, ""), true
	case "partner-apply":
		_, err := app.partners.ApplyPartner(ctx, session.Customer.ID, referralplus.SourceBot, "", "")
		switch {
		case err == nil:
			return app.partnerScreen(ctx, session, text(session.Locale, "partner.sent")), true
		case errors.Is(err, referralplus.ErrPartnerPending), errors.Is(err, referralplus.ErrAlreadyPartner):
			return app.partnerScreen(ctx, session, ""), true
		default:
			app.logError(ctx, "partner application failed", "error", err)
			return app.errorView(session.Locale, routeReferral), true
		}
	default:
		return View{}, false
	}
}

// partnerScreen shows the customer's partner standing: their rate when they
// are a partner, the open application when there is one, and the button to
// apply otherwise. Anything the customer wants to tell the operator — where
// they will promote — is written in the web panel; the bot applies in one tap.
func (app *App) partnerScreen(ctx context.Context, session commerceContext, notice string) View {
	state, err := app.partners.PartnerStatus(ctx, session.Customer.ID)
	if err != nil {
		app.logError(ctx, "partner status failed", "error", err)
		return app.errorView(session.Locale, routeReferral)
	}
	locale := session.Locale
	body := text(locale, "partner.intro")
	switch {
	case state.Partner:
		body = text(locale, "partner.active", bpsPercent(state.CommissionBPS), bpsPercent(state.FirstPaymentCommissionBPS))
	case state.Latest != nil && state.Latest.Status == "pending":
		body = text(locale, "partner.pending", formatDate(state.Latest.CreatedAt))
	case state.Latest != nil && state.Latest.Status == "rejected":
		body = text(locale, "partner.rejected") + "\n\n" + text(locale, "partner.intro")
	}
	if notice != "" {
		body = notice + "\n\n" + body
	}
	rows := [][]models.InlineKeyboardButton{}
	if state.CanApply {
		rows = append(rows, row(actionButton(text(locale, "partner.apply"), "partner-apply")))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeReferral)))
	return View{Text: text(locale, "partner.title") + "\n\n" + body, Keyboard: keyboard(rows...), RetryRoute: routeReferral}
}

// bpsPercent renders a rate in basis points as a percentage, or a dash when
// no rate is set.
func bpsPercent(bps *int32) string {
	if bps == nil {
		return "—"
	}
	if *bps%100 == 0 {
		return fmt.Sprintf("%d%%", *bps/100)
	}
	return fmt.Sprintf("%.2f%%", float64(*bps)/100)
}

// withReferralExtras adds the partner programme and, when any is switched
// on, the daily games to the invitation screen, above its back button.
func (app *App) withReferralExtras(view View, locale Locale, games bool) View {
	if view.Keyboard == nil || len(view.Keyboard.InlineKeyboard) == 0 {
		return view
	}
	extra := [][]models.InlineKeyboardButton{}
	if app.partners != nil {
		extra = append(extra, row(actionButton(text(locale, "partner.open"), "partner")))
	}
	if games {
		extra = append(extra, row(actionButton(text(locale, "games.open"), "games")))
	}
	if len(extra) == 0 {
		return view
	}
	rows := view.Keyboard.InlineKeyboard
	last := len(rows) - 1
	combined := make([][]models.InlineKeyboardButton, 0, len(rows)+len(extra))
	combined = append(combined, rows[:last]...)
	combined = append(combined, extra...)
	combined = append(combined, rows[last])
	return View{Text: view.Text, Keyboard: keyboard(combined...), Protect: view.Protect, RetryRoute: view.RetryRoute, Photo: view.Photo,
		Preview: view.Preview}
}
