//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
	"github.com/omniflow/omniflow/internal/bedolagasource"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/customerpg"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// bedolagaSchema is the part of Bedolaga v5's schema the importer reads,
// derived from app/database/models.py and the alembic history (0144/0145 for
// the fork's stars_orders). Column types match production: integer primary
// keys, bigint Telegram and Remnawave IDs, kopek amounts as integers.
//
// users.telegram_id is unique in Bedolaga. It is left non-unique here so the
// duplicate-ID conflict a hand-edited or merged dump can carry is testable.
// withdrawal_requests is deliberately absent: the reader must report a table
// an older Bedolaga never had instead of failing.
const bedolagaSchema = `
CREATE TABLE promo_groups (
  id serial PRIMARY KEY,
  name varchar(255) NOT NULL UNIQUE,
  priority integer NOT NULL DEFAULT 0,
  server_discount_percent integer NOT NULL DEFAULT 0,
  is_default boolean NOT NULL DEFAULT false
);
CREATE TABLE users (
  id serial PRIMARY KEY,
  telegram_id bigint,
  auth_type varchar(20) NOT NULL DEFAULT 'telegram',
  username varchar(255),
  first_name varchar(255),
  last_name varchar(255),
  status varchar(20) DEFAULT 'active',
  language varchar(5) DEFAULT 'ru',
  balance_kopeks integer DEFAULT 0,
  has_had_paid_subscription boolean NOT NULL DEFAULT false,
  referred_by_id integer REFERENCES users(id) ON DELETE SET NULL,
  referral_code varchar(20) UNIQUE,
  created_at timestamptz DEFAULT now(),
  updated_at timestamptz DEFAULT now(),
  remnawave_id bigint UNIQUE,
  remnawave_uuid varchar(255) UNIQUE,
  email varchar(255) UNIQUE,
  email_verified boolean NOT NULL DEFAULT false,
  email_verified_at timestamptz,
  google_id varchar(255) UNIQUE,
  vk_id bigint UNIQUE,
  promo_group_id integer REFERENCES promo_groups(id) ON DELETE RESTRICT
);
CREATE TABLE tariffs (
  id serial PRIMARY KEY,
  name varchar(255) NOT NULL,
  is_active boolean NOT NULL DEFAULT true,
  traffic_limit_gb integer NOT NULL DEFAULT 100,
  device_limit integer NOT NULL DEFAULT 1,
  period_prices json NOT NULL DEFAULT '{}'
);
CREATE TABLE subscriptions (
  id serial PRIMARY KEY,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status varchar(20) DEFAULT 'trial',
  is_trial boolean DEFAULT true,
  start_date timestamptz DEFAULT now(),
  end_date timestamptz NOT NULL,
  traffic_limit_gb integer DEFAULT 0,
  device_limit integer DEFAULT 1,
  connected_squads json DEFAULT '[]',
  created_at timestamptz DEFAULT now(),
  updated_at timestamptz DEFAULT now(),
  remnawave_short_uuid varchar(255),
  remnawave_id bigint,
  remnawave_uuid varchar(255),
  tariff_id integer REFERENCES tariffs(id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX uq_subscriptions_remnawave_id ON subscriptions (remnawave_id) WHERE remnawave_id IS NOT NULL;
CREATE TABLE transactions (
  id serial PRIMARY KEY,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type varchar(50) NOT NULL,
  amount_kopeks integer NOT NULL,
  description text,
  payment_method varchar(50),
  external_id varchar(255),
  is_completed boolean DEFAULT true,
  created_at timestamptz DEFAULT now(),
  completed_at timestamptz
);
CREATE TABLE promocodes (
  id serial PRIMARY KEY,
  code varchar(50) NOT NULL UNIQUE,
  type varchar(50) NOT NULL,
  balance_bonus_kopeks integer DEFAULT 0,
  subscription_days integer DEFAULT 0,
  max_uses integer DEFAULT 1,
  current_uses integer DEFAULT 0,
  valid_from timestamptz DEFAULT now(),
  valid_until timestamptz,
  is_active boolean DEFAULT true
);
CREATE TABLE promocode_uses (
  id serial PRIMARY KEY,
  promocode_id integer NOT NULL REFERENCES promocodes(id),
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  used_at timestamptz DEFAULT now()
);
CREATE TABLE discount_offers (
  id serial PRIMARY KEY,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  notification_type varchar(50) NOT NULL DEFAULT 'expired',
  discount_percent integer NOT NULL DEFAULT 0,
  bonus_amount_kopeks integer NOT NULL DEFAULT 0,
  expires_at timestamptz NOT NULL,
  claimed_at timestamptz,
  is_active boolean NOT NULL DEFAULT true
);
CREATE TABLE guest_purchases (
  id serial PRIMARY KEY,
  token varchar(64) NOT NULL UNIQUE,
  contact_type varchar(20) NOT NULL,
  contact_value varchar(255) NOT NULL,
  is_gift boolean NOT NULL DEFAULT false,
  buyer_user_id integer REFERENCES users(id) ON DELETE SET NULL,
  user_id integer REFERENCES users(id) ON DELETE SET NULL,
  period_days integer NOT NULL,
  amount_kopeks integer NOT NULL,
  payment_method varchar(50),
  status varchar(20) NOT NULL DEFAULT 'pending',
  created_at timestamptz DEFAULT now(),
  paid_at timestamptz
);
CREATE TABLE referral_earnings (
  id serial PRIMARY KEY,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  referral_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  amount_kopeks integer NOT NULL,
  reason varchar(100) NOT NULL,
  created_at timestamptz DEFAULT now()
);
CREATE TABLE tickets (
  id serial PRIMARY KEY,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title varchar(255) NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'open',
  priority varchar(20) NOT NULL DEFAULT 'normal',
  created_at timestamptz DEFAULT now()
);
CREATE TABLE stars_orders (
  id serial PRIMARY KEY,
  user_id integer REFERENCES users(id) ON DELETE SET NULL,
  recipient_username varchar(64) NOT NULL,
  quantity integer NOT NULL,
  amount_kopeks integer NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'paid',
  source varchar(20) NOT NULL DEFAULT 'bot',
  idempotency_key varchar(128) NOT NULL UNIQUE,
  ton_rate_kopeks integer,
  cost_kopeks bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE advertising_campaigns (
  id serial PRIMARY KEY,
  name varchar(255) NOT NULL,
  start_parameter varchar(64) NOT NULL UNIQUE,
  bonus_type varchar(20) NOT NULL,
  balance_bonus_kopeks integer DEFAULT 0,
  subscription_duration_days integer,
  subscription_traffic_gb integer,
  subscription_device_limit integer,
  subscription_squads json,
  tariff_id integer,
  tariff_duration_days integer,
  is_active boolean DEFAULT true,
  partner_user_id integer,
  created_by integer,
  created_at timestamptz DEFAULT now(),
  updated_at timestamptz DEFAULT now()
);
CREATE TABLE advertising_campaign_registrations (
  id serial PRIMARY KEY,
  campaign_id integer NOT NULL REFERENCES advertising_campaigns(id) ON DELETE CASCADE,
  user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  bonus_type varchar(20) NOT NULL,
  balance_bonus_kopeks integer DEFAULT 0,
  subscription_duration_days integer,
  tariff_id integer,
  tariff_duration_days integer,
  created_at timestamptz DEFAULT now(),
  CONSTRAINT uq_campaign_user UNIQUE (campaign_id, user_id)
);
`

// fakePanel is a Remnawave that can only list users, which is all the
// importer may ever ask of it. It counts the calls.
type fakePanel struct {
	mu    sync.Mutex
	users []remnawave.User
	calls int
}

var _ remnawave.Importer = (*fakePanel)(nil)

func (panel *fakePanel) ListUsers(_ context.Context, start, size int) ([]remnawave.User, int, error) {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	panel.calls++
	if start >= len(panel.users) {
		return nil, len(panel.users), nil
	}
	end := min(start+size, len(panel.users))
	return append([]remnawave.User(nil), panel.users[start:end]...), len(panel.users), nil
}

func (panel *fakePanel) setExpiry(id int64, expireAt time.Time) {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	for index := range panel.users {
		if panel.users[index].ID == id {
			panel.users[index].ExpireAt = expireAt
		}
	}
}

// crashingWallet commits a wallet movement and then pulls the plug on the
// run, the way a killed process would after a commit it never acknowledged.
type crashingWallet struct {
	inner  bedolagaimportpg.WalletAdjuster
	cancel context.CancelFunc
	after  int
	calls  int
}

func (wallet *crashingWallet) AdjustWallet(ctx context.Context, customerID, currency string, amount int64, kind, reference, key, reason, actor string) error {
	err := wallet.inner.AdjustWallet(ctx, customerID, currency, amount, kind, reference, key, reason, actor)
	wallet.calls++
	if wallet.calls >= wallet.after {
		wallet.cancel()
	}
	return err
}

type bedolagaFixture struct {
	harness  *harness
	source   *pgxpool.Pool
	url      string
	panel    *fakePanel
	store    *commercepg.Store
	contacts *customerpg.Service
	mapping  bedolagaimport.Mapping
	now      time.Time
	ends     map[string]time.Time
	tariffs  map[string]int64
}

func newBedolagaFixture(t *testing.T) *bedolagaFixture {
	t.Helper()
	ctx := context.Background()
	harness := newHarness(t)
	if _, err := harness.pool.Exec(ctx, `CREATE DATABASE bedolaga`); err != nil {
		t.Fatalf("create the Bedolaga database: %v", err)
	}
	url := strings.Replace(harness.url, "/omniflow?", "/bedolaga?", 1)
	source, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect to Bedolaga: %v", err)
	}
	t.Cleanup(source.Close)
	if _, err = source.Exec(ctx, bedolagaSchema); err != nil {
		t.Fatalf("create the Bedolaga schema: %v", err)
	}
	contacts, err := customerpg.New(harness.pool, []byte("0123456789abcdef0123456789abcdef"), 0)
	if err != nil {
		t.Fatalf("customer service: %v", err)
	}
	fixture := &bedolagaFixture{
		harness: harness, source: source, url: url, panel: &fakePanel{},
		store: harness.commerceStore(t, testOptions()), contacts: contacts,
		now: time.Now().UTC().Truncate(time.Second), ends: map[string]time.Time{}, tariffs: map[string]int64{},
	}
	fixture.seed(ctx, t)
	return fixture
}

func (fixture *bedolagaFixture) exec(ctx context.Context, t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := fixture.source.Exec(ctx, query, args...); err != nil {
		t.Fatalf("seed Bedolaga: %v\n%s", err, query)
	}
}

// seed writes a small Bedolaga with one of every case the importer decides
// about, and the Omniflow catalogue the mapping points at.
func (fixture *bedolagaFixture) seed(ctx context.Context, t *testing.T) {
	t.Helper()
	now := fixture.now
	recent := now.AddDate(0, 0, -20)
	old := now.AddDate(-1, 0, 0)
	for _, name := range []string{"Безлимит", "Retired tariff"} {
		var id int64
		if err := fixture.source.QueryRow(ctx, `INSERT INTO tariffs (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
			t.Fatalf("seed tariff: %v", err)
		}
		fixture.tariffs[name] = id
	}
	fixture.exec(ctx, t, `INSERT INTO promo_groups (name) VALUES ('VIP')`)
	users := []struct {
		id                         int
		telegram                   any
		username, language, status string
		balance                    int
		referredBy                 any
		paid                       bool
		email                      any
		verified                   bool
		remnawave                  any
		created                    time.Time
	}{
		{1, 5001, "alice", "ru", "active", 15000, nil, true, "alice@example.com", true, nil, old},
		{2, 5002, "bob", "en", "active", 0, 1, true, nil, false, nil, recent},
		{3, 5003, "carol", "ru", "active", -500, nil, false, nil, false, nil, old},
		{4, 5004, "dave", "ru", "blocked", 0, nil, false, nil, false, nil, old},
		{5, 5005, "erin", "ru", "deleted", 900, nil, false, nil, false, nil, old},
		{6, 6000, "twin1", "ru", "active", 0, nil, false, nil, false, nil, old},
		{7, 6000, "twin2", "ru", "active", 0, nil, false, nil, false, nil, old},
		{8, 5008, "frank", "ru", "active", 2500, 1, false, "frank@example.com", false, nil, recent},
		{9, 5009, "gina", "uk", "active", 0, nil, true, nil, false, 909, old},
		{10, 5010, "held", "ru", "active", 0, nil, false, nil, false, nil, old},
		{12, 5012, "grace", "ru", "active", 0, 1, true, nil, false, nil, recent},
	}
	for _, user := range users {
		fixture.exec(ctx, t, `INSERT INTO users (id, telegram_id, username, first_name, language, status, balance_kopeks,
			referred_by_id, has_had_paid_subscription, email, email_verified, email_verified_at, remnawave_id, created_at,
			referral_code, promo_group_id)
			VALUES ($1, $2, $3::text, initcap($3::text), $4, $5, $6, $7, $8, $9, $10::boolean, CASE WHEN $10::boolean THEN $11::timestamptz END, $12, $11::timestamptz,
			'ref' || $3::text, CASE WHEN $1::integer = 1 THEN 1 END)`,
			user.id, user.telegram, user.username, user.language, user.status, user.balance, user.referredBy,
			user.paid, user.email, user.verified, user.created, user.remnawave)
	}
	fixture.exec(ctx, t, `SELECT setval('users_id_seq', 100)`)
	unlimited, retired := fixture.tariffs["Безлимит"], fixture.tariffs["Retired tariff"]
	fixture.ends["alice"] = now.AddDate(0, 0, 20)
	fixture.ends["bob"] = now.AddDate(0, 0, 2)
	fixture.ends["dave"] = now.AddDate(0, 0, 9)
	fixture.ends["gina"] = now.AddDate(0, 0, 40)
	subscriptions := []struct {
		id, user  int
		status    string
		trial     bool
		tariff    any
		remnawave any
		start     time.Time
		end       time.Time
	}{
		{1, 1, "active", false, unlimited, 701, recent, fixture.ends["alice"]},
		{2, 2, "trial", true, unlimited, 702, now.AddDate(0, 0, -1), fixture.ends["bob"]},
		{3, 3, "active", false, unlimited, nil, recent, now.AddDate(0, 0, 10)},
		{4, 1, "active", false, retired, 704, recent, now.AddDate(0, 0, 5)},
		{5, 8, "expired", false, unlimited, 705, old, old.AddDate(0, 1, 0)},
		{6, 9, "active", false, unlimited, nil, recent, fixture.ends["gina"]},
		{7, 4, "active", false, unlimited, 707, recent, fixture.ends["dave"]},
	}
	for _, subscription := range subscriptions {
		fixture.exec(ctx, t, `INSERT INTO subscriptions (id, user_id, status, is_trial, tariff_id, remnawave_id, start_date, end_date, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $7)`, subscription.id, subscription.user, subscription.status,
			subscription.trial, subscription.tariff, subscription.remnawave, subscription.start, subscription.end)
	}
	fixture.exec(ctx, t, `SELECT setval('subscriptions_id_seq', 100)`)
	fixture.exec(ctx, t, `INSERT INTO transactions (user_id, type, amount_kopeks, payment_method) VALUES
		(1, 'deposit', 20000, 'yookassa'), (1, 'subscription_payment', -5000, NULL), (8, 'deposit', 2500, 'telegram_stars')`)
	fixture.exec(ctx, t, `INSERT INTO promocodes (code, type, balance_bonus_kopeks) VALUES ('WELCOME', 'balance', 5000)`)
	fixture.exec(ctx, t, `INSERT INTO referral_earnings (user_id, referral_id, amount_kopeks, reason) VALUES (1, 2, 5000, 'first_topup')`)
	fixture.exec(ctx, t, `INSERT INTO tickets (user_id, title, status) VALUES (1, 'Help', 'open'), (2, 'Thanks', 'closed')`)
	fixture.exec(ctx, t, `INSERT INTO stars_orders (user_id, recipient_username, quantity, amount_kopeks, status, idempotency_key)
		VALUES (1, 'alice_tg', 100, 17900, 'completed', 'k1'), (2, 'bobby_tg', 50, 8900, 'refunded', 'k2')`)

	devices := 5
	squad := []remnawave.InternalSquad{{UUID: "4c2a1d7e-0000-4000-8000-00000000a001", Name: "Default"}}
	for id, expire := range map[int64]time.Time{
		701: fixture.ends["alice"], 702: fixture.ends["bob"], 704: now.AddDate(0, 0, 5),
		705: old.AddDate(0, 1, 0), 707: fixture.ends["dave"], 909: fixture.ends["gina"], 999: now.AddDate(0, 0, 3),
	} {
		status := "ACTIVE"
		if id == 705 {
			status = "EXPIRED"
		}
		fixture.panel.users = append(fixture.panel.users, remnawave.User{
			ID: id, Username: "bedolaga_" + strings.Repeat("x", int(id%7)+1), Status: status, ExpireAt: expire,
			TrafficLimitBytes: 0, HWIDDeviceLimit: &devices, ActiveInternalSquads: squad,
		})
	}

	// Omniflow already knows Telegram account 5010: somebody started the new
	// bot during the rehearsal.
	held := fixture.harness.customer(ctx, t)
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
		VALUES ($1::uuid, 'telegram', '5010', now())`, held); err != nil {
		t.Fatalf("seed held identity: %v", err)
	}
	month := fixture.harness.catalog(ctx, t, "unlimited-month", 22900)
	var trialPlan, trialVersion string
	if err := fixture.harness.pool.QueryRow(ctx, `INSERT INTO plans (code, kind, visible) VALUES ('trial', 'trial', true) RETURNING id::text`).Scan(&trialPlan); err != nil {
		t.Fatalf("create trial plan: %v", err)
	}
	if err := fixture.harness.pool.QueryRow(ctx, `INSERT INTO plan_versions (plan_id, version, billing_period, duration_seconds)
		VALUES ($1::uuid, 1, 'day', 259200) RETURNING id::text`, trialPlan).Scan(&trialVersion); err != nil {
		t.Fatalf("create trial version: %v", err)
	}
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO plan_prices (plan_version_id, currency, amount_minor) VALUES ($1::uuid, 'RUB', 0)`, trialVersion); err != nil {
		t.Fatalf("price trial: %v", err)
	}
	fixture.mapping = bedolagaimport.Mapping{
		Currency: "RUB", BlockedUsers: bedolagaimport.BlockedSuspend, WalletReason: bedolagaimport.DefaultWalletReason,
		Plans: []bedolagaimport.PlanRule{
			{TariffID: &unlimited, PlanVersionID: month},
			{Trial: true, PlanVersionID: trialVersion},
		},
	}
}

func (fixture *bedolagaFixture) service(t *testing.T, wallet bedolagaimportpg.WalletAdjuster) *bedolagaimportpg.Service {
	t.Helper()
	if wallet == nil {
		wallet = fixture.store
	}
	service, err := bedolagaimportpg.New(fixture.harness.pool, wallet, fixture.contacts, fixture.panel, bedolagaimportpg.Options{Actor: "test-operator"})
	if err != nil {
		t.Fatalf("build importer: %v", err)
	}
	return service
}

// read takes the snapshot the CLI takes, through the read-only connection.
func (fixture *bedolagaFixture) read(ctx context.Context, t *testing.T) bedolagaimport.Source {
	t.Helper()
	connection, err := bedolagasource.Connect(ctx, fixture.url)
	if err != nil {
		t.Fatalf("connect read-only: %v", err)
	}
	defer func() { _ = connection.Close(ctx) }()
	source, err := bedolagasource.Read(ctx, connection)
	if err != nil {
		t.Fatalf("read Bedolaga: %v", err)
	}
	return source
}

func (fixture *bedolagaFixture) count(ctx context.Context, t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := fixture.harness.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count: %v\n%s", err, query)
	}
	return count
}

func (fixture *bedolagaFixture) customerOf(ctx context.Context, t *testing.T, sourceID int64) string {
	t.Helper()
	var id string
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT user_id::text FROM bedolaga_import_items
		WHERE kind = 'user' AND source_id = $1 AND status = 'applied'`, sourceID).Scan(&id); err != nil {
		t.Fatalf("customer of Bedolaga user %d: %v", sourceID, err)
	}
	return id
}

type importCounts struct {
	customers, identities, ledger, orders, entitlements, subscriptions, attributions, contacts int
}

func (fixture *bedolagaFixture) snapshot(ctx context.Context, t *testing.T) importCounts {
	return importCounts{
		customers:     fixture.count(ctx, t, `SELECT count(*) FROM users`),
		identities:    fixture.count(ctx, t, `SELECT count(*) FROM identities`),
		ledger:        fixture.count(ctx, t, `SELECT count(*) FROM ledger_transactions`),
		orders:        fixture.count(ctx, t, `SELECT count(*) FROM orders`),
		entitlements:  fixture.count(ctx, t, `SELECT count(*) FROM entitlements`),
		subscriptions: fixture.count(ctx, t, `SELECT count(*) FROM subscriptions`),
		attributions:  fixture.count(ctx, t, `SELECT count(*) FROM referral_attributions`),
		contacts:      fixture.count(ctx, t, `SELECT count(*) FROM contact_channels`),
	}
}

func TestBedolagaImportPlanApplyVerify(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	service := fixture.service(t, nil)
	source := fixture.read(ctx, t)

	if len(source.Users) != 11 || len(source.Subscriptions) != 7 {
		t.Fatalf("read %d users and %d subscriptions", len(source.Users), len(source.Subscriptions))
	}
	if len(source.Archive.StarsOrders) != 2 || source.Archive.ReferralEarnings.SumMinor != 5000 ||
		len(source.Archive.MissingTables) != 1 || source.Archive.MissingTables[0] != "withdrawal_requests" {
		t.Fatalf("archive counts: %+v", source.Archive)
	}

	before := fixture.snapshot(ctx, t)
	plan, err := service.Plan(ctx, source, fixture.mapping)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if after := fixture.snapshot(ctx, t); after != before {
		t.Fatalf("plan must write nothing: %+v → %+v", before, after)
	}
	report := plan.Report
	if report.Users.Actions[bedolagaimport.ActionCreate] != 7 || report.Users.Codes[bedolagaimport.CodeDuplicateTelegramID] != 2 ||
		report.Users.Codes[bedolagaimport.CodeTelegramAlreadyLinked] != 1 || report.Users.Codes[bedolagaimport.CodeDeletedInSource] != 1 {
		t.Fatalf("user plan: %+v", report.Users)
	}
	if report.Balances.Codes[bedolagaimport.CodeNegativeBalance] != 1 || report.Balances.CreditMinor != 17500 {
		t.Fatalf("balance plan: %+v", report.Balances)
	}
	if report.Subscriptions.Codes[bedolagaimport.CodeNoRemnawaveUser] != 1 || report.Subscriptions.Codes[bedolagaimport.CodeUnmappedTariff] != 1 ||
		report.Subscriptions.WithEntitlement != 4 || report.Subscriptions.LinkOnly != 1 {
		t.Fatalf("subscription plan: %+v", report.Subscriptions)
	}
	if len(report.UnmappedTariffs) != 1 || *report.UnmappedTariffs[0].TariffID != fixture.tariffs["Retired tariff"] {
		t.Fatalf("unmapped tariffs: %+v", report.UnmappedTariffs)
	}

	result, err := service.Apply(ctx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Failures) != 0 || result.Remaining != 0 {
		t.Fatalf("first apply: failures %+v, remaining %d", result.Failures, result.Remaining)
	}
	if result.Written["user"] != 7 || result.Written["balance"] != 2 || result.Written["subscription"] != 5 || result.Written["referral"] != 3 {
		t.Fatalf("written: %+v", result.Written)
	}

	alice := fixture.customerOf(ctx, t, 1)
	var createdAt time.Time
	var metadata string
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT u.created_at, i.metadata::text FROM users u
		JOIN identities i ON i.user_id = u.id AND i.provider = 'telegram' AND i.provider_subject = '5001'
		WHERE u.id = $1::uuid`, alice).Scan(&createdAt, &metadata); err != nil {
		t.Fatalf("read alice: %v", err)
	}
	if !createdAt.Equal(fixture.now.AddDate(-1, 0, 0)) || !strings.Contains(metadata, `"username": "alice"`) {
		t.Fatalf("alice keeps her sign-up date and Telegram profile: %s %s", createdAt, metadata)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM contact_channels WHERE user_id = $1::uuid AND kind = 'email'
		AND verified_at IS NOT NULL AND value_ciphertext IS NOT NULL AND NOT marketing_enabled`, alice) != 1 {
		t.Fatal("alice's verified e-mail must arrive sealed, verified, and opted out of marketing")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM contact_channels WHERE user_id = $1::uuid`, fixture.customerOf(ctx, t, 8)) != 0 {
		t.Fatal("an unverified e-mail is not carried over")
	}
	if balance := fixture.harness.walletBalance(ctx, t, alice); balance != 15000 {
		t.Fatalf("alice's wallet = %d, want 15000", balance)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM audit_events WHERE action = 'wallet.adjusted' AND actor_id = 'test-operator'`) != 2 {
		t.Fatal("every carried-over balance is an audited wallet adjustment")
	}
	var endsAt time.Time
	var status string
	var devices int
	var paid int64
	if err := fixture.harness.pool.QueryRow(ctx, `SELECT e.ends_at, e.status, e.device_limit, o.paid_minor
		FROM entitlements e JOIN orders o ON o.id = e.order_id
		JOIN subscriptions s ON s.id = e.subscription_id
		WHERE e.user_id = $1::uuid AND s.remnawave_user_id = 701 AND o.state = 'fulfilled'
		  AND o.idempotency_key = 'bedolaga:subscription:1'`, alice).Scan(&endsAt, &status, &devices, &paid); err != nil {
		t.Fatalf("read alice's entitlement: %v", err)
	}
	if !endsAt.Equal(fixture.ends["alice"]) || status != "active" || devices != 5 || paid != 0 {
		t.Fatalf("alice's entitlement: ends %s status %s devices %d paid %d", endsAt, status, devices, paid)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM remnawave_users WHERE user_id = $1::uuid AND remnawave_id = 701 AND telegram_id = 5001`, alice) != 1 {
		t.Fatal("alice's customer-level mapping must name her panel user and Telegram ID")
	}
	bob := fixture.customerOf(ctx, t, 2)
	if fixture.count(ctx, t, `SELECT count(*) FROM trial_claims WHERE user_id = $1::uuid`, bob) != 1 {
		t.Fatal("a Bedolaga trial counts as the customer's one trial")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM users WHERE id = $1::uuid AND locale = 'en'`, bob) != 1 {
		t.Fatal("bob keeps English")
	}
	frank := fixture.customerOf(ctx, t, 8)
	if fixture.count(ctx, t, `SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid AND remnawave_user_id = 705`, frank) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM entitlements WHERE user_id = $1::uuid`, frank) != 0 {
		t.Fatal("an expired subscription keeps its panel user linked and gets no entitlement")
	}
	gina := fixture.customerOf(ctx, t, 9)
	if fixture.count(ctx, t, `SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid AND remnawave_user_id = 909`, gina) != 1 {
		t.Fatal("a single subscription is linked through the user-level panel ID")
	}
	dave := fixture.customerOf(ctx, t, 4)
	if fixture.count(ctx, t, `SELECT count(*) FROM users WHERE id = $1::uuid AND status = 'suspended' AND suspended_at IS NOT NULL`, dave) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM customer_lifecycle_events WHERE user_id = $1::uuid AND action = 'suspended'`, dave) != 1 {
		t.Fatal("a blocked user under the suspend policy arrives suspended, with the reason on record")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM referral_attributions WHERE referred_user_id = $1::uuid
		AND referrer_user_id = $2::uuid AND qualified_at IS NOT NULL`, bob, alice) != 1 ||
		fixture.count(ctx, t, `SELECT count(*) FROM referral_attributions WHERE referred_user_id = $1::uuid
		AND referrer_user_id = $2::uuid AND qualified_at IS NULL`, frank, alice) != 1 {
		t.Fatal("referral pairs keep their referrer; a referee who paid in Bedolaga arrives qualified")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM fulfillment_operations`) != 0 || fixture.count(ctx, t, `SELECT count(*) FROM river_job`) != 0 {
		t.Fatal("the import must not queue anything that could write to Remnawave")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM bedolaga_import_items WHERE status = 'conflict'`) == 0 {
		t.Fatal("conflicts are recorded so the next run re-evaluates them")
	}

	// A second run writes nothing and changes nothing.
	settled := fixture.snapshot(ctx, t)
	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	for kind, written := range again.Written {
		if written != 0 {
			t.Fatalf("a second apply wrote %d %s records", written, kind)
		}
	}
	if after := fixture.snapshot(ctx, t); after != settled {
		t.Fatalf("a second apply changed the target: %+v → %+v", settled, after)
	}
	verify, err := service.Verify(ctx, fixture.read(ctx, t), fixture.mapping)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verify.OK {
		t.Fatalf("verify after apply: %+v", verify.Checks)
	}

	// The final delta: Bedolaga kept running after the first pass.
	renewed := fixture.ends["alice"].AddDate(0, 1, 0)
	fixture.exec(ctx, t, `UPDATE subscriptions SET end_date = $1 WHERE id = 1`, renewed)
	fixture.panel.setExpiry(701, renewed)
	fixture.exec(ctx, t, `UPDATE users SET balance_kopeks = 12000 WHERE id = 1`)
	fixture.exec(ctx, t, `INSERT INTO users (id, telegram_id, username, status, balance_kopeks, vk_id, google_id) VALUES (13, 5013, 'henry', 'active', 3000, 777001, 'g-henry')`)
	delta, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("delta apply: %v", err)
	}
	if delta.Written["user"] != 1 || delta.Written["balance"] != 2 || delta.Written["subscription"] != 1 || len(delta.Failures) != 0 {
		t.Fatalf("delta: written %+v failures %+v", delta.Written, delta.Failures)
	}
	// Henry signed into the Bedolaga cabinet with VK and Google: both links
	// arrive as identities bound to the preset issuers.
	if fixture.count(ctx, t, `SELECT count(*) FROM identities WHERE (provider, provider_subject, oidc_issuer) IN
		(('oidc:vk', '777001', 'https://id.vk.ru'), ('oidc:google', 'g-henry', 'https://accounts.google.com'))`) != 2 {
		t.Fatal("the cabinet's VK and Google links were not carried")
	}
	if balance := fixture.harness.walletBalance(ctx, t, alice); balance != 12000 {
		t.Fatalf("alice's wallet after the delta = %d, want 12000", balance)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM ledger_transactions WHERE reference_id = 'bedolaga-user:1:2' AND type = 'correction'`) != 1 {
		t.Fatal("the difference is a second, compensating movement")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM entitlements WHERE user_id = $1::uuid AND ends_at = $2`, alice, renewed) != 1 {
		t.Fatal("a renewal in Bedolaga moves the untouched imported entitlement")
	}
	if verify, err = service.Verify(ctx, fixture.read(ctx, t), fixture.mapping); err != nil || !verify.OK {
		t.Fatalf("verify after the delta: %v %+v", err, verify.Checks)
	}

	// Once Omniflow has acted on an entitlement, Bedolaga no longer moves it.
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO fulfillment_operations (entitlement_id, operation, idempotency_key, correlation_id)
		SELECT id, 'reconcile', 'test-reconcile', 'test' FROM entitlements WHERE user_id = $1::uuid AND remnawave_user_id = 701`, alice); err != nil {
		t.Fatalf("simulate a reconcile: %v", err)
	}
	fixture.exec(ctx, t, `UPDATE subscriptions SET end_date = $1 WHERE id = 1`, renewed.AddDate(0, 1, 0))
	fixture.panel.setExpiry(701, renewed.AddDate(0, 1, 0))
	owned, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("apply over an owned entitlement: %v", err)
	}
	if len(owned.Failures) != 1 || owned.Failures[0].Code != bedolagaimport.CodeOmniflowOwnsEntitlement {
		t.Fatalf("the refresh must be refused and reported: %+v", owned.Failures)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM entitlements WHERE user_id = $1::uuid AND ends_at = $2`, alice, renewed) != 1 {
		t.Fatal("a refused refresh leaves the entitlement as Omniflow holds it")
	}
	if verify, err = service.Verify(ctx, fixture.read(ctx, t), fixture.mapping); err != nil || verify.OK {
		t.Fatalf("verify must report the entitlement Bedolaga and Omniflow disagree about: %v %+v", err, verify.Checks)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM bedolaga_import_runs WHERE status = 'completed'`) != 4 {
		t.Fatal("every apply run is recorded")
	}
}

func TestBedolagaImportResumesAfterACrash(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	source := fixture.read(ctx, t)

	// A staged first pass stops at its limit.
	staged, err := fixture.service(t, nil).Apply(ctx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{Limit: 3})
	if err != nil {
		t.Fatalf("staged apply: %v", err)
	}
	if !staged.Stopped || staged.Written["user"] != 3 || staged.Remaining == 0 {
		t.Fatalf("a limited run stops with work remaining: %+v", staged)
	}

	// The next run dies right after committing the first wallet movement.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	crashing := &crashingWallet{inner: fixture.store, cancel: cancel, after: 1}
	if _, err = fixture.service(t, crashing).Apply(runCtx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("the crashed run must report the cancellation, got %v", err)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM bedolaga_import_runs WHERE status = 'failed'`) != 1 {
		t.Fatal("the crashed run is recorded as failed")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM ledger_transactions WHERE reference_id LIKE 'bedolaga-user:%'`) != 1 {
		t.Fatal("exactly the one committed movement exists after the crash")
	}

	// Resuming finishes the job without repeating the committed movement.
	service := fixture.service(t, nil)
	resumed, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Remaining != 0 || len(resumed.Failures) != 0 || resumed.Written["balance"] != 1 {
		t.Fatalf("resume: %+v", resumed)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM ledger_transactions WHERE reference_id LIKE 'bedolaga-user:%'`) != 2 ||
		fixture.count(ctx, t, `SELECT count(*) FROM users`) != 8 {
		t.Fatal("after the resume every customer and movement exists exactly once")
	}
	verify, err := service.Verify(ctx, fixture.read(ctx, t), fixture.mapping)
	if err != nil || !verify.OK {
		t.Fatalf("verify after resume: %v %+v", err, verify.Checks)
	}
}

// TestImportedQualifiedReferralNeverPaysTwice: the inviter of a referee who
// already paid in Bedolaga was rewarded there. That referee's first order in
// Omniflow must not reward anybody again, while a referee who never paid
// still qualifies the ordinary way.
func TestImportedQualifiedReferralNeverPaysTwice(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	if _, err := fixture.service(t, nil).Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := fixture.harness.pool.Exec(ctx, `INSERT INTO referral_programs
		(singleton, enabled, currency, inviter_reward_minor, invitee_reward_minor, terms_url)
		VALUES (true, true, 'RUB', 20000, 10000, 'https://example.test/referrals')`); err != nil {
		t.Fatalf("configure the referral programme: %v", err)
	}
	planVersion := fixture.mapping.Plans[0].PlanVersionID
	buy := func(customerID, key string) {
		t.Helper()
		order, err := fixture.store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: planVersion, Currency: "RUB", Operation: "purchase",
			IdempotencyKey: key, SkipWallet: true,
		})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		fixture.harness.settle(ctx, t, fixture.store, uuidText(order.ID), order.ExternalMinor, "event-"+key)
	}
	buy(fixture.customerOf(ctx, t, 12), "grace-first")
	if rewards := fixture.count(ctx, t, `SELECT count(*) FROM referral_rewards`); rewards != 0 {
		t.Fatalf("a referral qualified in Bedolaga paid again: %d rewards", rewards)
	}
	buy(fixture.customerOf(ctx, t, 8), "frank-first")
	if rewards := fixture.count(ctx, t, `SELECT count(*) FROM referral_rewards`); rewards != 2 {
		t.Fatalf("an imported referee who never paid qualifies normally, got %d rewards", rewards)
	}
}

func TestBedolagaReaderCannotWrite(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	connection, err := bedolagasource.Connect(ctx, fixture.url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = connection.Close(ctx) }()
	if _, err = connection.Exec(ctx, `UPDATE users SET balance_kopeks = 0`); err == nil {
		t.Fatal("the Bedolaga connection must refuse writes")
	}
}
