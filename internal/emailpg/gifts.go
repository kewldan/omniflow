package emailpg

import (
	"context"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/gifts"
	"github.com/omniflow/omniflow/internal/mailer"
)

// giftBatch bounds one pass over addressed gifts.
const giftBatch = 50

// prepareGifts mails a paid gift addressed to an e-mail address that no
// Telegram customer holds. (One that a Telegram customer holds is claimed for
// them and announced by the bot instead.) Each gift is mailed a claim code of
// its own, minted here: only its hash is stored, beside the gift, and the
// plaintext exists only in the queued message, sealed like every other. The
// address is erased in the same transaction, so a gift is mailed once and the
// address is kept no longer than it is needed.
func (service *Service) prepareGifts(ctx context.Context) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT r.gift_id::text, r.email_ciphertext, r.locale, g.kind,
			COALESCE((SELECT l.name FROM plan_versions v JOIN plan_localizations l ON l.plan_id = v.plan_id
				WHERE v.id = g.plan_version_id ORDER BY (l.locale = r.locale) DESC LIMIT 1), ''),
			COALESCE(g.credit_minor, 0), g.currency, COALESCE(g.sender_message, '')
		FROM gift_recipients r JOIN gifts g ON g.id = r.gift_id
		WHERE r.status = 'waiting' AND r.email_ciphertext IS NOT NULL
		  AND g.status = 'deliverable' AND g.expires_at > now()
		  AND NOT EXISTS (
			SELECT 1 FROM contact_channels c
			JOIN identities i ON i.user_id = c.user_id AND i.provider = 'telegram' AND i.status = 'active'
			JOIN users u ON u.id = c.user_id AND u.status = 'active'
			WHERE c.kind = 'email' AND c.value_fingerprint = r.email_fingerprint
			  AND c.revoked_at IS NULL AND c.verified_at IS NOT NULL)
		ORDER BY r.created_at LIMIT $1
		FOR UPDATE OF r SKIP LOCKED`, giftBatch)
	if err != nil {
		return err
	}
	type addressed struct {
		giftID, locale, kind, plan, currency, message string
		sealed                                        []byte
		credit                                        int64
	}
	var batch []addressed
	for rows.Next() {
		var item addressed
		if err = rows.Scan(&item.giftID, &item.sealed, &item.locale, &item.kind, &item.plan,
			&item.credit, &item.currency, &item.message); err != nil {
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
		if err = service.mailGift(ctx, tx, item.giftID, item.locale, item.kind, item.plan, item.currency, item.message, item.sealed, item.credit); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (service *Service) mailGift(ctx context.Context, tx pgx.Tx, giftID, locale, kind, plan, currency, message string, sealed []byte, credit int64) error {
	address, err := service.open(sealed, "email")
	if err != nil {
		return err
	}
	code, _, err := gifts.NewCode()
	if err != nil {
		return err
	}
	what := plan
	if kind != gifts.KindSubscription || what == "" {
		what = formatAmount(credit, currency)
	}
	activate := service.options.PublicURL + "/account/wallet?code=" + url.QueryEscape(code)
	if _, err = service.QueueToAddress(ctx, tx, KindGuestGift, "gift-recipient:"+giftID,
		mailer.GuestGift(string(address), locale, what, message, activate)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE gift_recipients SET status = 'delivered', delivered_via = 'email', delivered_at = now(),
			claim_code_hash = $2, email_ciphertext = NULL, email_fingerprint = NULL
		WHERE gift_id = $1::uuid AND status = 'waiting'`, giftID, gifts.Hash(code))
	return err
}
