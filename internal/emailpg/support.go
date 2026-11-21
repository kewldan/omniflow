package emailpg

import (
	"context"
	"strconv"
	"time"

	"github.com/omniflow/omniflow/internal/mailer"
)

// KindSupportReply is a support reply mailed because Telegram could not
// deliver it.
const KindSupportReply = "support_reply"

// supportReplyWindow bounds how old an undelivered reply may be and still be
// mailed, so enabling e-mail does not send a customer every answer they ever
// missed.
const supportReplyWindow = 7 * 24 * time.Hour

// supportReplyGrace leaves a reply alone for a few minutes after it was
// written: a customer writing from the web cabinet is often still on the
// page, reads the answer there, and needs no e-mail about it.
const supportReplyGrace = 5 * time.Minute

// prepareSupportReplies mails the operator replies the bot could not push to
// Telegram — the customer has no Telegram identity, blocked the bot, or
// Telegram gave up — and that the customer has not read in the cabinet since.
// It reads the bot's own delivery record, so a reply is mailed only once the
// bot has decided, and never when it reached Telegram. The dedupe key is the
// message, so each reply is mailed at most once.
func (service *Service) prepareSupportReplies(ctx context.Context) error {
	rows, err := service.pool.Query(ctx, `SELECT m.id, t.id::text, t.user_id::text, t.subject, m.body
		FROM support_messages m
		JOIN support_tickets t ON t.id = m.ticket_id
		JOIN users u ON u.id = t.user_id AND u.status = 'active'
		JOIN notification_deliveries n ON n.user_id = t.user_id AND n.kind = 'support'
			AND n.subscription_id IS NULL AND n.dedupe_key = 'support-message:' || m.id::text
			AND (n.status = 'suppressed' OR (n.status = 'failed' AND n.failure_count >= 3))
		WHERE m.sender IN ('operator', 'system') AND m.deleted_at IS NULL
		  AND m.delivered_at IS NULL AND m.read_at IS NULL
		  AND m.created_at > now() - $1::interval
		  AND m.created_at < now() - $2::interval
		  AND EXISTS (SELECT 1 FROM contact_channels c WHERE c.user_id = t.user_id AND c.kind = 'email'
			AND c.revoked_at IS NULL AND c.verified_at IS NOT NULL AND c.transactional_enabled)
		  AND NOT EXISTS (SELECT 1 FROM email_outbox e WHERE e.dedupe_key = 'support-reply:' || m.id::text)
		ORDER BY m.id LIMIT $3`, supportReplyWindow, supportReplyGrace, prepareBatch)
	if err != nil {
		return err
	}
	type reply struct {
		id                        int64
		ticket, customer, subject string
		body                      string
	}
	var batch []reply
	for rows.Next() {
		var item reply
		if err = rows.Scan(&item.id, &item.ticket, &item.customer, &item.subject, &item.body); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range batch {
		if _, err := service.QueueForCustomer(ctx, nil, item.customer, KindSupportReply,
			"support-reply:"+strconv.FormatInt(item.id, 10),
			func(address, locale string) mailer.Message {
				return mailer.SupportReply(address, locale, item.subject, plainText(item.body),
					service.options.PublicURL+"/account/support/"+item.ticket)
			}); err != nil {
			return err
		}
	}
	return nil
}
