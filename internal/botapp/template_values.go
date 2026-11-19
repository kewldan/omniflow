package botapp

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

// templateValues fills the broadcast variables for one recipient. The values
// are plain text; escapedValues prepares them for an HTML body, because a first
// name is the customer's own text and must never be read as markup.
//
// It is one read per recipient. A campaign batch is bounded, and a value read
// at send time is the value the customer has now, not when the audience was
// expanded.
func (store *PostgresStore) templateValues(
	ctx context.Context, customerID string, locale Locale, currency, botUsername string, now time.Time,
) (map[string]string, error) {
	var (
		firstName, planName, referralCode string
		tierEN, tierRU                    string
		endsAt                            pgtype.Timestamptz
		balance                           int64
		discountBPS                       int32
	)
	err := store.pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT i.metadata->>'firstName' FROM identities i
			WHERE i.user_id = u.id AND i.provider = 'telegram' AND i.status = 'active'
			ORDER BY i.created_at DESC LIMIT 1), ''),
		latest.ends_at,
		COALESCE(latest.plan_name, ''),
		COALESCE((SELECT sum(l.amount_minor) FROM ledger_entries l
			WHERE l.account_type = 'customer_wallet' AND l.user_id = u.id AND l.currency = $3), 0)::bigint,
		COALESCE((SELECT c.code FROM referral_codes c WHERE c.user_id = u.id), ''),
		COALESCE(tier.name_en, ''), COALESCE(tier.name_ru, ''), COALESCE(tier.discount_bps, 0)
		FROM users u
		LEFT JOIN LATERAL (
			SELECT e.ends_at, COALESCE(pl.name, pl_en.name) AS plan_name
			FROM entitlements e
			JOIN plan_versions pv ON pv.id = e.plan_version_id
			LEFT JOIN plan_localizations pl ON pl.plan_id = pv.plan_id AND pl.locale = $2
			LEFT JOIN plan_localizations pl_en ON pl_en.plan_id = pv.plan_id AND pl_en.locale = 'en'
			WHERE e.user_id = u.id AND e.status IN ('active', 'limited', 'paused', 'expired', 'disabled')
			ORDER BY e.ends_at DESC LIMIT 1
		) latest ON true
		LEFT JOIN LATERAL (
			SELECT t.name_en, t.name_ru, t.discount_bps
			FROM loyalty_standings s
			JOIN loyalty_tiers st ON st.id = s.tier_id
			JOIN loyalty_tiers t ON t.code = st.code
			JOIN loyalty_programs p ON p.id = t.program_id AND p.enabled
			WHERE s.user_id = u.id LIMIT 1
		) tier ON true
		WHERE u.id = $1::uuid`, customerID, string(locale), currency).Scan(
		&firstName, &endsAt, &planName, &balance, &referralCode, &tierEN, &tierRU, &discountBPS)
	if err != nil {
		return nil, err
	}
	if referralCode == "" && botUsername != "" {
		// A customer who never opened their referral screen has no code yet.
		// Minting one here is what lets {{referral_link}} work for everybody.
		if code, codeErr := newReferralCode(); codeErr == nil {
			_ = store.pool.QueryRow(ctx, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, $2)
				ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id RETURNING code`,
				customerID, code).Scan(&referralCode)
		}
	}
	name := strings.TrimSpace(firstName)
	if name == "" {
		name = text(locale, "template.friend")
	}
	values := map[string]string{
		"name":          name,
		"days_left":     "0",
		"plan":          planName,
		"expires_at":    "",
		"balance":       formatMoney(balance, currency),
		"referral_code": referralCode,
		"referral_link": referralURL(botUsername, referralCode),
		"loyalty_tier":  tierEN,
	}
	if locale == LocaleRussian {
		values["loyalty_tier"] = tierRU
	}
	if discountBPS > 0 {
		values["loyalty_discount"] = percentLabel(int(discountBPS))
	}
	if endsAt.Valid {
		values["expires_at"] = endsAt.Time.UTC().Format("2006-01-02")
		if remaining := endsAt.Time.Sub(now); remaining > 0 {
			values["days_left"] = strconv.Itoa(int(remaining.Hours() / 24))
		}
	}
	return values, nil
}

// escapedValues prepares template values for an HTML message body.
func escapedValues(values map[string]string) map[string]string {
	escaped := make(map[string]string, len(values))
	for name, value := range values {
		escaped[name] = html.EscapeString(value)
	}
	return escaped
}

// RememberTelegramName keeps the customer's Telegram first name on their
// Telegram identity, so a broadcast can greet them by it. Only the first name is
// kept, and only as long as the identity is.
func (store *PostgresStore) RememberTelegramName(ctx context.Context, telegramID int64, firstName string) error {
	firstName = strings.TrimSpace(firstName)
	for utf8.RuneCountInString(firstName) > 64 {
		_, size := utf8.DecodeLastRuneInString(firstName)
		firstName = firstName[:len(firstName)-size]
	}
	if firstName == "" {
		return nil
	}
	_, err := store.pool.Exec(ctx, `UPDATE identities
		SET metadata = jsonb_set(metadata, '{firstName}', to_jsonb($2::text))
		WHERE provider = 'telegram' AND provider_subject = $1 AND status = 'active'
		  AND metadata->>'firstName' IS DISTINCT FROM $2`, strconv.FormatInt(telegramID, 10), firstName)
	return err
}
