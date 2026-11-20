// Package guestpurchase lets a visitor with no account buy a subscription
// with only an e-mail address, for themselves or as a gift, as Bedolaga's
// guest purchases do (its landing pages are deliberately not reproduced).
//
// A guest purchase is a subscription gift order owned by the guest checkout
// system account, so payment, expiry, refunds, and the claim reuse the gift
// machinery. Once the order is paid the worker e-mails the activation code:
// to the payer, and for a gift with a recipient address to the recipient as
// well. Whoever redeems the code after signing in — by e-mail, Telegram, or
// OIDC — receives the subscription. The code is stored sealed only until it
// has been mailed.
package guestpurchase

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/gifts"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// AccountID is the guest checkout system account every guest order belongs to.
const AccountID = "00000000-0000-4000-8000-00000000600e"

// Errors a visitor can act on.
var (
	ErrInvalid  = errors.New("the purchase is not valid")
	ErrProvider = errors.New("that payment method is not available for a guest purchase")
	ErrNotFound = errors.New("no such purchase")
)

var emailPattern = regexp.MustCompile(`^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$`)

// Input is a guest's purchase.
type Input struct {
	PlanVersionID  string `json:"planVersionId"`
	Email          string `json:"email"`
	Gift           bool   `json:"gift"`
	RecipientEmail string `json:"recipientEmail"`
	Message        string `json:"message"`
	Provider       string `json:"provider"`
	Currency       string `json:"currency"`
	Locale         string `json:"locale"`
}

// Started is what the purchase page needs: where to pay, and a token to poll
// the purchase with.
type Started struct {
	ID          string `json:"id"`
	Token       string `json:"token,omitempty"`
	Status      string `json:"status"`
	CheckoutURL string `json:"checkoutUrl,omitempty"`
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
}

// Options configures the service.
type Options struct {
	DataKey    []byte
	PublicURL  string
	Currency   string
	Disposable mailer.DisposableChecker
	Logger     *slog.Logger
}

// Service is the guest purchase adapter.
type Service struct {
	pool     *pgxpool.Pool
	orders   *commercepg.Store
	payments *paymentservice.Service
	email    *emailpg.Service
	aead     cipher.AEAD
	options  Options
}

// New builds the service. orders and payments may be nil in the worker,
// which only mails paid purchases.
func New(pool *pgxpool.Pool, orders *commercepg.Store, payments *paymentservice.Service, email *emailpg.Service, options Options) (*Service, error) {
	if len(options.DataKey) != 32 || email == nil {
		return nil, errors.New("guest purchases need APP_DATA_ENCRYPTION_KEY and an e-mail queue")
	}
	block, err := aes.NewCipher(options.DataKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Currency == "" {
		options.Currency = "RUB"
	}
	options.PublicURL = strings.TrimRight(options.PublicURL, "/")
	return &Service{pool: pool, orders: orders, payments: payments, email: email, aead: aead, options: options}, nil
}

func (service *Service) validAddress(address string) bool {
	return len(address) <= 254 && emailPattern.MatchString(address) && !service.options.Disposable.IsDisposable(address)
}

// Start opens the order and its payment. A replay with the same idempotency
// key returns the same purchase without a new token.
func (service *Service) Start(ctx context.Context, idempotencyKey string, input Input) (Started, error) {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.RecipientEmail = strings.ToLower(strings.TrimSpace(input.RecipientEmail))
	input.Provider = strings.TrimSpace(input.Provider)
	if input.Locale != "en" {
		input.Locale = "ru"
	}
	if input.Currency == "" {
		input.Currency = service.options.Currency
	}
	if !service.validAddress(input.Email) || strings.TrimSpace(input.PlanVersionID) == "" ||
		utf8.RuneCountInString(input.Message) > 200 || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 ||
		(input.RecipientEmail != "" && (!input.Gift || !service.validAddress(input.RecipientEmail))) {
		return Started{}, ErrInvalid
	}
	// A guest has no chat to receive a Stars invoice in and no operator to
	// confirm a manual transfer for them.
	if input.Provider == "" || input.Provider == "telegram_stars" || input.Provider == "manual" || !service.payments.Enabled(input.Provider) {
		return Started{}, ErrProvider
	}
	purchase, err := service.orders.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: AccountID, Kind: gifts.KindSubscription, PlanVersionID: input.PlanVersionID,
		Currency: input.Currency, SenderMessage: input.Message, IdempotencyKey: "guest:" + idempotencyKey,
		SkipWallet: true, Provider: input.Provider,
	})
	if err != nil {
		return Started{}, err
	}
	orderID := purchase.Order.ID.String()
	started := Started{Status: "pending", AmountMinor: purchase.Order.ExternalMinor, Currency: purchase.Order.Currency}
	if purchase.Code != "" {
		token, digest, err := newToken()
		if err != nil {
			return Started{}, err
		}
		payer, err := service.seal(input.Email, "guest_email")
		if err != nil {
			return Started{}, err
		}
		var recipient []byte
		if input.RecipientEmail != "" {
			if recipient, err = service.seal(input.RecipientEmail, "guest_email"); err != nil {
				return Started{}, err
			}
		}
		code, err := service.seal(purchase.Code, "guest_code")
		if err != nil {
			return Started{}, err
		}
		if err = service.pool.QueryRow(ctx, `INSERT INTO guest_purchases (order_id, gift_id, payer_email_ciphertext,
				recipient_email_ciphertext, is_gift, locale, code_ciphertext, status_token_hash)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			orderID, purchase.Gift.ID, payer, recipient, input.Gift, input.Locale, code, digest).Scan(&started.ID); err != nil {
			return Started{}, err
		}
		started.Token = token
	} else if err = service.pool.QueryRow(ctx, `SELECT id::text FROM guest_purchases WHERE order_id = $1::uuid`, orderID).
		Scan(&started.ID); err != nil {
		return Started{}, err
	}
	if purchase.Order.ExternalMinor > 0 {
		intent, err := service.payments.CreateIntent(ctx, paymentservice.CreateIntentInput{
			OrderID: orderID, Provider: input.Provider, IdempotencyKey: "guest-pay:" + idempotencyKey,
			Description: "VPN", ReturnURL: service.options.PublicURL + "/guest?purchase=" + url.QueryEscape(started.ID),
		})
		if err != nil {
			return started, err
		}
		started.CheckoutURL = intent.CheckoutUrl.String
	}
	return started, nil
}

// Status reads a purchase for the page polling it.
func (service *Service) Status(ctx context.Context, id, token string) (Started, error) {
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 24 {
		return Started{}, ErrNotFound
	}
	digest := sha256.Sum256(raw)
	var started Started
	var orderState string
	err = service.pool.QueryRow(ctx, `SELECT g.id::text, g.status, o.state, o.external_minor, o.currency,
			COALESCE((SELECT p.checkout_url FROM payment_intents p WHERE p.order_id = o.id ORDER BY p.created_at DESC LIMIT 1), '')
		FROM guest_purchases g JOIN orders o ON o.id = g.order_id
		WHERE g.id = $1::uuid AND g.status_token_hash = $2`, id, digest[:]).
		Scan(&started.ID, &started.Status, &orderState, &started.AmountMinor, &started.Currency, &started.CheckoutURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return Started{}, ErrNotFound
	}
	if err != nil {
		return Started{}, ErrNotFound
	}
	if started.Status == "pending" && (orderState == "paid" || orderState == "fulfilled") {
		started.Status = "paid"
	}
	if started.Status != "pending" {
		started.CheckoutURL = ""
	}
	return started, nil
}

// Pass mails the activation code for every paid purchase and closes the ones
// whose order expired unpaid. Mailing and releasing the code commit together,
// so a crash in between leaves the purchase pending and the next pass mails
// it with the same dedupe keys.
func (service *Service) Pass(ctx context.Context) error {
	if _, err := service.pool.Exec(ctx, `UPDATE guest_purchases g SET status = 'expired', code_ciphertext = NULL
		FROM orders o WHERE o.id = g.order_id AND g.status = 'pending' AND o.state IN ('expired', 'cancelled')`); err != nil {
		return err
	}
	rows, err := service.pool.Query(ctx, `SELECT g.id::text, g.payer_email_ciphertext, g.recipient_email_ciphertext,
			g.is_gift, g.locale, g.code_ciphertext, COALESCE(gi.sender_message, ''),
			COALESCE((SELECT l.name FROM plan_localizations l WHERE l.plan_id = pv.plan_id AND l.locale = g.locale),
				(SELECT l.name FROM plan_localizations l WHERE l.plan_id = pv.plan_id LIMIT 1), 'VPN')
		FROM guest_purchases g
		JOIN orders o ON o.id = g.order_id
		JOIN gifts gi ON gi.id = g.gift_id
		LEFT JOIN plan_versions pv ON pv.id = gi.plan_version_id
		WHERE g.status = 'pending' AND o.state IN ('paid', 'fulfilled') AND g.code_ciphertext IS NOT NULL
		LIMIT 50`)
	if err != nil {
		return err
	}
	type paid struct {
		id, locale, message, plan string
		payer, recipient, code    []byte
		gift                      bool
	}
	var batch []paid
	for rows.Next() {
		var item paid
		if err = rows.Scan(&item.id, &item.payer, &item.recipient, &item.gift, &item.locale, &item.code,
			&item.message, &item.plan); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, item)
	}
	rows.Close()
	for _, item := range batch {
		if err := service.mail(ctx, item.id, item.locale, item.plan, item.message, item.payer, item.recipient, item.code, item.gift); err != nil {
			service.options.Logger.Error("guest purchase mail failed", "error", err)
		}
	}
	return rows.Err()
}

func (service *Service) mail(ctx context.Context, id, locale, plan, message string, sealedPayer, sealedRecipient, sealedCode []byte, gift bool) error {
	payer, err := service.open(sealedPayer, "guest_email")
	if err != nil {
		return err
	}
	code, err := service.open(sealedCode, "guest_code")
	if err != nil {
		return err
	}
	activate := service.options.PublicURL + "/account/wallet?code=" + url.QueryEscape(code)
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	notified := false
	if len(sealedRecipient) > 0 {
		recipient, err := service.open(sealedRecipient, "guest_email")
		if err != nil {
			return err
		}
		if _, err = service.email.QueueToAddress(ctx, tx, emailpg.KindGuestGift, "guest-gift:"+id,
			mailer.GuestGift(recipient, locale, plan, message, activate)); err != nil {
			return err
		}
		notified = true
	}
	if _, err = service.email.QueueToAddress(ctx, tx, emailpg.KindGuestPurchase, "guest-purchase:"+id,
		mailer.GuestPurchase(payer, locale, plan, code, activate, gift, notified)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE guest_purchases SET status = 'mailed', mailed_at = now(), code_ciphertext = NULL
		WHERE id = $1::uuid AND status = 'pending'`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func newToken() (string, []byte, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(raw), digest[:], nil
}

func (service *Service) seal(value, label string) ([]byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.aead.Seal(nonce, nonce, []byte(value), []byte(label)), nil
}

func (service *Service) open(sealed []byte, label string) (string, error) {
	size := service.aead.NonceSize()
	if len(sealed) <= size {
		return "", errors.New("sealed value is truncated")
	}
	plain, err := service.aead.Open(nil, sealed[:size], sealed[size:], []byte(label))
	return string(plain), err
}
