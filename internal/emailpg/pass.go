package emailpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/mailer"
)

// Pass is the worker's periodic mail pass: it turns what is owed — e-mail
// campaign recipients, personal offers marked for e-mail, settled payments —
// into queued messages, delivers what is due, and prunes old rows. Each step
// is safe to repeat, because every queued message has a dedupe key.
func (service *Service) Pass(ctx context.Context) error {
	var failures []error
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"campaigns", service.prepareCampaigns},
		{"offers", service.prepareOffers},
		{"receipts", service.prepareReceipts},
		{"delivery", service.deliver},
		{"prune", service.prune},
	} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := step.run(ctx); err != nil {
			service.options.Logger.Error("e-mail pass step failed", "step", step.name, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	return errors.Join(failures...)
}

// telegramMarkup strips the HTML tags a Telegram template may carry; an
// e-mail body is rendered from plain text and escaped again.
var telegramMarkup = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

func plainText(body string) string {
	return strings.TrimSpace(telegramMarkup.ReplaceAllString(body, ""))
}

// prepareCampaigns queues the next recipients of every running e-mail
// campaign. A recipient stays `queued` in campaign_recipients until delivery
// resolves it, so a campaign completes only once its mail has actually gone.
func (service *Service) prepareCampaigns(ctx context.Context) error {
	rows, err := service.pool.Query(ctx, `SELECT c.id::text, r.user_id::text, t.class,
			t.subject_en, t.subject_ru, t.body_en, t.body_ru
		FROM campaign_recipients r
		JOIN campaigns c ON c.id = r.campaign_id
		JOIN message_templates t ON t.id = c.template_id
		WHERE c.status = 'running' AND c.channel = 'email' AND r.status = 'queued'
		  AND NOT EXISTS (SELECT 1 FROM email_outbox e
			WHERE e.dedupe_key = 'campaign:' || c.id::text || ':' || r.user_id::text)
		ORDER BY r.queued_at, r.user_id
		LIMIT $1`, prepareBatch)
	if err != nil {
		return err
	}
	type pending struct {
		campaign, customer, class            string
		subjectEN, subjectRU, bodyEN, bodyRU string
	}
	var batch []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.campaign, &item.customer, &item.class,
			&item.subjectEN, &item.subjectRU, &item.bodyEN, &item.bodyRU); err != nil {
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
		person, found, err := service.recipientFor(ctx, item.customer)
		if err != nil {
			return err
		}
		if !found {
			if err = service.resolveRecipient(ctx, item.campaign, item.customer, "suppressed", "no_email", ""); err != nil {
				return err
			}
			continue
		}
		reason := ""
		if item.class == ClassMarketing {
			if reason, err = service.marketingDecision(person); err != nil {
				return err
			}
		} else if !person.Transactional || !person.Active {
			reason = "no_consent"
		}
		if reason != "" {
			if err = service.resolveRecipient(ctx, item.campaign, item.customer, "suppressed", reason, ""); err != nil {
				return err
			}
			continue
		}
		subject, body := item.subjectEN, item.bodyEN
		if person.Locale == "ru" {
			subject, body = item.subjectRU, item.bodyRU
		}
		body = plainText(commerce.RenderTemplate(body, map[string]string{}))
		if strings.TrimSpace(subject) == "" {
			subject = firstLine(body)
		}
		message := mailer.Render(person.Address, mailer.Content{Locale: person.Locale, Subject: subject, Paragraphs: []string{body}})
		if item.class == ClassMarketing {
			message = mailer.Broadcast(person.Address, person.Locale, subject, body, service.UnsubscribeURL(item.customer))
		}
		if _, err = service.Enqueue(ctx, nil, Item{
			CustomerID: item.customer, Kind: KindCampaign, Class: item.class, CampaignID: item.campaign,
			Dedupe: "campaign:" + item.campaign + ":" + item.customer, Message: message,
		}); err != nil {
			return err
		}
	}
	return nil
}

func firstLine(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	if runes := []rune(line); len(runes) > 80 {
		return string(runes[:80])
	}
	return line
}

// resolveRecipient records a campaign recipient's outcome. The status guard
// makes it safe to repeat.
func (service *Service) resolveRecipient(ctx context.Context, campaignID, customerID, status, reason, errorCode string) error {
	_, err := service.pool.Exec(ctx, `UPDATE campaign_recipients
		SET status = $3, suppression_reason = NULLIF($4, ''), error_code = NULLIF($5, ''), resolved_at = now()
		WHERE campaign_id = $1::uuid AND user_id = $2::uuid AND status = 'queued'`,
		campaignID, customerID, status, reason, errorCode)
	return err
}

// prepareOffers e-mails every active personal offer the operator marked for
// e-mail. An offer the policy refuses is recorded as skipped with its reason.
func (service *Service) prepareOffers(ctx context.Context) error {
	rows, err := service.pool.Query(ctx, `SELECT o.id::text, o.user_id::text, o.title_en, o.title_ru,
			o.terms_en, o.terms_ru, o.expires_at
		FROM personal_offers o
		WHERE o.notify_email AND o.status = 'active' AND o.starts_at <= now() AND o.expires_at > now()
		  AND NOT EXISTS (SELECT 1 FROM email_outbox e WHERE e.dedupe_key = 'offer:' || o.id::text)
		ORDER BY o.created_at LIMIT $1`, prepareBatch)
	if err != nil {
		return err
	}
	type offer struct {
		id, customer, titleEN, titleRU, termsEN, termsRU string
		expires                                          time.Time
	}
	var batch []offer
	for rows.Next() {
		var item offer
		if err = rows.Scan(&item.id, &item.customer, &item.titleEN, &item.titleRU,
			&item.termsEN, &item.termsRU, &item.expires); err != nil {
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
		dedupe := "offer:" + item.id
		person, found, err := service.recipientFor(ctx, item.customer)
		if err != nil {
			return err
		}
		if !found {
			if err = service.recordSkipped(ctx, item.customer, KindOffer, ClassMarketing, dedupe, "no_email"); err != nil {
				return err
			}
			continue
		}
		reason, err := service.marketingDecision(person)
		if err != nil {
			return err
		}
		if reason != "" {
			if err = service.recordSkipped(ctx, item.customer, KindOffer, ClassMarketing, dedupe, reason); err != nil {
				return err
			}
			continue
		}
		title, terms := item.titleEN, item.termsEN
		if person.Locale == "ru" {
			title, terms = item.titleRU, item.termsRU
		}
		message := mailer.Offer(person.Address, person.Locale, title, terms,
			item.expires.UTC().Format("2006-01-02 15:04 UTC"), service.options.PublicURL+"/account",
			service.UnsubscribeURL(item.customer))
		if _, err = service.Enqueue(ctx, nil, Item{
			CustomerID: item.customer, Kind: KindOffer, Class: ClassMarketing, Dedupe: dedupe, Message: message,
		}); err != nil {
			return err
		}
	}
	return nil
}

// operationLabels names a purchase in a receipt.
var operationLabels = map[string][2]string{
	"purchase":  {"VPN subscription", "Подписка VPN"},
	"extension": {"Subscription extension", "Продление подписки"},
	"renewal":   {"Subscription renewal", "Продление подписки"},
	"upgrade":   {"Plan change", "Смена тарифа"},
	"downgrade": {"Plan change", "Смена тарифа"},
	"topup":     {"Balance top-up", "Пополнение баланса"},
	"addon":     {"Add-on", "Дополнение"},
	"gift":      {"Gift", "Подарок"},
	"goods":     {"Digital goods", "Цифровой товар"},
	"code":      {"Code", "Код"},
}

// prepareReceipts queues a confirmation for each recently settled payment
// whose customer has a verified address that accepts transactional mail.
// Only orders paid within receiptWindow are considered, so turning receipts
// on does not mail anyone about an old order.
func (service *Service) prepareReceipts(ctx context.Context) error {
	if !service.options.ReceiptsEnabled {
		return nil
	}
	rows, err := service.pool.Query(ctx, `SELECT o.id::text, o.user_id::text, o.operation, o.currency,
			o.wallet_minor + o.external_minor, o.paid_at
		FROM orders o
		WHERE o.paid_at > now() - $1::interval AND o.external_minor > 0
		  AND o.state IN ('paid', 'fulfilled', 'partially_refunded')
		  AND EXISTS (SELECT 1 FROM contact_channels c WHERE c.user_id = o.user_id AND c.kind = 'email'
			AND c.revoked_at IS NULL AND c.verified_at IS NOT NULL AND c.transactional_enabled)
		  AND NOT EXISTS (SELECT 1 FROM email_outbox e WHERE e.dedupe_key = 'receipt:' || o.id::text)
		ORDER BY o.paid_at LIMIT $2`, receiptWindow, prepareBatch)
	if err != nil {
		return err
	}
	type order struct {
		id, customer, operation, currency string
		amount                            int64
		paid                              time.Time
	}
	var batch []order
	for rows.Next() {
		var item order
		if err = rows.Scan(&item.id, &item.customer, &item.operation, &item.currency, &item.amount, &item.paid); err != nil {
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
		label := operationLabels[item.operation]
		if _, err := service.QueueForCustomer(ctx, nil, item.customer, KindReceipt, "receipt:"+item.id,
			func(address, locale string) mailer.Message {
				description := label[0]
				if locale == "ru" {
					description = label[1]
				}
				return mailer.ReceiptMessage(address, locale, mailer.Receipt{
					OrderNumber: strings.ToUpper(item.id[:8]), Description: description,
					Amount:    formatAmount(item.amount, item.currency),
					Paid:      item.paid.UTC().Format("2006-01-02 15:04 UTC"),
					OrdersURL: service.options.PublicURL + "/account/orders/" + item.id,
				})
			}); err != nil {
			return err
		}
	}
	return nil
}

// due is a queued message ready to send.
type due struct {
	id, campaign, customer string
	payload                []byte
	attempts               int
}

// deliver sends what is due. Periodic passes are unique across the
// installation, so two workers never send the same row; the status guard on
// every update is the second line of defence.
func (service *Service) deliver(ctx context.Context) error {
	if service.options.Sender == nil {
		return nil
	}
	rows, err := service.pool.Query(ctx, `SELECT id::text, COALESCE(campaign_id::text, ''), COALESCE(user_id::text, ''),
			payload_ciphertext, attempts
		FROM email_outbox WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY next_attempt_at LIMIT $1`, deliveryBatch)
	if err != nil {
		return err
	}
	var batch []due
	for rows.Next() {
		var item due
		if err = rows.Scan(&item.id, &item.campaign, &item.customer, &item.payload, &item.attempts); err != nil {
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
		if ctx.Err() != nil {
			return ctx.Err()
		}
		service.deliverOne(ctx, item)
	}
	return nil
}

func (service *Service) deliverOne(ctx context.Context, item due) {
	plaintext, err := service.open(item.payload, "email_outbox")
	var message mailer.Message
	if err == nil {
		err = json.Unmarshal(plaintext, &message)
	}
	if err != nil {
		// A row sealed under a different key will never open; retrying it
		// only repeats the failure.
		service.finish(ctx, item, "failed", "unreadable")
		return
	}
	sendErr := service.options.Sender.Send(ctx, message)
	switch {
	case sendErr == nil:
		service.finish(ctx, item, "sent", "")
	case errors.Is(sendErr, mailer.ErrPermanent):
		service.finish(ctx, item, "failed", "rejected")
	case item.attempts+1 >= maxAttempts:
		service.finish(ctx, item, "failed", "exhausted")
	default:
		// Exponential backoff from one minute, capped at four hours.
		delay := min(time.Minute<<item.attempts, 4*time.Hour)
		if _, err := service.pool.Exec(ctx, `UPDATE email_outbox SET attempts = attempts + 1,
				next_attempt_at = now() + $2::interval, last_error_code = 'temporary'
			WHERE id = $1::uuid AND status = 'pending'`, item.id, delay); err != nil {
			service.options.Logger.Error("e-mail retry bookkeeping failed", "error", err)
		}
		service.options.Logger.Warn("e-mail delivery deferred", "attempt", item.attempts+1, "error", sendErr)
	}
}

func (service *Service) finish(ctx context.Context, item due, status, code string) {
	if _, err := service.pool.Exec(ctx, `UPDATE email_outbox SET status = $2, attempts = attempts + 1,
			last_error_code = NULLIF($3, ''), sent_at = CASE WHEN $2 = 'sent' THEN now() END
		WHERE id = $1::uuid AND status = 'pending'`, item.id, status, code); err != nil {
		service.options.Logger.Error("e-mail bookkeeping failed", "error", err)
		return
	}
	if item.campaign == "" || item.customer == "" {
		return
	}
	recipientStatus, errorCode := "sent", ""
	if status != "sent" {
		recipientStatus, errorCode = "failed", "email_"+code
	}
	if err := service.resolveRecipient(ctx, item.campaign, item.customer, recipientStatus, "", errorCode); err != nil {
		service.options.Logger.Error("campaign bookkeeping failed", "campaignId", item.campaign, "error", err)
	}
}

// prune removes delivered and failed rows past retention.
func (service *Service) prune(ctx context.Context) error {
	_, err := service.pool.Exec(ctx, `DELETE FROM email_outbox WHERE id IN (
		SELECT id FROM email_outbox WHERE status <> 'pending' AND created_at < now() - $1::interval LIMIT 1000)`, retention)
	return err
}
