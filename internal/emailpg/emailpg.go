// Package emailpg is the durable e-mail queue.
//
// Every e-mail except the sign-in link is written to email_outbox in the
// transaction that decides to send it and delivered by the worker's mail pass
// with retries; the sign-in link stays synchronous because the customer is
// waiting on the form. The rendered message is sealed with the installation's
// data key, so the table holds no address, subject, or link in clear.
//
// Marketing mail goes through the same policy as a Telegram broadcast:
// commerce.EvaluateDelivery decides consent and the frequency cap, the
// suppression list vetoes, and the cap counts marketing sent on either
// channel. The customer's consent for e-mail is the marketing flag on their
// verified e-mail contact, and every marketing message carries an RFC 8058
// one-click unsubscribe link that needs no sign-in.
package emailpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/payments"
)

// Classes of message.
const (
	ClassTransactional = "transactional"
	ClassMarketing     = "marketing"
)

// Kinds of message the queue holds; they match email_outbox_kind_known.
const (
	KindReceipt       = "receipt"
	KindCampaign      = "campaign"
	KindOffer         = "offer"
	KindGuestPurchase = "guest_purchase"
	KindGuestGift     = "guest_gift"
	KindWithdrawal    = "withdrawal"
	KindTest          = "test"
	// KindMaintenance is declared in maintenance.go.
)

const (
	deliveryBatch = 50
	prepareBatch  = 200
	// maxAttempts bounds a temporary failure: with the backoff below the last
	// attempt is about nine hours after the first.
	maxAttempts = 8
	// receiptWindow is how far back the receipt scan looks. It is short so
	// switching receipts on does not mail every customer about last month.
	receiptWindow = 6 * time.Hour
	// retention keeps delivered and failed rows long enough for the frequency
	// window and an operator's questions, then removes them.
	retention = 90 * 24 * time.Hour
)

// ErrUnavailable reports that e-mail is not configured.
var ErrUnavailable = errors.New("e-mail is not configured")

// ErrInvalidAddress reports an address the queue will not accept.
var ErrInvalidAddress = errors.New("that is not a deliverable e-mail address")

// Options configures the service.
type Options struct {
	// Sender delivers. Nil leaves the queue writable and the delivery pass
	// idle, so the api can enqueue while only the worker sends.
	Sender mailer.Sender
	// DataKey is APP_DATA_ENCRYPTION_KEY.
	DataKey   []byte
	PublicURL string
	// ReceiptsEnabled e-mails a confirmation for every settled payment.
	ReceiptsEnabled bool
	// The marketing policy shared with the Telegram broadcasts.
	MarketingFrequencyCap int
	MarketingWindow       time.Duration
	Disposable            mailer.DisposableChecker
	Logger                *slog.Logger
	Clock                 func() time.Time
}

// Service is the queue.
type Service struct {
	pool    *pgxpool.Pool
	aead    cipher.AEAD
	signer  mailer.UnsubscribeSigner
	options Options
}

// New builds the service.
func New(pool *pgxpool.Pool, options Options) (*Service, error) {
	if len(options.DataKey) != 32 {
		return nil, errors.New("the e-mail queue needs APP_DATA_ENCRYPTION_KEY")
	}
	block, err := aes.NewCipher(options.DataKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	signer, err := mailer.NewUnsubscribeSigner(options.DataKey)
	if err != nil {
		return nil, err
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.MarketingWindow <= 0 {
		options.MarketingWindow = 7 * 24 * time.Hour
	}
	options.PublicURL = strings.TrimRight(options.PublicURL, "/")
	return &Service{pool: pool, aead: aead, signer: signer, options: options}, nil
}

// Sending reports whether this process has a transport.
func (service *Service) Sending() bool { return service != nil && service.options.Sender != nil }

// Item is one message to queue.
type Item struct {
	CustomerID string
	Kind       string
	Class      string
	Dedupe     string
	CampaignID string
	Message    mailer.Message
}

// Enqueue writes a message in the caller's transaction (or on the pool). It
// reports false when the dedupe key was already used, which is how a retried
// caller learns the message is already on its way.
func (service *Service) Enqueue(ctx context.Context, db dbgen.DBTX, item Item) (bool, error) {
	if db == nil {
		db = service.pool
	}
	if strings.TrimSpace(item.Message.To) == "" {
		return false, ErrInvalidAddress
	}
	payload, err := json.Marshal(item.Message)
	if err != nil {
		return false, err
	}
	sealed, err := service.seal(payload, "email_outbox")
	if err != nil {
		return false, err
	}
	tag, err := db.Exec(ctx, `INSERT INTO email_outbox (user_id, kind, class, dedupe_key, payload_ciphertext, campaign_id)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5, NULLIF($6, '')::uuid)
		ON CONFLICT (dedupe_key) DO NOTHING`,
		item.CustomerID, item.Kind, item.Class, item.Dedupe, sealed, item.CampaignID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// recordSkipped writes a row that was decided against, so the decision is
// visible and the scan that found it does not find it again.
func (service *Service) recordSkipped(ctx context.Context, customerID, kind, class, dedupe, reason string) error {
	_, err := service.pool.Exec(ctx, `INSERT INTO email_outbox
		(user_id, kind, class, dedupe_key, payload_ciphertext, status, last_error_code)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4, '\x'::bytea, 'failed', $5)
		ON CONFLICT (dedupe_key) DO NOTHING`, customerID, kind, class, dedupe, reason)
	return err
}

func (service *Service) seal(plaintext []byte, label string) ([]byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.aead.Seal(nonce, nonce, plaintext, []byte(label)), nil
}

func (service *Service) open(ciphertext []byte, label string) ([]byte, error) {
	size := service.aead.NonceSize()
	if len(ciphertext) <= size {
		return nil, errors.New("sealed value is truncated")
	}
	return service.aead.Open(nil, ciphertext[:size], ciphertext[size:], []byte(label))
}

// UnsubscribeURL is the one-click endpoint the List-Unsubscribe header names.
// It is the API route itself, because RFC 8058 has the mail client POST to it.
func (service *Service) UnsubscribeURL(customerID string) string {
	return service.options.PublicURL + "/v1/account/email/unsubscribe?token=" +
		url.QueryEscape(service.signer.Token(customerID))
}

// Unsubscribe stops marketing e-mail for the customer a token names. It is
// idempotent: a second click, or a mail client posting twice, changes nothing
// more and still succeeds.
func (service *Service) Unsubscribe(ctx context.Context, token string) error {
	customerID, err := service.signer.Verify(token)
	if err != nil {
		return err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE contact_channels SET marketing_enabled = false
		WHERE user_id = $1::uuid AND kind = 'email' AND marketing_enabled AND revoked_at IS NULL`, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		// The consent trail records the withdrawal with its source, so an
		// operator asked "why did they stop getting mail" has a dated answer.
		if _, err = tx.Exec(ctx, `INSERT INTO consent_records (user_id, purpose, granted, policy_version, source)
			VALUES ($1::uuid, 'marketing', false, 'email-unsubscribe', 'email_unsubscribe')`, customerID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// CheckAddress refuses an address on a throwaway domain.
func (service *Service) CheckAddress(address string) error {
	if service.options.Disposable.IsDisposable(address) {
		return ErrInvalidAddress
	}
	return nil
}

// recipient is a customer's e-mail address and what may be sent to it.
type recipient struct {
	Address       string
	Locale        string
	Marketing     bool
	Transactional bool
	Suppressed    bool
	SentInWindow  int
	Active        bool
}

// recipientFor resolves the newest verified, unrevoked e-mail address of a
// customer. found is false when there is none.
func (service *Service) recipientFor(ctx context.Context, customerID string) (recipient, bool, error) {
	var (
		value []byte
		found recipient
	)
	err := service.pool.QueryRow(ctx, `SELECT c.value_ciphertext, u.locale, c.marketing_enabled, c.transactional_enabled,
			EXISTS (SELECT 1 FROM communication_suppressions s WHERE s.user_id = u.id),
			(SELECT count(*)::integer FROM notification_deliveries n WHERE n.user_id = u.id
				AND n.class = 'marketing' AND n.status = 'sent' AND n.sent_at > now() - $2::interval)
			+ (SELECT count(*)::integer FROM email_outbox e WHERE e.user_id = u.id
				AND e.class = 'marketing' AND e.status IN ('pending', 'sent') AND e.payload_ciphertext <> '\x'::bytea
				AND e.created_at > now() - $2::interval),
			u.status = 'active'
		FROM users u
		JOIN LATERAL (SELECT * FROM contact_channels c WHERE c.user_id = u.id AND c.kind = 'email'
			AND c.revoked_at IS NULL AND c.verified_at IS NOT NULL AND c.value_ciphertext IS NOT NULL
			ORDER BY c.verified_at DESC LIMIT 1) c ON true
		WHERE u.id = $1::uuid`, customerID, service.options.MarketingWindow).Scan(
		&value, &found.Locale, &found.Marketing, &found.Transactional, &found.Suppressed,
		&found.SentInWindow, &found.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return recipient{}, false, nil
	}
	if err != nil {
		return recipient{}, false, err
	}
	address, err := service.open(value, "email")
	if err != nil {
		return recipient{}, false, err
	}
	found.Address = string(address)
	return found, true, nil
}

// marketingDecision applies the shared policy to one recipient and returns
// "" when the message may go, or the suppression reason.
func (service *Service) marketingDecision(person recipient) (string, error) {
	if !person.Active {
		return "no_consent", nil
	}
	decision, err := commerce.EvaluateDelivery(service.options.Clock().UTC(), "marketing", commerce.DeliveryPolicy{
		KindEnabled:           true,
		MarketingConsent:      person.Marketing,
		MarketingSentInWindow: person.SentInWindow,
		MarketingFrequencyCap: service.options.MarketingFrequencyCap,
		FrequencyWindow:       service.options.MarketingWindow,
	})
	if err != nil {
		return "", err
	}
	switch {
	case person.Suppressed:
		return "suppressed", nil
	case decision.Allow:
		return "", nil
	case decision.Reason == "frequency_cap":
		return "frequency_cap", nil
	default:
		return "no_consent", nil
	}
}

// QueueForCustomer renders and queues a transactional message to a
// customer's verified address. It reports false when the customer has none,
// or has switched transactional mail off for it.
func (service *Service) QueueForCustomer(
	ctx context.Context, db dbgen.DBTX, customerID, kind, dedupe string,
	render func(address, locale string) mailer.Message,
) (bool, error) {
	person, found, err := service.recipientFor(ctx, customerID)
	if err != nil || !found || !person.Transactional || !person.Active {
		return false, err
	}
	return service.Enqueue(ctx, db, Item{
		CustomerID: customerID, Kind: kind, Class: ClassTransactional, Dedupe: dedupe,
		Message: render(person.Address, person.Locale),
	})
}

// QueueToAddress queues a transactional message to an address that belongs
// to no customer yet: a guest buyer, a gift recipient, an operator's test.
func (service *Service) QueueToAddress(
	ctx context.Context, db dbgen.DBTX, kind, dedupe string, message mailer.Message,
) (bool, error) {
	return service.Enqueue(ctx, db, Item{Kind: kind, Class: ClassTransactional, Dedupe: dedupe, Message: message})
}

// SendTest queues the operator's test message and audits it.
func (service *Service) SendTest(ctx context.Context, address, locale string, actor panelpg.Actor) error {
	address = strings.TrimSpace(address)
	if len(address) > 254 || !strings.Contains(address, "@") || strings.ContainsAny(address, " \r\n<>") {
		return ErrInvalidAddress
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = service.QueueToAddress(ctx, tx, KindTest, fmt.Sprintf("test:%x", nonce), mailer.Test(address, locale)); err != nil {
		return err
	}
	// The address is not written to the audit trail: it is personal data, and
	// "an operator sent a test" is the fact worth keeping.
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.email.test_sent", "configuration", "email", "transport", nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Stats is the queue's state for the panel.
type Stats struct {
	Sending   bool           `json:"sending"`
	Receipts  bool           `json:"receipts"`
	Pending   int64          `json:"pending"`
	Sent24h   int64          `json:"sent24h"`
	Failed24h int64          `json:"failed24h"`
	Skipped   map[string]int `json:"skipped24h"`
}

// Stats reads the queue counters. sending is passed in because the api, which
// serves the panel, may hold no transport while the worker does.
func (service *Service) Stats(ctx context.Context, sending bool) (Stats, error) {
	stats := Stats{Sending: sending, Receipts: service.options.ReceiptsEnabled, Skipped: map[string]int{}}
	if err := service.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'sent' AND sent_at > now() - interval '24 hours'),
			count(*) FILTER (WHERE status = 'failed' AND payload_ciphertext <> '\x'::bytea AND created_at > now() - interval '24 hours')
		FROM email_outbox`).Scan(&stats.Pending, &stats.Sent24h, &stats.Failed24h); err != nil {
		return Stats{}, err
	}
	rows, err := service.pool.Query(ctx, `SELECT COALESCE(last_error_code, 'unknown'), count(*)::integer FROM email_outbox
		WHERE status = 'failed' AND payload_ciphertext = '\x'::bytea AND created_at > now() - interval '24 hours'
		GROUP BY 1`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var count int
		if err = rows.Scan(&reason, &count); err != nil {
			return Stats{}, err
		}
		stats.Skipped[reason] = count
	}
	return stats, rows.Err()
}

// formatAmount renders money for a message body.
func formatAmount(amountMinor int64, currency string) string {
	return payments.FormatAmount(amountMinor, currency)
}
