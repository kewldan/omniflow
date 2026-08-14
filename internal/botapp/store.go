package botapp

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/connectpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

var ErrNotLinked = errors.New("telegram account is not linked")

type IdentityStore interface {
	RemnawaveUserID(context.Context, int64) (int64, error)
	Link(context.Context, int64, int64) (int64, error)
}

type Preferences struct {
	Locale               string
	ExpiryNotifications  bool
	TrafficNotifications bool
}

type Store interface {
	IdentityStore
	Preferences(context.Context, int64) (Preferences, error)
	SetLocale(context.Context, int64, string) error
	ToggleNotification(context.Context, int64, string) error
	BeginSupport(context.Context, int64) error
	CancelSession(context.Context, int64) error
	Session(context.Context, int64) (string, error)
	SubmitSupport(context.Context, int64, int, string) error
	Referral(context.Context, int64) (string, int64, error)
	AttributeReferral(context.Context, int64, string) error

	// The connection catalogue. It is on this interface rather than on the
	// optional commerce store because the connect screen predates commerce: a
	// bot running only the v0.2 self-service surface still tells a customer
	// which application to install.
	ConnectPlatforms(ctx context.Context, locale string) ([]commerce.ConnectPlatform, error)
	ConnectClients(ctx context.Context, platform, locale string) ([]commerce.ClientApp, error)
}

func (store *PostgresStore) Link(ctx context.Context, telegramID, remnawaveID int64) (int64, error) {
	linkedID, err := store.queries.LinkTelegramRemnawaveUser(ctx, dbgen.LinkTelegramRemnawaveUserParams{
		RemnawaveID: remnawaveID,
		TelegramID:  telegramID,
	})
	if err != nil {
		return 0, fmt.Errorf("persist Telegram identity link: %w", err)
	}
	return linkedID, nil
}

type PostgresStore struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
	// connect is the operator's connection guidance, read through the same
	// package the customer web panel reads it through so the two surfaces
	// cannot recommend different applications.
	connect *connectpg.Catalogue
}

// ConnectPlatforms lists the platforms this installation documents.
func (store *PostgresStore) ConnectPlatforms(
	ctx context.Context, locale string,
) ([]commerce.ConnectPlatform, error) {
	return store.connect.Platforms(ctx, locale)
}

// ConnectClients lists the applications documented for one platform.
func (store *PostgresStore) ConnectClients(
	ctx context.Context, platform, locale string,
) ([]commerce.ClientApp, error) {
	return store.connect.Clients(ctx, platform, locale)
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	if databaseURL == "" {
		return nil, errors.New("database URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open bot database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping bot database: %w", err)
	}
	return &PostgresStore{
		pool: pool, queries: dbgen.New(pool), connect: connectpg.New(pool),
	}, nil
}

func (store *PostgresStore) Close() {
	store.pool.Close()
}

func (store *PostgresStore) RemnawaveUserID(ctx context.Context, telegramID int64) (int64, error) {
	userID, err := store.queries.GetRemnawaveUserIDByTelegramID(ctx, pgtype.Int8{Int64: telegramID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotLinked
	}
	if err != nil {
		return 0, fmt.Errorf("lookup Telegram identity: %w", err)
	}
	return userID, nil
}

func (store *PostgresStore) Preferences(ctx context.Context, telegramID int64) (Preferences, error) {
	var preferences Preferences
	err := store.pool.QueryRow(ctx, `
		SELECT COALESCE(p.locale, 'auto'), COALESCE(p.expiry_notifications, true),
		       COALESCE(p.traffic_notifications, true)
		FROM remnawave_users r
		LEFT JOIN bot_preferences p ON p.user_id = r.user_id
		WHERE r.telegram_id = $1`, telegramID).Scan(
		&preferences.Locale, &preferences.ExpiryNotifications, &preferences.TrafficNotifications,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preferences{Locale: "auto", ExpiryNotifications: true, TrafficNotifications: true}, ErrNotLinked
	}
	return preferences, err
}

func (store *PostgresStore) SetLocale(ctx context.Context, telegramID int64, locale string) error {
	if locale != "auto" && locale != "ru" && locale != "en" {
		return errors.New("unsupported locale")
	}
	return store.upsertPreference(ctx, telegramID, "locale", locale)
}

func (store *PostgresStore) ToggleNotification(ctx context.Context, telegramID int64, kind string) error {
	column := "expiry_notifications"
	if kind == "traffic" {
		column = "traffic_notifications"
	} else if kind != "expiry" {
		return errors.New("unsupported notification kind")
	}
	command := `INSERT INTO bot_preferences (user_id, ` + column + `)
		SELECT user_id, false FROM remnawave_users WHERE telegram_id = $1
		ON CONFLICT (user_id) DO UPDATE SET ` + column + ` = NOT bot_preferences.` + column + `, updated_at = now()`
	result, err := store.pool.Exec(ctx, command, telegramID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotLinked
	}
	return err
}

func (store *PostgresStore) upsertPreference(ctx context.Context, telegramID int64, column, value string) error {
	command := `INSERT INTO bot_preferences (user_id, ` + column + `)
		SELECT user_id, $2 FROM remnawave_users WHERE telegram_id = $1
		ON CONFLICT (user_id) DO UPDATE SET ` + column + ` = EXCLUDED.` + column + `, updated_at = now()`
	result, err := store.pool.Exec(ctx, command, telegramID, value)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotLinked
	}
	return err
}

func (store *PostgresStore) BeginSupport(ctx context.Context, telegramID int64) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO bot_sessions (telegram_id, state)
		VALUES ($1, 'support_message') ON CONFLICT (telegram_id) DO UPDATE
		SET state = EXCLUDED.state, updated_at = now(), expires_at = now() + interval '30 minutes'`, telegramID)
	return err
}

func (store *PostgresStore) CancelSession(ctx context.Context, telegramID int64) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM bot_sessions WHERE telegram_id = $1`, telegramID)
	return err
}

func (store *PostgresStore) Session(ctx context.Context, telegramID int64) (string, error) {
	var state string
	if _, err := store.pool.Exec(ctx, `DELETE FROM bot_sessions WHERE telegram_id = $1 AND expires_at <= now()`, telegramID); err != nil {
		return "", err
	}
	err := store.pool.QueryRow(ctx, `SELECT state FROM bot_sessions WHERE telegram_id = $1`, telegramID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return state, err
}

func (store *PostgresStore) SubmitSupport(ctx context.Context, telegramID int64, messageID int, body string) error {
	body = strings.TrimSpace(body)
	if body == "" || len([]rune(body)) > 4000 {
		return errors.New("support message must contain 1 to 4000 characters")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ticketID string
	// A customer may hold several open tickets since v0.8, so this continues the
	// most recent one rather than relying on a uniqueness constraint that no
	// longer exists.
	err = tx.QueryRow(ctx, `WITH account AS (
		SELECT user_id FROM remnawave_users WHERE telegram_id = $1
	), existing AS (
		SELECT t.id FROM support_tickets t
		JOIN account a ON a.user_id = t.user_id
		WHERE t.status IN ('open', 'pending')
		ORDER BY t.last_message_at DESC LIMIT 1
	), created AS (
		INSERT INTO support_tickets (user_id, queue_id)
		SELECT a.user_id,
		       (SELECT id FROM support_queues WHERE is_default AND archived_at IS NULL)
		FROM account a
		WHERE NOT EXISTS (SELECT 1 FROM existing)
		RETURNING id
	)
	SELECT id::text FROM existing
	UNION ALL
	SELECT id::text FROM created`, telegramID).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotLinked
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO support_messages (ticket_id, sender, body, telegram_message_id)
		VALUES ($1::uuid, 'customer', $2, $3)`, ticketID, body, messageID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM bot_sessions WHERE telegram_id = $1`, telegramID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *PostgresStore) Referral(ctx context.Context, telegramID int64) (string, int64, error) {
	code, err := newReferralCode()
	if err != nil {
		return "", 0, err
	}
	var saved string
	err = store.pool.QueryRow(ctx, `INSERT INTO referral_codes (user_id, code)
		SELECT user_id, $2 FROM remnawave_users WHERE telegram_id = $1
		ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING code`, telegramID, code).Scan(&saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNotLinked
	}
	if err != nil {
		return "", 0, err
	}
	var count int64
	err = store.pool.QueryRow(ctx, `SELECT count(*) FROM referral_attributions a
		JOIN referral_codes c ON c.user_id = a.referrer_user_id WHERE c.code = $1`, saved).Scan(&count)
	return saved, count, err
}

func (store *PostgresStore) AttributeReferral(ctx context.Context, telegramID int64, code string) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		SELECT referred.user_id, code_owner.user_id, code_owner.code
		FROM remnawave_users referred JOIN referral_codes code_owner ON code_owner.code = $2
		WHERE referred.telegram_id = $1 AND referred.user_id <> code_owner.user_id
		ON CONFLICT (referred_user_id) DO NOTHING`, telegramID, strings.ToUpper(strings.TrimSpace(code)))
	return err
}

func newReferralCode() (string, error) {
	buffer := make([]byte, 7)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)[:10], nil
}
