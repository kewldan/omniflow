package botapp

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// The traffic choice in the chat checkout: the version's own allowance, or a
// volume of the customer's own priced per gigabyte. The volume is checked and
// stored by the shared checkout, so the chat and the web panel price it the
// same way and refuse the same volumes.

// trafficStateGB is the session state of a typed traffic volume.
const trafficStateGB = "traffic_gb"

// checkoutTraffic reads the traffic the open checkout's version sells beside
// its own allowance.
func (app *App) checkoutTraffic(ctx context.Context, checkout CheckoutSession) accountcheckout.TrafficOffer {
	offer, err := app.customers.checkout().PlanTraffic(ctx, checkout.PlanVersionID, checkout.Currency)
	if err != nil {
		app.logger.Warn("traffic offer lookup failed", "error", err)
		return accountcheckout.TrafficOffer{}
	}
	return offer
}

// trafficLabel names a checkout's traffic: the chosen volume, or the
// version's own allowance.
func trafficLabel(locale Locale, offer accountcheckout.TrafficOffer, chosenGB int) string {
	switch {
	case chosenGB > 0:
		return text(locale, "traffic.gb", chosenGB)
	case offer.OwnBytes == nil:
		return text(locale, "traffic.unlimited")
	default:
		return formatBytes(*offer.OwnBytes)
	}
}

// withTrafficChoice adds the chosen traffic and a button to change it to the
// checkout summary, when the version sells a choice.
func withTrafficChoice(view View, locale Locale, offer accountcheckout.TrafficOffer, checkout CheckoutSession) View {
	if !offer.Choosable() {
		return view
	}
	line := text(locale, "checkout.traffic", trafficLabel(locale, offer, checkout.TrafficGB))
	if index := strings.Index(view.Text, "\n\n"); index >= 0 {
		head, tail, _ := strings.Cut(view.Text[index+2:], "\n\n")
		view.Text = view.Text[:index+2] + head + "\n" + line + "\n\n" + tail
	} else {
		view.Text += "\n" + line
	}
	if view.Keyboard != nil {
		button := row(actionButton(text(locale, "traffic.change"), "traffic"))
		rows := view.Keyboard.InlineKeyboard
		at := min(1, len(rows))
		rows = append(rows[:at], append([][]models.InlineKeyboardButton{button}, rows[at:]...)...)
		view.Keyboard.InlineKeyboard = rows
	}
	return view
}

// trafficSteps are the volumes offered as buttons inside a custom range: the
// bounds and a few round values between them.
func trafficSteps(offer accountcheckout.TrafficOffer) []int {
	candidates := []int{offer.MinGB, 50, 100, 200, 500, 1000, offer.MaxGB}
	steps := make([]int, 0, len(candidates))
	for _, value := range candidates {
		if value < offer.MinGB || value > offer.MaxGB || len(steps) > 0 && steps[len(steps)-1] >= value {
			continue
		}
		steps = append(steps, value)
	}
	return steps
}

// trafficScreen lists the traffic the open checkout may be bought with.
func (app *App) trafficScreen(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	offer := app.checkoutTraffic(ctx, checkout)
	if !offer.Choosable() {
		return app.checkoutScreen(ctx, session)
	}
	return trafficView(session.Locale, offer, checkout.TrafficGB)
}

func trafficView(locale Locale, offer accountcheckout.TrafficOffer, chosenGB int) View {
	lines := []string{
		text(locale, "traffic.title"),
		"",
		text(locale, "traffic.current", trafficLabel(locale, offer, chosenGB)),
	}
	rows := [][]models.InlineKeyboardButton{
		row(actionButton(selectedMark(chosenGB == 0, text(locale, "traffic.own", trafficLabel(locale, offer, 0))), "traffic-set:0")),
	}
	// A listed package is a volume at its own price for the plan's period.
	for _, pack := range offer.Packages {
		label := text(locale, "traffic.package", pack.GB, formatMoney(pack.AmountMinor, offer.Currency))
		rows = append(rows, row(actionButton(selectedMark(chosenGB == pack.GB, label), "traffic-set:"+strconv.Itoa(pack.GB))))
	}
	if offer.MinGB > 0 {
		lines = append(lines, text(locale, "traffic.perGb", formatMoney(offer.PricePerGBMinor, offer.Currency), offer.MinGB, offer.MaxGB))
		steps := trafficSteps(offer)
		for start := 0; start < len(steps); start += 3 {
			buttons := make([]models.InlineKeyboardButton, 0, 3)
			for _, value := range steps[start:min(start+3, len(steps))] {
				buttons = append(buttons, actionButton(selectedMark(chosenGB == value, text(locale, "traffic.gb", value)), "traffic-set:"+strconv.Itoa(value)))
			}
			rows = append(rows, buttons)
		}
		rows = append(rows, row(actionButton(text(locale, "traffic.enter"), "traffic-enter")))
	}
	rows = append(rows, row(actionButton(text(locale, "action.back"), "checkout")))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...)}
}

// setTraffic chooses a volume for the open checkout and returns to it.
func (app *App) setTraffic(ctx context.Context, session commerceContext, argument string) View {
	gigabytes, err := strconv.Atoi(argument)
	if err != nil || gigabytes < 0 {
		return app.trafficScreen(ctx, session)
	}
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	if _, err = app.commerce.checkout.ChooseTraffic(ctx, checkout, gigabytes); err != nil {
		if errors.Is(err, commercepg.ErrTrafficUnavailable) || errors.Is(err, commerce.ErrInvalidAmount) {
			view := trafficView(session.Locale, app.checkoutTraffic(ctx, checkout), checkout.TrafficGB)
			view.Text = text(session.Locale, "traffic.refused") + "\n\n" + view.Text
			return view
		}
		app.logger.Error("traffic choice failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return app.checkoutScreen(ctx, session)
}

// beginTrafficEntry asks the customer to type a volume.
func (app *App) beginTrafficEntry(ctx context.Context, session commerceContext) View {
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, trafficStateGB, nil); err != nil {
		app.logger.Error("traffic prompt failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return View{Text: text(session.Locale, "traffic.prompt"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.cancel"), "traffic")))}
}

// completeTrafficEntry applies a typed volume.
func (app *App) completeTrafficEntry(ctx context.Context, session commerceContext, typed string) View {
	if err := app.customers.CancelSession(ctx, session.TelegramID); err != nil {
		app.logger.Warn("session cleanup failed", "error", err)
	}
	digits := strings.TrimSpace(strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(typed)), "GB"))
	gigabytes, err := strconv.Atoi(digits)
	if err != nil || gigabytes <= 0 {
		view := app.trafficScreen(ctx, session)
		view.Text = text(session.Locale, "traffic.refused") + "\n\n" + view.Text
		return view
	}
	return app.setTraffic(ctx, session, strconv.Itoa(gigabytes))
}

// selectedMark marks the current choice in a list of buttons.
func selectedMark(selected bool, label string) string {
	if selected {
		return "• " + label
	}
	return label
}
