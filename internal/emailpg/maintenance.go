package emailpg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/notice"
)

// KindMaintenance is a customer notice that maintenance started or ended.
const KindMaintenance = "maintenance"

// prepareMaintenance queues the latest maintenance notice for the customers
// who have a verified address that accepts transactional mail, when the
// maintenance settings asked for e-mail at the time of the transition. Like
// the bot, it only ever sends the latest notice, within a day of it; each
// message's dedupe key is the notice and the customer, so the pass can run as
// often as it likes. The wording is the operator's notice override, the same
// text the bot sends, without its markup.
func (service *Service) prepareMaintenance(ctx context.Context) error {
	var (
		broadcastID, transition, noticeRU, noticeEN string
		expected                                    pgtype.Timestamptz
		email                                       bool
	)
	err := service.pool.QueryRow(ctx, `SELECT id::text, transition, notice_ru, notice_en, expected_return_at, email
		FROM maintenance_broadcasts
		WHERE created_at > now() - interval '24 hours'
		ORDER BY created_at DESC LIMIT 1`).Scan(&broadcastID, &transition, &noticeRU, &noticeEN, &expected, &email)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !email {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := service.pool.Query(ctx, `SELECT u.id::text FROM users u
		WHERE u.status = 'active'
		  AND EXISTS (SELECT 1 FROM contact_channels c WHERE c.user_id = u.id AND c.kind = 'email'
			AND c.revoked_at IS NULL AND c.verified_at IS NOT NULL AND c.transactional_enabled)
		  AND NOT EXISTS (SELECT 1 FROM email_outbox e
			WHERE e.dedupe_key = 'maintenance:' || $1 || ':' || u.id::text)
		ORDER BY u.id LIMIT $2`, broadcastID, prepareBatch)
	if err != nil {
		return err
	}
	customers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(customers) == 0 {
		return err
	}
	overrides, err := service.noticeOverrides(ctx)
	if err != nil {
		return err
	}
	for _, customer := range customers {
		if _, err := service.QueueForCustomer(ctx, nil, customer, KindMaintenance,
			"maintenance:"+broadcastID+":"+customer,
			func(address, locale string) mailer.Message {
				return maintenanceMessage(address, locale, transition, noticeRU, noticeEN, expected, overrides)
			}); err != nil {
			return err
		}
	}
	return nil
}

// noticeOverrides reads the operator's notice wording, keyed code/locale.
func (service *Service) noticeOverrides(ctx context.Context) (map[string]string, error) {
	rows, err := service.pool.Query(ctx, `SELECT code || '/' || locale, body FROM notice_overrides
		WHERE code IN ('maintenance_started', 'maintenance_ended')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overrides := map[string]string{}
	for rows.Next() {
		var key, body string
		if err := rows.Scan(&key, &body); err != nil {
			return nil, err
		}
		overrides[key] = body
	}
	return overrides, rows.Err()
}

// maintenanceMessage renders one maintenance e-mail.
func maintenanceMessage(
	address, locale, transition, noticeRU, noticeEN string, expected pgtype.Timestamptz,
	overrides map[string]string,
) mailer.Message {
	language := "en"
	if locale == "ru" {
		language = "ru"
	}
	code, subject := notice.CodeMaintenanceEnded, mailer.Text(language, "maintenance_ended_subject")
	values := map[string]string{}
	if transition == "activated" {
		code, subject = notice.CodeMaintenanceStarted, mailer.Text(language, "maintenance_started_subject")
		body := noticeEN
		if language == "ru" && noticeRU != "" || body == "" {
			body = noticeRU
		}
		values["notice"] = body
		values["until"] = mailer.Text(language, "maintenance_soon")
		if expected.Valid {
			values["until"] = expected.Time.UTC().Format("02.01.2006 15:04") + " UTC"
		}
	}
	template, ok := overrides[string(code)+"/"+language]
	if !ok {
		definition, _ := notice.Lookup(string(code))
		template = definition.Default[language]
	}
	return mailer.Render(address, mailer.Content{
		Locale: language, Subject: subject,
		Paragraphs: []string{plainText(notice.Render(template, values))},
	})
}
