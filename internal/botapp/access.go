package botapp

import (
	"context"
	"slices"
	"strconv"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// Registration modes of the access section.
const (
	// accessOpen lets anybody who writes to the bot become a customer.
	accessOpen = "open"
	// accessInvite admits a new account only through a link: a referral
	// (ref_…), a campaign (c_…) listed in allowedCampaigns or any campaign
	// when that list is empty, or one of the operator's invite codes (inv_…).
	accessInvite = "invite"
	// accessClosed admits no new accounts at all.
	accessClosed = "closed"
)

// admitted decides whether a /start text may create a new account.
func admitted(access installsettings.Document, startText string) bool {
	switch access.String("mode", accessOpen) {
	case accessClosed:
		return false
	case accessInvite:
		// Invite codes first: a bare start argument also reads as a campaign.
		if fields := strings.Fields(startText); len(fields) == 2 && strings.HasPrefix(fields[1], "inv_") {
			return slices.Contains(access.Strings("inviteCodes", nil), strings.TrimPrefix(fields[1], "inv_"))
		}
		payload := parseStartPayload(startText)
		switch payload.Kind {
		case startPayloadReferral:
			return access.Bool("allowReferrals", true)
		case startPayloadCampaign:
			allowed := access.Strings("allowedCampaigns", nil)
			return len(allowed) == 0 || slices.Contains(allowed, payload.Value)
		}
		return false
	default:
		return true
	}
}

// AccessMiddleware enforces the access section for accounts the bot has never
// seen: existing customers and operators are never affected, and an invited
// newcomer passes on the /start that carries their link. Everybody else is
// told registration is by invitation (or closed) and nothing is created.
func (app *App) AccessMiddleware() telegram.Middleware {
	return func(next telegram.HandlerFunc) telegram.HandlerFunc {
		return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
			access := app.section("access")
			user := senderOf(update)
			if user == nil || app.customers == nil || access.String("mode", accessOpen) == accessOpen || app.isOperator(user.ID) {
				next(ctx, client, update)
				return
			}
			if known, err := app.customers.knownTelegram(ctx, user.ID); err != nil || known {
				next(ctx, client, update)
				return
			}
			startText := ""
			if update.Message != nil {
				startText = update.Message.Text
			}
			if strings.HasPrefix(startText, "/start") && admitted(access, startText) {
				next(ctx, client, update)
				return
			}
			locale := localeFrom(user.LanguageCode)
			key := "access.inviteOnly"
			if access.String("mode", accessOpen) == accessClosed {
				key = "access.closed"
			}
			if update.CallbackQuery != nil {
				_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{
					CallbackQueryID: update.CallbackQuery.ID, Text: text(locale, key), ShowAlert: true,
				})
				return
			}
			_, _ = client.SendMessage(ctx, &telegram.SendMessageParams{ChatID: user.ID, Text: text(locale, key)})
		}
	}
}

// knownTelegram reports whether a Telegram account already has a customer.
func (store *PostgresStore) knownTelegram(ctx context.Context, telegramID int64) (bool, error) {
	var known bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM identities WHERE provider = 'telegram' AND provider_subject = $1 AND status = 'active'
	) OR EXISTS (SELECT 1 FROM remnawave_users WHERE telegram_id = $2)`,
		strconv.FormatInt(telegramID, 10), telegramID).Scan(&known)
	return known, err
}
