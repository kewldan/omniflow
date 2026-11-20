package botapp

import (
	"context"
	"encoding/json"
	"fmt"
	"html"

	"github.com/go-telegram/bot/models"
)

// eventNoticeWindow bounds how long an event notice waits for a customer the
// bot cannot reach yet. A week-old "somebody joined through your link" is no
// longer news.
const eventNoticeWindow = "7 days"

// pendingEventNotice is one event a customer should hear about.
type pendingEventNotice struct {
	ID     string
	UserID string
	Kind   string
	Params eventNoticeParams
}

// eventNoticeParams are the few values a notice's wording needs. They come
// from the trigger that queued it, never from customer input.
type eventNoticeParams struct {
	Role        string `json:"role"`
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
	Days        int    `json:"days"`
	TierCode    string `json:"tierCode"`
	NameEN      string `json:"nameEn"`
	NameRU      string `json:"nameRu"`
	DiscountBPS int    `json:"discountBps"`
	Down        bool   `json:"down"`
}

// eventNoticeKind is the notification kind a notice is delivered under, which
// is what decides its class and the customer's preference that governs it.
func eventNoticeKind(kind string) string {
	switch kind {
	case "loyalty_tier":
		return "loyalty"
	case "renewal_low_balance":
		return "payment"
	}
	return "referral"
}

func eventNoticeDedupe(id string) string { return "event:" + id }

// pendingEventNotices lists recent notices without a final delivery outcome:
// never attempted, deferred past quiet hours, or failed with retries left.
func (store *PostgresStore) pendingEventNotices(ctx context.Context, limit int) ([]pendingEventNotice, error) {
	rows, err := store.pool.Query(ctx, `SELECT n.id::text, n.user_id::text, n.kind, n.params
		FROM customer_event_notices n
		WHERE n.created_at > now() - $2::interval
		  AND NOT EXISTS (
			SELECT 1 FROM notification_deliveries d
			WHERE d.user_id = n.user_id AND d.subscription_id IS NULL
			  AND d.kind = CASE WHEN n.kind = 'loyalty_tier' THEN 'loyalty'
				WHEN n.kind = 'renewal_low_balance' THEN 'payment' ELSE 'referral' END
			  AND d.dedupe_key = 'event:' || n.id::text
			  AND NOT `+reclaimableDelivery+`
		  )
		ORDER BY n.created_at LIMIT $1`, limit, eventNoticeWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := make([]pendingEventNotice, 0, limit)
	for rows.Next() {
		var (
			notice pendingEventNotice
			raw    []byte
		)
		if err := rows.Scan(&notice.ID, &notice.UserID, &notice.Kind, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &notice.Params)
		notices = append(notices, notice)
	}
	return notices, rows.Err()
}

// deliverEventNotices sends one batch of event notices through the ordinary
// delivery path, so preferences, quiet hours, and the outcome are recorded the
// same way as for every other notification.
func (notifier *Notifier) deliverEventNotices(ctx context.Context) {
	notices, err := notifier.store.pendingEventNotices(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("event notice lookup failed", "error", err)
		return
	}
	lookup := notifier.candidateLookup()
	for _, notice := range notices {
		if ctx.Err() != nil {
			return
		}
		kind, dedupe := eventNoticeKind(notice.Kind), eventNoticeDedupe(notice.ID)
		candidate, found, err := lookup(ctx, notice.UserID)
		if err != nil {
			notifier.logger.Warn("event notice recipient lookup failed", "error", err)
			continue
		}
		if !found {
			if err := notifier.store.suppressUnreachable(ctx, notice.UserID, "", kind, dedupe, "unreachable"); err != nil {
				notifier.logger.Warn("event notice suppression failed", "error", err)
			}
			continue
		}
		view, ok := notifier.eventNoticeView(ctx, localeFrom(candidate.Locale), notice)
		if !ok {
			if err := notifier.store.suppressUnreachable(ctx, notice.UserID, "", kind, dedupe, "nothing_to_say"); err != nil {
				notifier.logger.Warn("event notice suppression failed", "error", err)
			}
			continue
		}
		notifier.deliver(ctx, candidate, "", kind, dedupe, view)
	}
}

// eventNoticeView words one notice. It reports false for a notice that has
// nothing to tell, such as a welcome from a programme that rewards the invitee
// with nothing.
func (notifier *Notifier) eventNoticeView(ctx context.Context, locale Locale, notice pendingEventNotice) (View, bool) {
	params := notice.Params
	switch notice.Kind {
	case "referral_joined":
		return View{Text: text(locale, "event.referralJoined"), Keyboard: referralNoticeKeyboard(locale)}, true
	case "referral_welcome":
		program, _, err := notifier.store.ReferralProgram(ctx)
		if err != nil || !program.Enabled {
			return View{}, false
		}
		switch {
		case program.InviteeRewardDays > 0:
			return View{Text: text(locale, "event.referralWelcomeDays", program.InviteeRewardDays)}, true
		case program.InviteeRewardMinor > 0:
			return View{Text: text(locale, "event.referralWelcomeMoney", formatMoney(program.InviteeRewardMinor, program.Currency))}, true
		default:
			return View{}, false
		}
	case "referral_reward":
		reward := formatMoney(params.AmountMinor, params.Currency)
		if params.Days > 0 {
			reward = dayCount(locale, params.Days)
		}
		key := "event.referralRewardInvitee"
		switch params.Role {
		case "inviter":
			key = "event.referralRewardInviter"
		case "commission":
			key = "event.referralCommission"
		}
		return View{Text: text(locale, key, reward), Keyboard: referralNoticeKeyboard(locale)}, true
	case "loyalty_tier":
		name := params.NameEN
		if locale == LocaleRussian {
			name = params.NameRU
		}
		key := "event.loyaltyUp"
		if params.Down {
			key = "event.loyaltyDown"
		}
		message := text(locale, key, html.EscapeString(name))
		if params.DiscountBPS > 0 {
			message += "\n" + text(locale, "event.loyaltyDiscount", percentLabel(params.DiscountBPS))
		}
		return View{Text: message}, true
	case "renewal_low_balance":
		return View{
			Text:     text(locale, "event.renewalLowBalance", dayCount(locale, max(params.Days, 1)), formatMoney(params.AmountMinor, params.Currency)),
			Keyboard: keyboard(row(callbackButton(text(locale, "menu.topup"), routeTopUp))),
		}, true
	default:
		return View{}, false
	}
}

// percentLabel renders basis points as a percentage without trailing zeros.
func percentLabel(bps int) string {
	if bps%100 == 0 {
		return fmt.Sprintf("%d%%", bps/100)
	}
	return fmt.Sprintf("%.2g%%", float64(bps)/100)
}

func referralNoticeKeyboard(locale Locale) *models.InlineKeyboardMarkup {
	return keyboard(row(callbackButton(text(locale, "event.referralOpen"), routeReferral)))
}

// dayCount renders a number of days with the right plural form.
func dayCount(locale Locale, days int) string {
	return plural(locale, days, phrase{ru: "%d день", en: "%d day"}, phrase{ru: "%d дня", en: "%d days"}, phrase{ru: "%d дней", en: "%d days"})
}
