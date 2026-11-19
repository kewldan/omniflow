package botapp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// WinbackWave is one automatic "come back" message with a time-limited
// discount, the way Bedolaga sends its second and third expiry waves: by
// default 10% for 24 hours two days after the subscription ended, then 20%
// for 24 hours on the fifth day.
type WinbackWave struct {
	Number    int
	AfterDays int
	Percent   int
	ValidFor  time.Duration
}

// WinbackWavesFromEnv reads APP_WINBACK_*; APP_WINBACK_ENABLED=false or a
// zero percent turns a wave off.
func WinbackWavesFromEnv() []WinbackWave {
	if value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("APP_WINBACK_ENABLED"))); err == nil && !value {
		return nil
	}
	read := func(key string, fallback int) int {
		value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
		if err != nil {
			return fallback
		}
		return value
	}
	valid := time.Duration(read("APP_WINBACK_VALID_HOURS", 24)) * time.Hour
	waves := []WinbackWave{
		{Number: 2, AfterDays: read("APP_WINBACK_WAVE2_AFTER_DAYS", 2), Percent: read("APP_WINBACK_WAVE2_PERCENT", 10), ValidFor: valid},
		{Number: 3, AfterDays: read("APP_WINBACK_WAVE3_AFTER_DAYS", 5), Percent: read("APP_WINBACK_WAVE3_PERCENT", 20), ValidFor: valid},
	}
	enabled := waves[:0]
	for _, wave := range waves {
		if wave.Percent > 0 && wave.Percent <= 100 && wave.AfterDays > 0 && wave.ValidFor > 0 {
			enabled = append(enabled, wave)
		}
	}
	return enabled
}

// dueWinbackWave picks the wave an expired subscription is in now: the latest
// one whose day has come, as long as the next one has not. A wave is due for
// two days after its start, so a missed notifier pass still sends it.
func dueWinbackWave(waves []WinbackWave, endedAt, now time.Time) (WinbackWave, bool) {
	since := now.Sub(endedAt)
	var due WinbackWave
	found := false
	for _, wave := range waves {
		start := time.Duration(wave.AfterDays) * 24 * time.Hour
		if since >= start && since < start+48*time.Hour {
			due, found = wave, true
		}
	}
	return due, found
}

// IssueWinbackOffer creates the customer's personal discount for one wave of
// one lapsed entitlement — a single-use promo code valid for the wave's
// window, published as a personal offer so the bot and the web checkout apply
// it automatically. It is idempotent per wave and entitlement.
func (store *PostgresStore) IssueWinbackOffer(
	ctx context.Context, customerID, entitlementID string, wave WinbackWave,
) (code string, expiresAt time.Time, err error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	slug := fmt.Sprintf("wb%d-%s", wave.Number, strings.ReplaceAll(entitlementID, "-", ""))
	var promotionID string
	err = tx.QueryRow(ctx, `INSERT INTO promotions (code, kind, value, per_customer_limit, redemption_limit, starts_at, ends_at, applies_to)
		VALUES ($1, 'percent', $2, 1, 1, now(), now() + $3::interval, 'plans')
		ON CONFLICT (code) DO NOTHING RETURNING id::text`,
		slug, wave.Percent*100, fmt.Sprintf("%d seconds", int(wave.ValidFor.Seconds()))).Scan(&promotionID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already issued for this wave: hand back the same code.
		err = tx.QueryRow(ctx, `SELECT c.normalized_code, p.ends_at FROM promotions p
			JOIN promo_codes c ON c.promotion_id = p.id WHERE p.code = $1 LIMIT 1`, slug).Scan(&code, &expiresAt)
		return code, expiresAt, err
	}
	if err != nil {
		return "", time.Time{}, err
	}
	code = fmt.Sprintf("BACK%d-%s", wave.Percent, randomCode(6))
	if _, err = tx.Exec(ctx, `INSERT INTO promo_codes (promotion_id, normalized_code) VALUES ($1::uuid, $2)`, promotionID, code); err != nil {
		return "", time.Time{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO personal_offers (user_id, promotion_id, title_ru, title_en, terms_ru, terms_en, expires_at)
		SELECT $1::uuid, $2::uuid, $3, $4, $5, $6, ends_at FROM promotions WHERE id = $2::uuid
		RETURNING expires_at`,
		customerID, promotionID,
		fmt.Sprintf("Скидка %d%% на продление", wave.Percent), fmt.Sprintf("%d%% off your renewal", wave.Percent),
		"Применяется автоматически при оплате, один раз.", "Applied automatically at checkout, once.",
	).Scan(&expiresAt)
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, tx.Commit(ctx)
}

const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

func randomCode(length int) string {
	buffer := make([]byte, length)
	_, _ = rand.Read(buffer)
	for index := range buffer {
		buffer[index] = codeAlphabet[int(buffer[index])%len(codeAlphabet)]
	}
	return string(buffer)
}

func winbackView(locale Locale, entitlement Entitlement, wave WinbackWave, code string, expiresAt time.Time) View {
	hours := int(time.Until(expiresAt).Round(time.Hour).Hours())
	if hours < 1 {
		hours = 1
	}
	return View{
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) +
			text(locale, "winback.body", wave.Percent, hours, code),
		Keyboard: keyboard(
			row(actionButton(text(locale, "winback.renew", wave.Percent), "buy:"+entitlement.PlanVersionID+":purchase")),
			row(callbackButton(text(locale, "menu.plans"), routePlans)),
		),
	}
}
