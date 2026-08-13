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
	TelegramID int64
	Locale     Locale
	Class      string
	Subject    string
	Body       string
}

// PendingCampaignMessages reads the next slice of a running campaign.
//
// Only campaigns in the running state are read, which is what makes pause work:
// pausing stops delivery at the next batch without cancelling what has already
// gone or losing the record of who received it.
func (store *PostgresStore) PendingCampaignMessages(
	ctx context.Context, limit int,
) ([]PendingCampaignMessage, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT c.id::text, r.user_id::text, recipient.telegram_id,
		       CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END,
		       t.class, t.subject_en, t.subject_ru, t.body_en, t.body_ru
		FROM campaign_recipients r
		JOIN campaigns c ON c.id = r.campaign_id AND c.status = 'running'
		JOIN message_templates t ON t.id = c.template_id
		JOIN users u ON u.id = r.user_id AND u.status = 'active'
		JOIN recipient ON recipient.user_id = r.user_id
		LEFT JOIN bot_preferences p ON p.user_id = r.user_id
		WHERE r.status = 'queued'
		ORDER BY r.queued_at
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
