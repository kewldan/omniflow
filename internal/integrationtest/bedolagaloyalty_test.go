//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Promo groups become a published spend programme and their members keep
// their tier; every other row of the previous bot is archived without its
// secrets, follows the customer, and is erased with them.
func TestBedolagaImportCarriesLoyaltyAndHistory(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	service := fixture.service(t, nil)

	fixture.exec(ctx, t, `
ALTER TABLE promo_groups ADD COLUMN traffic_discount_percent integer NOT NULL DEFAULT 0,
  ADD COLUMN device_discount_percent integer NOT NULL DEFAULT 0, ADD COLUMN period_discounts json,
  ADD COLUMN auto_assign_total_spent_kopeks integer;
ALTER TABLE users ADD COLUMN password_hash varchar(255), ADD COLUMN vless_uuid varchar(64);
CREATE TABLE user_promo_groups (user_id integer NOT NULL REFERENCES users(id), promo_group_id integer NOT NULL REFERENCES promo_groups(id),
  assigned_at timestamptz DEFAULT now(), assigned_by varchar(50), PRIMARY KEY (user_id, promo_group_id));
CREATE TABLE system_settings (id serial PRIMARY KEY, key varchar(255), value text);
CREATE TABLE referral_earnings_extra (id serial PRIMARY KEY, user_id integer, amount_kopeks integer, created_at timestamptz DEFAULT now());`)
	fixture.exec(ctx, t, `UPDATE promo_groups SET server_discount_percent = 20, auto_assign_total_spent_kopeks = 300000 WHERE name = 'VIP'`)
	fixture.exec(ctx, t, `INSERT INTO promo_groups (name, is_default) VALUES ('Все', true)`)
	fixture.exec(ctx, t, `INSERT INTO promo_groups (name, server_discount_percent, auto_assign_total_spent_kopeks) VALUES ('TOP', 10, 150000)`)
	fixture.exec(ctx, t, `INSERT INTO user_promo_groups (user_id, promo_group_id, assigned_by)
		SELECT 1, id, 'admin' FROM promo_groups WHERE name = 'VIP'
		UNION ALL SELECT 2, id, 'auto' FROM promo_groups WHERE name = 'TOP'`)
	fixture.exec(ctx, t, `UPDATE users SET password_hash = 'secret-hash', vless_uuid = 'vless-secret' WHERE id = 1`)
	fixture.exec(ctx, t, `INSERT INTO system_settings (key, value) VALUES ('BOT_TOKEN', 'never-read')`)
	fixture.exec(ctx, t, `INSERT INTO referral_earnings_extra (user_id, amount_kopeks) VALUES (1, 500), (5, 700), (NULL, 900)`)

	result, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil || len(result.Failures) != 0 {
		t.Fatalf("apply: %v %+v", err, result.Failures)
	}
	if result.Written["loyalty"] != 1 || result.Written["loyalty_member"] != 2 || result.Written["archive"] == 0 {
		t.Fatalf("written %+v", result.Written)
	}

	pool := fixture.harness.pool
	if fixture.count(ctx, t, `SELECT count(*) FROM loyalty_programs WHERE enabled AND metric = 'spend'
		AND window_days = 3650 AND recalculation_requested_at IS NOT NULL`) != 1 {
		t.Fatal("the programme is published and asks to be evaluated")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM loyalty_tiers WHERE (code, threshold, discount_bps) IN
		(('vip', 300000, 2000), ('top', 150000, 1000))`) != 2 {
		t.Fatal("VIP and TOP keep their thresholds and discounts")
	}
	alice, bob := fixture.customerOf(ctx, t, 1), fixture.customerOf(ctx, t, 2)
	if fixture.count(ctx, t, `SELECT count(*) FROM loyalty_memberships WHERE (user_id, tier_code) IN
		(($1::uuid, 'vip'), ($2::uuid, 'top'))`, alice, bob) != 2 {
		t.Fatal("both members keep their tier")
	}

	var data string
	if err := pool.QueryRow(ctx, `SELECT data::text FROM legacy_records WHERE kind = 'users' AND user_id = $1::uuid`, alice).Scan(&data); err != nil {
		t.Fatalf("alice's account row: %v", err)
	}
	if strings.Contains(data, "secret-hash") || strings.Contains(data, "vless-secret") || strings.Contains(data, "password_hash") || !strings.Contains(data, "alice") {
		t.Fatalf("archived account row keeps a secret or loses the data: %s", data)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM legacy_records WHERE kind = 'system_settings'`) != 0 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_records WHERE data::text LIKE '%never-read%'`) != 0 {
		t.Fatal("settings, which hold the bot's keys, are never archived")
	}
	// Erin (5) is deleted in Bedolaga and not carried, so neither is her row;
	// a row of nobody's is kept.
	if fixture.count(ctx, t, `SELECT count(*) FROM legacy_records WHERE kind = 'referral_earnings_extra'`) != 2 ||
		fixture.count(ctx, t, `SELECT count(*) FROM legacy_records WHERE kind = 'referral_earnings_extra' AND user_id IS NULL`) != 1 {
		t.Fatal("history follows carried customers only")
	}

	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil || len(again.Failures) != 0 || again.Written["loyalty"] != 0 || again.Written["loyalty_member"] != 0 || again.Written["archive"] != 0 {
		t.Fatalf("second apply: %v %+v %+v", err, again.Failures, again.Written)
	}

	if err := dbgen.New(pool).AnonymizeCustomerData(ctx, mustUUID(t, alice)); err != nil {
		t.Fatal(err)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM legacy_records WHERE user_id = $1::uuid`, alice) != 0 {
		t.Fatal("anonymizing a customer erases the previous bot's rows about them")
	}
}
