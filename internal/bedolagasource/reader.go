// Package bedolagasource reads a Bedolaga bot database for the importer.
//
// It only ever reads. Every query runs inside one REPEATABLE READ, READ ONLY
// transaction, so the importer sees a single consistent snapshot even while
// the bot is still running during a rehearsal, and PostgreSQL itself refuses
// any write that a bug here might attempt. Point it at a role that has only
// SELECT on the bot database and the guarantee holds twice.
package bedolagasource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
)

// requiredColumns are the columns the importer cannot do without. Bedolaga
// gained numeric Remnawave identifiers with Remnawave 3.0; a database older
// than that must be upgraded in Bedolaga before it can be migrated.
var requiredColumns = map[string][]string{
	"users": {
		"id", "telegram_id", "username", "first_name", "last_name", "status", "language",
		"balance_kopeks", "email", "email_verified", "email_verified_at", "referred_by_id",
		"referral_code", "has_had_paid_subscription", "remnawave_id", "created_at",
	},
	"subscriptions": {
		"id", "user_id", "status", "is_trial", "start_date", "end_date", "tariff_id",
		"remnawave_id", "created_at",
	},
	"tariffs": {"id", "name", "is_active"},
}

// Read opens a read-only snapshot of the Bedolaga database and loads every
// record the importer decides about, plus the counts of what stays behind.
func Read(ctx context.Context, connection *pgx.Conn) (bedolagaimport.Source, error) {
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return bedolagaimport.Source{}, fmt.Errorf("open a read-only snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	columns, err := loadColumns(ctx, tx)
	if err != nil {
		return bedolagaimport.Source{}, err
	}
	for table, names := range requiredColumns {
		for _, name := range names {
			if !columns[table][name] {
				return bedolagaimport.Source{}, fmt.Errorf("the Bedolaga database has no %s.%s; upgrade Bedolaga to v5 before migrating", table, name)
			}
		}
	}
	source := bedolagaimport.Source{}
	if source.Users, err = readUsers(ctx, tx); err != nil {
		return bedolagaimport.Source{}, err
	}
	if err = readOAuthLinks(ctx, tx, columns, source.Users); err != nil {
		return bedolagaimport.Source{}, err
	}
	if source.Subscriptions, err = readSubscriptions(ctx, tx); err != nil {
		return bedolagaimport.Source{}, err
	}
	if source.Tariffs, err = readTariffs(ctx, tx); err != nil {
		return bedolagaimport.Source{}, err
	}
	if source.Payments, err = readPayments(ctx, tx, columns); err != nil {
		return bedolagaimport.Source{}, err
	}
	gifts, unclaimed, err := readGifts(ctx, tx, columns)
	if err != nil {
		return bedolagaimport.Source{}, err
	}
	source.Payments = append(source.Payments, gifts...)
	source.Gifts = unclaimed
	if source.Discounts, err = readDiscounts(ctx, tx, columns); err != nil {
		return bedolagaimport.Source{}, err
	}
	if source.Archive, err = readArchive(ctx, tx, columns); err != nil {
		return bedolagaimport.Source{}, err
	}
	return source, nil
}

func loadColumns(ctx context.Context, tx pgx.Tx) (map[string]map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema()`)
	if err != nil {
		return nil, fmt.Errorf("read the Bedolaga schema: %w", err)
	}
	defer rows.Close()
	columns := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		if columns[table] == nil {
			columns[table] = map[string]bool{}
		}
		columns[table][column] = true
	}
	return columns, rows.Err()
}

func readUsers(ctx context.Context, tx pgx.Tx) ([]bedolagaimport.User, error) {
	rows, err := tx.Query(ctx, `SELECT id, telegram_id, COALESCE(username, ''), COALESCE(first_name, ''),
		COALESCE(last_name, ''), COALESCE(status, 'active'), COALESCE(language, ''),
		COALESCE(balance_kopeks, 0)::bigint, COALESCE(email, ''), COALESCE(email_verified, false),
		email_verified_at, referred_by_id, COALESCE(referral_code, ''),
		COALESCE(has_had_paid_subscription, false), remnawave_id, COALESCE(created_at, now())
		FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (bedolagaimport.User, error) {
		var user bedolagaimport.User
		var referredBy *int32
		err := row.Scan(&user.ID, &user.TelegramID, &user.Username, &user.FirstName, &user.LastName,
			&user.Status, &user.Language, &user.BalanceKopeks, &user.Email, &user.EmailVerified,
			&user.EmailVerifiedAt, &referredBy, &user.ReferralCode, &user.HasPaid, &user.RemnawaveID,
			&user.CreatedAt)
		if referredBy != nil {
			id := int64(*referredBy)
			user.ReferredByID = &id
		}
		user.CreatedAt = user.CreatedAt.UTC()
		return user, err
	})
}

// readOAuthLinks attaches the cabinet sign-in links Bedolaga stores as
// google_id, yandex_id, discord_id, and vk_id, reading only the columns this
// Bedolaga version has.
func readOAuthLinks(ctx context.Context, tx pgx.Tx, columns map[string]map[string]bool, users []bedolagaimport.User) error {
	slugs := []string{}
	selects := []string{"id"}
	for _, slug := range []string{"google", "yandex", "discord", "vk"} {
		if columns["users"][slug+"_id"] {
			slugs = append(slugs, slug)
			selects = append(selects, "COALESCE("+slug+"_id::text, '')")
		}
	}
	if len(slugs) == 0 {
		return nil
	}
	index := make(map[int64]int, len(users))
	for position, user := range users {
		index[user.ID] = position
	}
	rows, err := tx.Query(ctx, "SELECT "+strings.Join(selects, ", ")+" FROM users")
	if err != nil {
		return fmt.Errorf("read OAuth links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]string, len(slugs))
		targets := []any{new(int64)}
		for position := range values {
			targets = append(targets, &values[position])
		}
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		position, ok := index[*targets[0].(*int64)]
		if !ok {
			continue
		}
		for slot, slug := range slugs {
			if subject := strings.TrimSpace(values[slot]); subject != "" {
				if users[position].OAuth == nil {
					users[position].OAuth = map[string]string{}
				}
				users[position].OAuth[slug] = subject
			}
		}
	}
	return rows.Err()
}

func readSubscriptions(ctx context.Context, tx pgx.Tx) ([]bedolagaimport.Subscription, error) {
	rows, err := tx.Query(ctx, `SELECT id, user_id, COALESCE(status, 'trial'), COALESCE(is_trial, false),
		COALESCE(start_date, created_at, now()), end_date, tariff_id, remnawave_id, COALESCE(created_at, now())
		FROM subscriptions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read subscriptions: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (bedolagaimport.Subscription, error) {
		var subscription bedolagaimport.Subscription
		var id, userID int32
		var tariffID *int32
		err := row.Scan(&id, &userID, &subscription.Status, &subscription.IsTrial, &subscription.StartDate,
			&subscription.EndDate, &tariffID, &subscription.RemnawaveID, &subscription.CreatedAt)
		subscription.ID, subscription.UserID = int64(id), int64(userID)
		if tariffID != nil {
			tariff := int64(*tariffID)
			subscription.TariffID = &tariff
		}
		subscription.StartDate = subscription.StartDate.UTC()
		subscription.EndDate = subscription.EndDate.UTC()
		return subscription, err
	})
}

func readTariffs(ctx context.Context, tx pgx.Tx) ([]bedolagaimport.Tariff, error) {
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(name, ''), COALESCE(is_active, false) FROM tariffs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read tariffs: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (bedolagaimport.Tariff, error) {
		var tariff bedolagaimport.Tariff
		var id int32
		err := row.Scan(&id, &tariff.Name, &tariff.IsActive)
		tariff.ID = int64(id)
		return tariff, err
	})
}

// readPayments loads every completed VPN purchase Bedolaga recorded as a
// transaction: subscription payments, from the balance or through a provider,
// and gifts paid from the balance. Top-ups are not read: in the Omniflow fork
// the balance buys Telegram Stars too, so a top-up is not VPN spend. A Bedolaga without a transactions table has no
// payment history to carry; the archive reports the table as missing. The
// moment a payment completed is completed_at where this Bedolaga records it,
// and its creation otherwise.
func readPayments(ctx context.Context, tx pgx.Tx, columns map[string]map[string]bool) ([]bedolagaimport.Payment, error) {
	table := columns["transactions"]
	for _, name := range []string{"id", "user_id", "type", "amount_kopeks", "created_at"} {
		if !table[name] {
			return nil, nil
		}
	}
	method, completed, paidAt := "''", "true", "created_at"
	if table["payment_method"] {
		method = "COALESCE(payment_method, '')"
	}
	if table["is_completed"] {
		completed = "COALESCE(is_completed, true)"
	}
	if table["completed_at"] {
		paidAt = "COALESCE(completed_at, created_at)"
	}
	// Bedolaga stores some purchase amounts negative, as a debit of the
	// balance, and reads them back with abs(); so does this. A zero-amount
	// purchase is an operator's tariff switch, not a purchase, and a row with
	// no date cannot be placed in any window.
	rows, err := tx.Query(ctx, `SELECT CASE WHEN type = 'gift_payment' THEN '`+bedolagaimport.PaymentGiftBalance+`'
			ELSE '`+bedolagaimport.PaymentSubscription+`' END,
		id::bigint, user_id::bigint, abs(amount_kopeks)::bigint, `+method+`, `+paidAt+`
		FROM transactions
		WHERE type IN ('subscription_payment', 'gift_payment') AND `+completed+`
		  AND amount_kopeks <> 0 AND user_id IS NOT NULL AND created_at IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read payments: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (bedolagaimport.Payment, error) {
		var payment bedolagaimport.Payment
		err := row.Scan(&payment.Kind, &payment.ID, &payment.UserID, &payment.AmountKopeks, &payment.Method, &payment.PaidAt)
		payment.PaidAt = payment.PaidAt.UTC()
		return payment, err
	})
}

// readGifts loads the gifts bought through a provider in the web cabinet —
// purchases of their buyers that no transaction records — and the paid gifts
// whose recipient has not received them yet. A gift paid from the balance is
// a `gift_payment` transaction, which readPayments reads.
func readGifts(ctx context.Context, tx pgx.Tx, columns map[string]map[string]bool) ([]bedolagaimport.Payment, []bedolagaimport.Gift, error) {
	table := columns["guest_purchases"]
	for _, name := range []string{"id", "is_gift", "status", "amount_kopeks", "payment_method", "paid_at", "buyer_user_id"} {
		if !table[name] {
			return nil, nil, nil
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::bigint, buyer_user_id::bigint, amount_kopeks::bigint,
		COALESCE(payment_method, ''), paid_at, COALESCE(status, '')
		FROM guest_purchases
		WHERE is_gift AND paid_at IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read gifts: %w", err)
	}
	defer rows.Close()
	var payments []bedolagaimport.Payment
	var unclaimed []bedolagaimport.Gift
	for rows.Next() {
		var (
			id, amount     int64
			buyer          *int64
			method, status string
			paidAt         time.Time
		)
		if err := rows.Scan(&id, &buyer, &amount, &method, &paidAt, &status); err != nil {
			return nil, nil, err
		}
		paidAt = paidAt.UTC()
		if buyer != nil && method != "" && method != "balance" {
			payments = append(payments, bedolagaimport.Payment{
				Kind: bedolagaimport.PaymentGift, ID: id, UserID: *buyer, AmountKopeks: amount, Method: method, PaidAt: paidAt,
			})
		}
		if status != "delivered" {
			unclaimed = append(unclaimed, bedolagaimport.Gift{ID: id, Status: status, AmountKopeks: amount, PaidAt: paidAt})
		}
	}
	return payments, unclaimed, rows.Err()
}

// readDiscounts finds, per user, the latest discount Bedolaga gave: a one-off
// `discount` promo code it recorded a use of, or a discount offer the user
// claimed. A promo group's standing discount is not a one-off and is not read.
func readDiscounts(ctx context.Context, tx pgx.Tx, columns map[string]map[string]bool) ([]bedolagaimport.Discount, error) {
	parts := []string{}
	if columns["promocode_uses"]["used_at"] && columns["promocode_uses"]["promocode_id"] && columns["promocodes"]["type"] {
		parts = append(parts, `SELECT u.user_id, u.used_at AS at FROM promocode_uses u
			JOIN promocodes p ON p.id = u.promocode_id WHERE p.type = 'discount' AND u.used_at IS NOT NULL`)
	}
	if offers := columns["discount_offers"]; offers["claimed_at"] && offers["discount_percent"] && offers["user_id"] {
		parts = append(parts, `SELECT o.user_id, o.claimed_at AS at FROM discount_offers o
			WHERE o.claimed_at IS NOT NULL AND o.discount_percent > 0`)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT user_id::bigint, max(at) FROM (`+strings.Join(parts, " UNION ALL ")+`) discounts
		GROUP BY user_id ORDER BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("read discounts: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (bedolagaimport.Discount, error) {
		var discount bedolagaimport.Discount
		err := row.Scan(&discount.UserID, &discount.At)
		discount.At = discount.At.UTC()
		return discount, err
	})
}

// archiveQueries count what does not migrate. Each names the table it needs:
// Bedolaga grew tables over its releases, and a missing one is reported, not
// fatal.
var archiveQueries = []struct {
	table string
	query string
	set   func(*bedolagaimport.Archive, []bedolagaimport.CountSum)
}{
	{"transactions", `SELECT COALESCE(type, ''), count(*), COALESCE(sum(amount_kopeks), 0)::bigint
		FROM transactions GROUP BY 1 ORDER BY 1`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) { archive.Transactions = rows }},
	{"transactions", `SELECT COALESCE(payment_method, 'unknown'), count(*), COALESCE(sum(amount_kopeks), 0)::bigint
		FROM transactions WHERE type = 'deposit' AND is_completed GROUP BY 1 ORDER BY 1`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) {
			archive.CompletedDeposits = rows
		}},
	{"stars_orders", `SELECT status, count(*), COALESCE(sum(amount_kopeks), 0)::bigint
		FROM stars_orders GROUP BY 1 ORDER BY 1`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) { archive.StarsOrders = rows }},
	{"tickets", `SELECT status, count(*), 0::bigint FROM tickets GROUP BY 1 ORDER BY 1`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) { archive.Tickets = rows }},
	{"promocodes", `SELECT type, count(*), 0::bigint FROM promocodes
		WHERE is_active AND (valid_until IS NULL OR valid_until > now())
		  AND COALESCE(current_uses, 0) < COALESCE(max_uses, 1)
		GROUP BY 1 ORDER BY 1`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) { archive.ActivePromoCodes = rows }},
	{"promo_groups", `SELECT g.name, count(u.id), 0::bigint
		FROM promo_groups g LEFT JOIN users u ON u.promo_group_id = g.id
		GROUP BY g.name ORDER BY g.name`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) { archive.PromoGroups = rows }},
	{"referral_earnings", `SELECT 'all', count(*), COALESCE(sum(amount_kopeks), 0)::bigint FROM referral_earnings`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) {
			if len(rows) == 1 {
				archive.ReferralEarnings = rows[0]
			}
		}},
	{"withdrawal_requests", `SELECT status, count(*), COALESCE(sum(amount_kopeks), 0)::bigint
		FROM withdrawal_requests GROUP BY 1 ORDER BY 1`,
		func(archive *bedolagaimport.Archive, rows []bedolagaimport.CountSum) {
			archive.WithdrawalRequests = rows
		}},
}

func readArchive(ctx context.Context, tx pgx.Tx, columns map[string]map[string]bool) (bedolagaimport.Archive, error) {
	archive := bedolagaimport.Archive{}
	missing := map[string]bool{}
	for _, entry := range archiveQueries {
		if columns[entry.table] == nil {
			if !missing[entry.table] {
				missing[entry.table] = true
				archive.MissingTables = append(archive.MissingTables, entry.table)
			}
			continue
		}
		rows, err := tx.Query(ctx, entry.query)
		if err != nil {
			return archive, fmt.Errorf("count %s: %w", entry.table, err)
		}
		buckets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (bedolagaimport.CountSum, error) {
			var bucket bedolagaimport.CountSum
			err := row.Scan(&bucket.Key, &bucket.Count, &bucket.SumMinor)
			return bucket, err
		})
		if err != nil {
			return archive, fmt.Errorf("count %s: %w", entry.table, err)
		}
		entry.set(&archive, buckets)
	}
	oauth := []string{}
	for _, column := range []string{"google_id", "yandex_id", "discord_id", "vk_id"} {
		if columns["users"][column] {
			oauth = append(oauth, column+" IS NOT NULL")
		}
	}
	if len(oauth) > 0 {
		query := "SELECT count(*) FROM users WHERE " + joinOr(oauth)
		if err := tx.QueryRow(ctx, query).Scan(&archive.OAuthLinkedUsers); err != nil {
			return archive, fmt.Errorf("count OAuth links: %w", err)
		}
	}
	return archive, nil
}

func joinOr(parts []string) string {
	result := ""
	for index, part := range parts {
		if index > 0 {
			result += " OR "
		}
		result += part
	}
	return result
}

// Connect opens the Bedolaga database read-only. The session default is set
// as well as each transaction's mode, so even a statement run outside Read
// cannot write.
func Connect(ctx context.Context, url string) (*pgx.Conn, error) {
	if url == "" {
		return nil, errors.New("the Bedolaga database URL is required")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse the Bedolaga database URL: %w", err)
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	config.RuntimeParams["default_transaction_read_only"] = "on"
	config.RuntimeParams["application_name"] = "omniflow-migrate-bedolaga"
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return pgx.ConnectConfig(connectCtx, config)
}
