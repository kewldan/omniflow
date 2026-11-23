package referralpayout

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/walletpost"
)

// Errors a customer can act on.
var (
	ErrDisabled     = errors.New("withdrawals are not available")
	ErrBelowMinimum = errors.New("the amount is below the minimum")
	ErrInsufficient = errors.New("the amount is more than can be withdrawn")
	ErrCooldown     = errors.New("the next request is not allowed yet")
	ErrPending      = errors.New("a request is already being reviewed")
	ErrInvalid      = errors.New("the request is not valid")
	ErrNotFound     = errors.New("no such request")
	ErrState        = errors.New("the request is not in a state that allows this")
)

// Methods a payout can be made by.
var methods = map[string]bool{"card": true, "sbp": true, "crypto": true, "other": true}

// Settings is the operator's policy.
type Settings struct {
	Enabled      bool   `json:"enabled"`
	Currency     string `json:"currency"`
	MinimumMinor int64  `json:"minimumMinor"`
	CooldownDays int    `json:"cooldownDays"`
	OnlyReferral bool   `json:"onlyReferralBalance"`
}

// Request is one withdrawal request.
type Request struct {
	ID          string     `json:"id"`
	CustomerID  string     `json:"customerId,omitempty"`
	AmountMinor int64      `json:"amountMinor"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	Method      string     `json:"method"`
	Risk        Assessment `json:"risk"`
	Note        string     `json:"note,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
	PaidAt      *time.Time `json:"paidAt,omitempty"`
}

// Summary is what the customer's screen shows.
type Summary struct {
	Settings
	EarnedMinor    int64      `json:"earnedMinor"`
	RequestedMinor int64      `json:"requestedMinor"`
	AvailableMinor int64      `json:"availableMinor"`
	NextAllowedAt  *time.Time `json:"nextAllowedAt,omitempty"`
	Requests       []Request  `json:"requests"`
}

// Service is the withdrawal adapter.
type Service struct {
	pool   *pgxpool.Pool
	aead   cipher.AEAD
	email  *emailpg.Service
	logger *slog.Logger
	clock  func() time.Time
}

// New builds the service. email may be nil; decisions are then not mailed.
func New(pool *pgxpool.Pool, dataKey []byte, email *emailpg.Service, logger *slog.Logger) (*Service, error) {
	if len(dataKey) != 32 {
		return nil, errors.New("withdrawals need APP_DATA_ENCRYPTION_KEY to seal payout details")
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, aead: aead, email: email, logger: logger, clock: time.Now}, nil
}

// Settings reads the policy.
func (service *Service) Settings(ctx context.Context) (Settings, error) {
	return readSettings(ctx, service.pool)
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readSettings(ctx context.Context, db queryRower) (Settings, error) {
	var settings Settings
	err := db.QueryRow(ctx, `SELECT enabled, currency, minimum_minor, cooldown_days, only_referral_balance
		FROM referral_withdrawal_settings`).Scan(&settings.Enabled, &settings.Currency,
		&settings.MinimumMinor, &settings.CooldownDays, &settings.OnlyReferral)
	return settings, err
}

// SaveSettings replaces the policy and audits the change.
func (service *Service) SaveSettings(ctx context.Context, settings Settings, actor panelpg.Actor) (Settings, error) {
	settings.Currency = strings.ToUpper(strings.TrimSpace(settings.Currency))
	if len(settings.Currency) != 3 || settings.MinimumMinor <= 0 || settings.CooldownDays < 0 || settings.CooldownDays > 365 {
		return Settings{}, ErrInvalid
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE referral_withdrawal_settings SET enabled = $1, currency = $2,
			minimum_minor = $3, cooldown_days = $4, only_referral_balance = $5, updated_at = now(),
			updated_by = NULLIF($6, '')::uuid`,
		settings.Enabled, settings.Currency, settings.MinimumMinor, settings.CooldownDays, settings.OnlyReferral,
		actor.AdminID); err != nil {
		return Settings{}, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral_withdrawals.settings_saved", "configuration",
		"referral_withdrawals", "settings", map[string]any{
			"enabled": settings.Enabled, "currency": settings.Currency, "minimumMinor": settings.MinimumMinor,
			"cooldownDays": settings.CooldownDays, "onlyReferralBalance": settings.OnlyReferral,
		}); err != nil {
		return Settings{}, err
	}
	return settings, tx.Commit(ctx)
}

// earningRoles are the referral rewards that count as what referrals earned:
// the one-off invite reward and the commission on a friend's payments, a
// partner's personal rate included. Both are credited to the wallet as
// referral income; the invitee's own welcome reward is not the customer's
// earnings from referring anybody and stays out.
const earningRoles = `role IN ('inviter', 'commission')`

// balance is the money side of a summary.
func (service *Service) balance(ctx context.Context, tx pgx.Tx, customerID string, settings Settings) (earned, requested, available int64, err error) {
	if err = tx.QueryRow(ctx, `SELECT
			(SELECT COALESCE(sum(amount_minor), 0)::bigint FROM referral_rewards
				WHERE beneficiary_user_id = $1::uuid AND `+earningRoles+` AND reversed_at IS NULL
				  AND days IS NULL AND currency = $2),
			(SELECT COALESCE(sum(amount_minor), 0)::bigint FROM referral_withdrawals
				WHERE user_id = $1::uuid AND currency = $2 AND status IN ('pending', 'approved', 'paid'))`,
		customerID, settings.Currency).Scan(&earned, &requested); err != nil {
		return 0, 0, 0, err
	}
	wallet, err := walletpost.Available(ctx, tx, customerID, settings.Currency)
	if err != nil {
		return 0, 0, 0, err
	}
	return earned, requested, Available(wallet, earned, requested, settings.OnlyReferral), nil
}

// Summary reads the customer's position and their requests.
func (service *Service) Summary(ctx context.Context, customerID string) (Summary, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Settings: settings, Requests: []Request{}}
	if summary.EarnedMinor, summary.RequestedMinor, summary.AvailableMinor, err = service.balance(ctx, tx, customerID, settings); err != nil {
		return Summary{}, err
	}
	if next, err := service.nextAllowed(ctx, tx, customerID, settings); err != nil {
		return Summary{}, err
	} else if next.After(service.clock()) {
		summary.NextAllowedAt = &next
	}
	summary.Requests, err = service.list(ctx, tx, `WHERE w.user_id = $1::uuid`, 20, customerID)
	if err != nil {
		return Summary{}, err
	}
	for index := range summary.Requests {
		// The customer sees the outcome, not the operator's risk reasoning.
		summary.Requests[index].Risk = Assessment{Flags: []string{}}
		summary.Requests[index].CustomerID = ""
	}
	return summary, nil
}

func (service *Service) nextAllowed(ctx context.Context, tx pgx.Tx, customerID string, settings Settings) (time.Time, error) {
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(created_at) FROM referral_withdrawals
		WHERE user_id = $1::uuid AND status IN ('pending', 'approved', 'paid')`, customerID).Scan(&last); err != nil {
		return time.Time{}, err
	}
	if last == nil {
		return time.Time{}, nil
	}
	return last.Add(time.Duration(settings.CooldownDays) * 24 * time.Hour), nil
}

// Input is a customer's request.
type Input struct {
	AmountMinor int64
	Method      string
	Details     string
}

// RequestWithdrawal opens a request and holds its amount.
//
// It runs serializable so two requests, or a request and a purchase, racing
// for the same balance cannot both succeed; the loser fails and the client
// retries with the same idempotency key, which returns the request that won.
func (service *Service) RequestWithdrawal(ctx context.Context, customerID, idempotencyKey string, input Input) (Request, error) {
	input.Method = strings.ToLower(strings.TrimSpace(input.Method))
	input.Details = strings.TrimSpace(input.Details)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !methods[input.Method] || input.Details == "" || utf8.RuneCountInString(input.Details) > 300 ||
		input.AmountMinor <= 0 || idempotencyKey == "" || len(idempotencyKey) > 120 {
		return Request{}, ErrInvalid
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Request{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if existing, err := service.list(ctx, tx, `WHERE w.user_id = $1::uuid AND w.idempotency_key = $2`, 1,
		customerID, idempotencyKey); err != nil {
		return Request{}, err
	} else if len(existing) == 1 {
		return existing[0], tx.Commit(ctx)
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return Request{}, err
	}
	if !settings.Enabled {
		return Request{}, ErrDisabled
	}
	if input.AmountMinor < settings.MinimumMinor {
		return Request{}, ErrBelowMinimum
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM referral_withdrawals WHERE user_id = $1::uuid AND status = 'pending')`,
		customerID).Scan(&pending); err != nil {
		return Request{}, err
	}
	if pending {
		return Request{}, ErrPending
	}
	if next, err := service.nextAllowed(ctx, tx, customerID, settings); err != nil {
		return Request{}, err
	} else if next.After(service.clock()) {
		return Request{}, ErrCooldown
	}
	_, _, available, err := service.balance(ctx, tx, customerID, settings)
	if err != nil {
		return Request{}, err
	}
	if input.AmountMinor > available {
		return Request{}, ErrInsufficient
	}
	facts, err := service.facts(ctx, tx, customerID, settings)
	if err != nil {
		return Request{}, err
	}
	assessment := Assess(facts)
	evidence, err := json.Marshal(facts)
	if err != nil {
		return Request{}, err
	}
	sealed, err := service.seal(input.Details)
	if err != nil {
		return Request{}, err
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return Request{}, err
	}
	hold, err := walletpost.Post(ctx, tx, walletpost.Posting{
		Type: "debit", ReferenceType: "referral_withdrawal", ReferenceID: id,
		IdempotencyKey: "referral-withdrawal-hold:" + id, Reason: "Referral withdrawal request",
		ActorID: customerID, CustomerID: customerID, Currency: settings.Currency,
		AmountMinor: -input.AmountMinor, CheckBalance: true,
	})
	if errors.Is(err, walletpost.ErrInsufficient) {
		return Request{}, ErrInsufficient
	}
	if err != nil {
		return Request{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO referral_withdrawals (id, user_id, amount_minor, currency, payout_method,
			payout_details_ciphertext, risk_score, risk_flags, risk_evidence, hold_ledger_transaction_id, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11)`,
		id, customerID, input.AmountMinor, settings.Currency, input.Method, sealed,
		assessment.Score, assessment.Flags, evidence, hold, idempotencyKey); err != nil {
		if uniqueViolation(err) {
			return Request{}, ErrPending
		}
		return Request{}, err
	}
	created, err := service.list(ctx, tx, `WHERE w.id = $1::uuid`, 1, id)
	if err != nil || len(created) != 1 {
		return Request{}, errors.Join(err, ErrNotFound)
	}
	return created[0], tx.Commit(ctx)
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// facts gathers what the risk score needs.
func (service *Service) facts(ctx context.Context, tx pgx.Tx, customerID string, settings Settings) (Facts, error) {
	facts := Facts{MinimumMinor: settings.MinimumMinor}
	// Commission counts towards what was earned and towards the largest single
	// source, which is what those signals measure. The burst counts invite
	// rewards only: a partner with many paying friends is paid a commission row
	// per payment, and that is the programme working, not a burst of sign-ups.
	err := tx.QueryRow(ctx, `WITH rewards AS (
			SELECT referred_user_id, amount_minor, granted_at, role FROM referral_rewards
			WHERE beneficiary_user_id = $1::uuid AND `+earningRoles+` AND reversed_at IS NULL
			  AND days IS NULL AND currency = $2
		), referrals AS (
			SELECT a.referred_user_id, a.review_state,
				EXISTS (SELECT 1 FROM orders o WHERE o.user_id = a.referred_user_id AND o.paid_minor > 0
					AND o.state IN ('paid', 'fulfilled', 'partially_refunded')) AS paying,
				EXISTS (SELECT 1 FROM referral_signals s WHERE s.referred_user_id = a.referred_user_id) AS signalled
			FROM referral_attributions a WHERE a.referrer_user_id = $1::uuid
		)
		SELECT
			(SELECT COALESCE(sum(amount_minor), 0)::bigint FROM rewards),
			(SELECT count(*)::integer FROM referrals),
			(SELECT count(*)::integer FROM referrals WHERE paying),
			(SELECT COALESCE(max(total), 0)::bigint FROM (SELECT sum(amount_minor) AS total FROM rewards GROUP BY referred_user_id) s),
			(SELECT count(*)::integer FROM rewards WHERE role = 'inviter'
				AND granted_at > now() - interval '7 days'),
			(SELECT COALESCE(sum(credited_minor), 0)::bigint FROM wallet_topups
				WHERE user_id = $1::uuid AND currency = $2 AND credited_at IS NOT NULL),
			(SELECT count(*)::integer FROM orders WHERE user_id = $1::uuid
				AND state IN ('paid', 'fulfilled', 'partially_refunded')
				AND operation IN ('purchase', 'extension', 'renewal', 'upgrade', 'downgrade')),
			(SELECT GREATEST(0, extract(day FROM now() - created_at))::integer FROM users WHERE id = $1::uuid),
			(SELECT count(*)::integer FROM referrals WHERE review_state IN ('held', 'rejected')),
			(SELECT count(*)::integer FROM referrals WHERE signalled)`,
		customerID, settings.Currency).Scan(&facts.EarnedMinor, &facts.Referrals, &facts.PayingReferrals,
		&facts.LargestSourceMinor, &facts.RewardsLastWeek, &facts.OwnTopUpsMinor, &facts.OwnPurchases,
		&facts.AccountAgeDays, &facts.HeldAttributions, &facts.SignalledReferrals)
	return facts, err
}

// Cancel withdraws the customer's own pending request and releases the hold.
func (service *Service) Cancel(ctx context.Context, customerID, requestID string) error {
	return service.release(ctx, requestID, customerID, "cancelled", "", panelpg.Actor{})
}

// List reads requests for the operator, newest first, optionally by status.
func (service *Service) List(ctx context.Context, status string, limit int) ([]Request, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if status != "" {
		return service.list(ctx, service.pool, `WHERE w.status = $1`, limit, status)
	}
	return service.list(ctx, service.pool, ``, limit)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (service *Service) list(ctx context.Context, db querier, where string, limit int, args ...any) ([]Request, error) {
	rows, err := db.Query(ctx, `SELECT w.id::text, w.user_id::text, w.amount_minor, w.currency, w.status, w.payout_method,
			w.risk_score, w.risk_flags, COALESCE(w.decision_note, ''), w.created_at, w.decided_at, w.paid_at
		FROM referral_withdrawals w `+where+` ORDER BY w.created_at DESC LIMIT `+itoa(limit), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Request, error) {
		var request Request
		err := row.Scan(&request.ID, &request.CustomerID, &request.AmountMinor, &request.Currency, &request.Status,
			&request.Method, &request.Risk.Score, &request.Risk.Flags, &request.Note, &request.CreatedAt,
			&request.DecidedAt, &request.PaidAt)
		request.Risk.Level = level(request.Risk.Score)
		if request.Risk.Flags == nil {
			request.Risk.Flags = []string{}
		}
		return request, err
	})
}

func itoa(value int) string {
	digits := []byte{}
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// RevealDetails unseals the payout details for an operator and audits that
// they were seen: a card number is personal data, and who looked at it is a
// question that gets asked.
func (service *Service) RevealDetails(ctx context.Context, requestID string, actor panelpg.Actor) (string, error) {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sealed []byte
	if err = tx.QueryRow(ctx, `SELECT payout_details_ciphertext FROM referral_withdrawals WHERE id = $1::uuid`,
		requestID).Scan(&sealed); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	details, err := service.open(sealed)
	if err != nil {
		return "", err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral_withdrawal.details_revealed", "financial",
		"referral_withdrawal", requestID, nil); err != nil {
		return "", err
	}
	return details, tx.Commit(ctx)
}

// Approve accepts a pending request. The hold stays: the money has left the
// customer's wallet and is waiting to be paid out.
func (service *Service) Approve(ctx context.Context, requestID, note string, actor panelpg.Actor) error {
	return service.transition(ctx, requestID, "pending", "approved", note, actor)
}

// Reject refuses a pending request and returns the held amount.
func (service *Service) Reject(ctx context.Context, requestID, note string, actor panelpg.Actor) error {
	if strings.TrimSpace(note) == "" {
		return ErrInvalid
	}
	return service.release(ctx, requestID, "", "rejected", note, actor)
}

// MarkPaid records that an approved request was paid out.
func (service *Service) MarkPaid(ctx context.Context, requestID, note string, actor panelpg.Actor) error {
	return service.transition(ctx, requestID, "approved", "paid", note, actor)
}

func (service *Service) transition(ctx context.Context, requestID, from, to, note string, actor panelpg.Actor) error {
	if utf8.RuneCountInString(note) > 400 {
		return ErrInvalid
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var customerID, currency string
	var amount int64
	err = tx.QueryRow(ctx, `UPDATE referral_withdrawals SET status = $2,
			decided_at = CASE WHEN $2 = 'approved' THEN now() ELSE decided_at END,
			decided_by = CASE WHEN $2 = 'approved' THEN NULLIF($4, '')::uuid ELSE decided_by END,
			paid_at = CASE WHEN $2 = 'paid' THEN now() ELSE paid_at END,
			paid_by = CASE WHEN $2 = 'paid' THEN NULLIF($4, '')::uuid ELSE paid_by END,
			decision_note = COALESCE(NULLIF($5, ''), decision_note)
		WHERE id = $1::uuid AND status = $3
		RETURNING user_id::text, currency, amount_minor`, requestID, to, from, actor.AdminID, strings.TrimSpace(note)).
		Scan(&customerID, &currency, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.missingOrState(ctx, requestID)
	}
	if err != nil {
		return err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral_withdrawal."+to, "financial",
		"referral_withdrawal", requestID, map[string]any{"amountMinor": amount, "currency": currency, "customerId": customerID}); err != nil {
		return err
	}
	service.notify(ctx, tx, customerID, requestID, to, amount, currency, note)
	return tx.Commit(ctx)
}

// release ends a pending request without a payout and credits the hold back.
// customerID, when set, restricts it to that customer's own request.
func (service *Service) release(ctx context.Context, requestID, customerID, to, note string, actor panelpg.Actor) error {
	if utf8.RuneCountInString(note) > 400 {
		return ErrInvalid
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var owner, currency string
	var amount int64
	err = tx.QueryRow(ctx, `SELECT user_id::text, currency, amount_minor FROM referral_withdrawals
		WHERE id = $1::uuid AND status = 'pending' AND ($2 = '' OR user_id = NULLIF($2, '')::uuid) FOR UPDATE`,
		requestID, customerID).Scan(&owner, &currency, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		if customerID != "" {
			return ErrNotFound
		}
		return service.missingOrState(ctx, requestID)
	}
	if err != nil {
		return err
	}
	actorID := actor.AdminID
	if actorID == "" {
		actorID = owner
	}
	released, err := walletpost.Post(ctx, tx, walletpost.Posting{
		Type: "credit", ReferenceType: "referral_withdrawal", ReferenceID: requestID,
		IdempotencyKey: "referral-withdrawal-release:" + requestID, Reason: "Referral withdrawal " + to,
		ActorID: actorID, CustomerID: owner, Currency: currency, AmountMinor: amount,
	})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE referral_withdrawals SET status = $2, decided_at = now(),
			decided_by = NULLIF($3, '')::uuid, decision_note = NULLIF($4, ''), release_ledger_transaction_id = $5::uuid
		WHERE id = $1::uuid`, requestID, to, actor.AdminID, strings.TrimSpace(note), released); err != nil {
		return err
	}
	if to == "rejected" {
		if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral_withdrawal.rejected", "financial",
			"referral_withdrawal", requestID, map[string]any{"amountMinor": amount, "currency": currency, "customerId": owner}); err != nil {
			return err
		}
		service.notify(ctx, tx, owner, requestID, to, amount, currency, note)
	}
	return tx.Commit(ctx)
}

func (service *Service) missingOrState(ctx context.Context, requestID string) error {
	var exists bool
	if err := service.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM referral_withdrawals WHERE id = $1::uuid)`,
		requestID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrState
}

// notify queues an e-mail about the decision when the customer has an
// address. A failure to queue never blocks the decision itself.
func (service *Service) notify(ctx context.Context, tx pgx.Tx, customerID, requestID, status string, amount int64, currency, note string) {
	if service.email == nil {
		return
	}
	if _, err := service.email.QueueForCustomer(ctx, tx, customerID, emailpg.KindWithdrawal,
		"withdrawal:"+requestID+":"+status, func(address, locale string) mailer.Message {
			return mailer.WithdrawalUpdate(address, locale, status, payments.FormatAmount(amount, currency), note)
		}); err != nil {
		service.logger.Warn("withdrawal e-mail was not queued", "error", err)
	}
}

func (service *Service) seal(plaintext string) ([]byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.aead.Seal(nonce, nonce, []byte(plaintext), []byte("referral_withdrawal")), nil
}

func (service *Service) open(sealed []byte) (string, error) {
	size := service.aead.NonceSize()
	if len(sealed) <= size {
		return "", errors.New("sealed payout details are truncated")
	}
	plaintext, err := service.aead.Open(nil, sealed[:size], sealed[size:], []byte("referral_withdrawal"))
	return string(plaintext), err
}
