package botapp

import (
	"context"

	"github.com/omniflow/omniflow/internal/commerce"
)

// PendingCampaignMessage is one campaign message waiting to be delivered.
//
// The body is rendered here rather than stored, because storing it would copy
// customer-facing content into a second retention regime for no operational
// gain: the template and the variables are the record, and the delivery itself
// is what the customer received.
type PendingCampaignMessage struct {
	CampaignID string
	CustomerID string
	// TelegramID is zero when the customer has no Telegram identity the bot
	// can message. The recipient is still read, so it can be resolved.
	TelegramID int64
	// Active is false for a customer suspended or deleted after the audience
	// was expanded.
	Active bool
	// DeliveryStatus is the customer's Telegram delivery health, so a blocked
	// chat is recorded as blocked rather than as a missing identity.
	DeliveryStatus string
	Locale         Locale
	Class          string
	Subject        string
	Body           string
}

// PendingCampaignMessages reads the next slice of the running campaigns.
//
// Only campaigns in the running state are read, which is what makes pause work:
// pausing stops delivery at the next batch without cancelling what has already
// gone or losing the record of who received it.
//
// Every queued recipient is read, reachable or not. The read used to join the
// Telegram recipient and the active-account filter, so a customer with neither
// was never returned and never resolved: their row stayed `queued`, the
// campaign could not complete, and because the batch was one queue ordered by
// `queued_at` across all campaigns, such rows at its head stopped every other
// campaign too. Now they come back with no Telegram identity or as inactive,
// and the notifier records why they were not sent.
//
// The batch is shared fairly. Each running campaign contributes its oldest
// recipients, and the batch takes the first of every campaign, then the
// second, and so on, so a large campaign cannot hold a small one behind it.
func (store *PostgresStore) PendingCampaignMessages(
	ctx context.Context, limit int,
) ([]PendingCampaignMessage, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (pending.position, c.id, pending.user_id)
		       c.id::text, pending.user_id::text, COALESCE(recipient.telegram_id, 0),
		       u.status = 'active', COALESCE(s.status, 'active'),
		       CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END,
		       t.class, t.subject_en, t.subject_ru, t.body_en, t.body_ru
		FROM campaigns c
		CROSS JOIN LATERAL (
			SELECT r.user_id, row_number() OVER (ORDER BY r.queued_at, r.user_id) AS position
			FROM campaign_recipients r
			WHERE r.campaign_id = c.id AND r.status = 'queued'
			ORDER BY r.queued_at, r.user_id
			LIMIT $1
		) pending
		JOIN message_templates t ON t.id = c.template_id
		JOIN users u ON u.id = pending.user_id
		LEFT JOIN recipient ON recipient.user_id = pending.user_id
		LEFT JOIN bot_preferences p ON p.user_id = pending.user_id
		LEFT JOIN bot_delivery_state s ON s.user_id = pending.user_id
		WHERE c.status = 'running'
		ORDER BY pending.position, c.id, pending.user_id, recipient.telegram_id DESC NULLS LAST
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]PendingCampaignMessage, 0, limit)
	for rows.Next() {
		var (
			message   PendingCampaignMessage
			locale    string
			subjectEN string
			subjectRU string
			bodyEN    string
			bodyRU    string
		)
		if err := rows.Scan(&message.CampaignID, &message.CustomerID, &message.TelegramID,
			&message.Active, &message.DeliveryStatus,
			&locale, &message.Class, &subjectEN, &subjectRU, &bodyEN, &bodyRU); err != nil {
			return nil, err
		}
		message.Locale = localeFrom(locale)
		if message.Locale == LocaleRussian {
			message.Subject, message.Body = subjectRU, bodyRU
		} else {
			message.Subject, message.Body = subjectEN, bodyEN
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// ResolveCampaignRecipient records what became of one message.
//
// The `status = 'queued'` guard makes it safe to call twice: a retried delivery
// updates nothing rather than overwriting a recorded outcome, so the counters
// an operator reads cannot drift above the audience.
func (store *PostgresStore) ResolveCampaignRecipient(
	ctx context.Context, campaignID, customerID, status, suppressionReason, errorCode string,
) error {
	_, err := store.pool.Exec(ctx,
		`UPDATE campaign_recipients
		 SET status = $3,
		     suppression_reason = NULLIF($4, ''),
		     error_code = NULLIF($5, ''),
		     resolved_at = now()
		 WHERE campaign_id = $1::uuid AND user_id = $2::uuid AND status = 'queued'`,
		campaignID, customerID, status, suppressionReason, errorCode)
	return err
}

// renderTemplate substitutes the declared variables.
//
// The rule itself lives in `internal/commerce`, because the operator preview
// has to render a campaign exactly as this does. A preview that substituted
// differently would be a preview of a different message.
func renderTemplate(body string, values map[string]string) string {
	return commerce.RenderTemplate(body, values)
}
