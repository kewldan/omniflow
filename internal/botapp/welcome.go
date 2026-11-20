package botapp

import (
	"context"
	"html"
	"math/rand/v2"
	"strconv"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// referralCodeAction opens the prompt for an invite code typed by hand.
const referralCodeAction = "ref-code"

// welcomeActions are the start-screen buttons this file owns.
var welcomeActions = map[string]bool{referralCodeAction: true}

// marketing is the Marketing settings section.
func (app *App) marketing() installsettings.Document {
	return app.section("marketing")
}

// welcomeOnce reports whether this is the customer's first /start, marking it
// so the welcome is shown once.
func (store *PostgresStore) welcomeOnce(ctx context.Context, telegramID int64) (bool, error) {
	result, err := store.pool.Exec(ctx, `UPDATE identities
		SET metadata = metadata || '{"welcomed": true}'::jsonb
		WHERE provider = 'telegram' AND provider_subject = $1 AND status = 'active'
		  AND NOT (metadata ? 'welcomed')`, strconv.FormatInt(telegramID, 10))
	return err == nil && result.RowsAffected() > 0, err
}

// sendWelcome greets a newcomer the way the operator wrote it, with the logo or
// video above when one is configured, and offers to type an invite code when
// that is switched on. A returning customer gets only the start media.
func (app *App) sendWelcome(ctx context.Context, client *telegram.Bot, chatID int64, from *models.User, locale Locale) {
	if app.customers == nil || from == nil {
		return
	}
	settings := app.marketing()
	first, err := app.customers.welcomeOnce(ctx, from.ID)
	if err != nil {
		app.logger.Warn("welcome state could not be kept", "error", err)
	}
	body := ""
	if first {
		template := settings.String("welcomeTextEn", "")
		if locale == LocaleRussian {
			template = settings.String("welcomeTextRu", "")
		}
		body = renderWelcome(template, from.FirstName, app.section("branding").String("serviceName", ""))
	}
	var markup models.ReplyMarkup
	if first && settings.Bool("referralCodePrompt", false) && app.commerceEnabled() {
		markup = keyboard(row(actionButton(text(locale, "welcome.referralCode"), referralCodeAction)))
		if body == "" {
			body = text(locale, "welcome.referralHint")
		}
	}
	photo, video := settings.String("startPhotoUrl", ""), settings.String("startVideoUrl", "")
	switch {
	case safeURL(video) && strings.HasPrefix(video, "https://"):
		params := &telegram.SendVideoParams{ChatID: chatID, Video: &models.InputFileString{Data: video}, ReplyMarkup: markup}
		if body != "" {
			params.Caption, params.ParseMode = body, models.ParseModeHTML
		}
		_, err = client.SendVideo(ctx, params)
	case safeURL(photo) && strings.HasPrefix(photo, "https://"):
		params := &telegram.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: photo}, ReplyMarkup: markup}
		if body != "" {
			params.Caption, params.ParseMode = body, models.ParseModeHTML
		}
		_, err = client.SendPhoto(ctx, params)
	case body != "":
		params := sendParams(chatID, View{Text: body})
		params.ReplyMarkup = markup
		_, err = client.SendMessage(ctx, params)
	}
	if err != nil {
		app.logger.Warn("welcome could not be sent", "error", err)
	}
}

// renderWelcome fills the welcome text's placeholders. The operator's text is
// HTML, saved through the same settings screen as every other wording; the
// customer's name is escaped because it is the customer's own text.
func renderWelcome(template, firstName, service string) string {
	template = strings.TrimSpace(template)
	if template == "" {
		return ""
	}
	name := strings.TrimSpace(firstName)
	return strings.NewReplacer(
		"{name}", html.EscapeString(name), "{first_name}", html.EscapeString(name),
		"{service}", html.EscapeString(service),
	).Replace(template)
}

// randomTip picks one of the operator's tips for the main menu, one per line.
func (app *App) randomTip(locale Locale) string {
	key := "tipsEn"
	if locale == LocaleRussian {
		key = "tipsRu"
	}
	tips := make([]string, 0, 8)
	for _, line := range strings.Split(app.marketing().String(key, ""), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			tips = append(tips, line)
		}
	}
	if len(tips) == 0 {
		return ""
	}
	return tips[rand.IntN(len(tips))]
}

// withTip appends a random tip to the main menu.
func (app *App) withTip(view View, locale Locale) View {
	if tip := app.randomTip(locale); tip != "" {
		view.Text += "\n\n💡 " + html.EscapeString(tip)
	}
	return view
}

// handleWelcomeAction opens the invite-code prompt.
func (app *App) handleWelcomeAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if parts[0] != referralCodeAction {
		return View{}, false
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "referral_code", nil); err != nil {
		app.logger.Error("invite code prompt failed", "error", err)
		return app.errorView(session.Locale, routeHome), true
	}
	return View{
		Text:     text(session.Locale, "welcome.referralPrompt"),
		Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.cancel"), routeHome))),
	}, true
}

// completeReferralCode attributes the customer to the invite code they typed.
func (app *App) completeReferralCode(ctx context.Context, session commerceContext, code string) View {
	if err := app.customers.CancelSession(ctx, session.TelegramID); err != nil {
		app.logger.Warn("session cleanup failed", "error", err)
	}
	if !app.allow(ctx, "bot:gift-claim", session.TelegramID, promoBudget) {
		return View{Text: text(session.Locale, "menu.rateLimit")}
	}
	if err := app.customers.AttributeReferralForCustomer(ctx, session.Customer.ID, code); err != nil {
		app.logger.Warn("typed referral attribution failed", "error", err)
	}
	attributed, err := app.customers.HasReferralAttribution(ctx, session.Customer.ID)
	back := keyboard(row(callbackButton(text(session.Locale, "action.menu"), routeHome)))
	if err != nil || !attributed {
		return View{Text: text(session.Locale, "welcome.referralRefused"), Keyboard: back}
	}
	return View{Text: text(session.Locale, "welcome.referralAccepted"), Keyboard: back}
}

// HasReferralAttribution reports whether somebody invited the customer.
func (store *PostgresStore) HasReferralAttribution(ctx context.Context, customerID string) (bool, error) {
	var attributed bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM referral_attributions WHERE referred_user_id = $1::uuid)`,
		customerID).Scan(&attributed)
	return attributed, err
}
