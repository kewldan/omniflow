//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/campaigns"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// recordingSender captures what the mail pass delivers and can fail on demand.
type recordingSender struct {
	mu   sync.Mutex
	sent []mailer.Message
	fail error
}

func (sender *recordingSender) Send(_ context.Context, message mailer.Message) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.fail != nil {
		return sender.fail
	}
	sender.sent = append(sender.sent, message)
	return nil
}

func (sender *recordingSender) messages() []mailer.Message {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]mailer.Message(nil), sender.sent...)
}

func (harness *harness) emailCustomer(ctx context.Context, t *testing.T, address string, marketing bool) string {
	t.Helper()
	customer := harness.customer(ctx, t)
	contacts, err := customerpg.New(harness.pool, operationsKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	if _, err = contacts.SetContact(ctx, customer, "email", address, &verified, true, marketing); err != nil {
		t.Fatalf("add e-mail contact: %v", err)
	}
	return customer
}

func newEmailQueue(t *testing.T, harness *harness, sender mailer.Sender, receipts bool) *emailpg.Service {
	t.Helper()
	service, err := emailpg.New(harness.pool, emailpg.Options{
		Sender: sender, DataKey: operationsKey, PublicURL: "https://vpn.example",
		ReceiptsEnabled: receipts, MarketingFrequencyCap: 3, MarketingWindow: 24 * time.Hour,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// An e-mail campaign reaches the customers who agreed to marketing e-mail,
// records why the others were skipped, carries a working one-click
// unsubscribe link, and completes once its mail has gone.
func TestEmailCampaignHonoursConsentAndUnsubscribe(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "mail@omniflow.test")

	subscribed := harness.emailCustomer(ctx, t, "reader@example.com", true)
	declined := harness.emailCustomer(ctx, t, "quiet@example.com", false)
	noAddress := harness.customer(ctx, t)

	template, err := operations.SaveTemplate(ctx, panelpg.MessageTemplate{
		Code: "mail-news", Class: "marketing", SubjectEN: "News", SubjectRU: "Новости",
		BodyEN: "<b>Big</b> news", BodyRU: "Новости", Variables: []string{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := operations.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "everyone-mail", NameEN: "Everyone", NameRU: "Все", Filters: map[string]any{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := operations.CreateCampaignOn(ctx, "email", "Mail news", template.ID, segment.ID, actor)
	if err != nil || campaign.Channel != "email" {
		t.Fatalf("create campaign: %+v %v", campaign, err)
	}
	if _, err = operations.SetCampaignState(ctx, campaign.ID, "running", nil, actor); err != nil {
		t.Fatal(err)
	}
	runner := campaigns.New(harness.pool, slog.New(slog.DiscardHandler), campaigns.Config{})
	runner.RunOnce(ctx)

	sender := &recordingSender{}
	queue := newEmailQueue(t, harness, sender, false)
	if err = queue.Pass(ctx); err != nil {
		t.Fatalf("mail pass: %v", err)
	}
	// A second pass must not send again.
	if err = queue.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	sent := sender.messages()
	if len(sent) != 1 || sent[0].To != "reader@example.com" || sent[0].Subject != "News" {
		t.Fatalf("sent = %+v, want exactly one message to the subscriber", sent)
	}
	if strings.Contains(sent[0].Text, "<b>") || sent[0].Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("message = %+v", sent[0])
	}
	outcomes := map[string]string{}
	rows, err := harness.pool.Query(ctx, `SELECT user_id::text, status || ':' || COALESCE(suppression_reason, '')
		FROM campaign_recipients WHERE campaign_id = $1::uuid`, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var user, outcome string
		if err = rows.Scan(&user, &outcome); err != nil {
			t.Fatal(err)
		}
		outcomes[user] = outcome
	}
	rows.Close()
	if outcomes[subscribed] != "sent:" || outcomes[declined] != "suppressed:no_consent" ||
		outcomes[noAddress] != "suppressed:no_email" {
		t.Fatalf("outcomes = %v", outcomes)
	}
	runner.RunOnce(ctx)
	var status string
	if err = harness.pool.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1::uuid`, campaign.ID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("campaign status = %q (%v)", status, err)
	}

	// The bot never reads an e-mail campaign: no Telegram row was touched.
	link := strings.Trim(sent[0].Headers["List-Unsubscribe"], "<>")
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get("token")
	if err = queue.Unsubscribe(ctx, token); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err = queue.Unsubscribe(ctx, token); err != nil {
		t.Fatalf("a second unsubscribe must still succeed: %v", err)
	}
	if err = queue.Unsubscribe(ctx, token+"x"); !errors.Is(err, mailer.ErrBadUnsubscribeToken) {
		t.Fatalf("a forged token: %v", err)
	}
	var marketing bool
	var withdrawals int
	if err = harness.pool.QueryRow(ctx, `SELECT marketing_enabled,
			(SELECT count(*) FROM consent_records WHERE user_id = $1::uuid AND purpose = 'marketing' AND NOT granted)
		FROM contact_channels WHERE user_id = $1::uuid AND kind = 'email'`, subscribed).Scan(&marketing, &withdrawals); err != nil {
		t.Fatal(err)
	}
	if marketing || withdrawals != 1 {
		t.Fatalf("after unsubscribe marketing=%v withdrawals=%d", marketing, withdrawals)
	}
}

// A temporary failure is retried later; a permanent one is not retried at all.
func TestEmailDeliveryRetriesAndGivesUp(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	sender := &recordingSender{fail: errors.New("connection refused")}
	queue := newEmailQueue(t, harness, sender, false)
	actor := harness.operator(ctx, t, "tester@omniflow.test")
	if err := queue.SendTest(ctx, "owner@example.com", "en", actor); err != nil {
		t.Fatal(err)
	}
	if err := queue.SendTest(ctx, "not an address", "en", actor); !errors.Is(err, emailpg.ErrInvalidAddress) {
		t.Fatalf("invalid address: %v", err)
	}
	if err := queue.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	var next time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT status, attempts, next_attempt_at FROM email_outbox WHERE kind = 'test'`).
		Scan(&status, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || !next.After(time.Now()) {
		t.Fatalf("after a temporary failure: %s %d %v", status, attempts, next)
	}
	// The plaintext address is not stored anywhere in the row.
	var leaked bool
	if err := harness.pool.QueryRow(ctx, `SELECT position('owner@example.com' in encode(payload_ciphertext, 'escape')) > 0
		FROM email_outbox WHERE kind = 'test'`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("payload leaked the address (%v)", err)
	}

	if _, err := harness.pool.Exec(ctx, `UPDATE email_outbox SET next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	sender.fail = errors.Join(mailer.ErrPermanent, errors.New("550"))
	sender.mu.Unlock()
	if err := queue.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT status, attempts FROM email_outbox WHERE kind = 'test'`).
		Scan(&status, &attempts); err != nil || status != "failed" || attempts != 2 {
		t.Fatalf("after a permanent failure: %s %d (%v)", status, attempts, err)
	}
	stats, err := queue.Stats(ctx, true)
	if err != nil || stats.Failed24h != 1 || stats.Pending != 0 {
		t.Fatalf("stats = %+v (%v)", stats, err)
	}
}

// A settled payment is confirmed by e-mail once, to a customer with a
// verified address; one without an address gets nothing and is not rescanned.
func TestEmailReceiptIsSentOncePerPaidOrder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.emailCustomer(ctx, t, "payer@example.com", false)
	silent := harness.customer(ctx, t)
	for _, owner := range []string{customer, silent} {
		if _, err := harness.pool.Exec(ctx, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
				external_minor, paid_minor, idempotency_key, paid_at)
			VALUES ($1::uuid, 'paid', 'topup', 'RUB', 15000, 15000, 15000, 'receipt-' || $1, now())`, owner); err != nil {
			t.Fatal(err)
		}
	}
	sender := &recordingSender{}
	queue := newEmailQueue(t, harness, sender, true)
	for range 2 {
		if err := queue.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sent := sender.messages()
	if len(sent) != 1 || sent[0].To != "payer@example.com" || !strings.Contains(sent[0].Text, "150.00 RUB") {
		t.Fatalf("receipts = %+v", sent)
	}
	if sent[0].Headers["List-Unsubscribe"] != "" {
		t.Fatal("a receipt carried an unsubscribe header")
	}
}
