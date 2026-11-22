//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
)

// The referral terms Bedolaga paid become Omniflow's programme: the levels
// scheme's first level as day rewards, read from the REFERRAL_* keys of
// system_settings and never from the secrets beside them, saved once and
// audited, and left alone on a re-run.
func TestBedolagaImportCarriesReferralTerms(t *testing.T) {
	fixture := newBedolagaFixture(t)
	ctx := context.Background()
	service := fixture.service(t, nil)

	fixture.exec(ctx, t, `
CREATE TABLE system_settings (id serial PRIMARY KEY, key varchar(255), value text);
CREATE TABLE referral_reward_levels (id serial PRIMARY KEY, level integer NOT NULL UNIQUE,
  is_active boolean NOT NULL DEFAULT true, reward_mode varchar(10) NOT NULL DEFAULT 'money',
  trigger varchar(20) NOT NULL DEFAULT 'first_topup', referrer_percent integer, referrer_fixed_kopeks integer,
  referrer_days integer NOT NULL DEFAULT 0, referrer_tariff_id integer, referee_fixed_kopeks integer,
  referee_days integer NOT NULL DEFAULT 0, referee_tariff_id integer, max_payments integer NOT NULL DEFAULT 0,
  required_referrals integer NOT NULL DEFAULT 0);`)
	fixture.exec(ctx, t, `INSERT INTO system_settings (key, value) VALUES
		('REFERRAL_COMMISSION_PERCENT', '15'), ('REFERRAL_REWARD_SCHEME', 'levels'), ('BOT_TOKEN', 'never-read')`)
	fixture.exec(ctx, t, `INSERT INTO referral_reward_levels (level, reward_mode, trigger, referrer_days, referee_days)
		VALUES (1, 'days', 'first_topup', 14, 7)`)

	source := fixture.read(ctx, t)
	if _, ok := source.ReferralTerms.Settings["BOT_TOKEN"]; ok || len(source.ReferralTerms.Settings) != 2 {
		t.Fatalf("settings read %v", source.ReferralTerms.Settings)
	}
	result, err := service.Apply(ctx, source, fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil || len(result.Failures) != 0 || result.Written["referral_program"] != 1 {
		t.Fatalf("apply: %v %+v %+v", err, result.Failures, result.Written)
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM referral_programs WHERE enabled AND inviter_reward_days = 14
		AND invitee_reward_days = 7 AND inviter_reward_minor = 0 AND invitee_reward_minor = 0 AND commission_bps = 0
		AND qualification = 'first_paid_order' AND currency = 'RUB'`) != 1 {
		t.Fatal("the programme pays fourteen and seven days")
	}
	if fixture.count(ctx, t, `SELECT count(*) FROM audit_events WHERE action = 'referral.program.saved'
		AND actor_type = 'system' AND metadata->>'scheme' = 'levels'`) != 1 {
		t.Fatal("saving the programme is audited")
	}

	again, err := service.Apply(ctx, fixture.read(ctx, t), fixture.mapping, bedolagaimportpg.ApplyOptions{})
	if err != nil || len(again.Failures) != 0 || again.Written["referral_program"] != 0 {
		t.Fatalf("second apply: %v %+v %+v", err, again.Failures, again.Written)
	}

	// An operator's own terms stand.
	if _, err := fixture.harness.pool.Exec(ctx, `UPDATE referral_programs SET inviter_reward_days = 30`); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(ctx, fixture.read(ctx, t), fixture.mapping)
	if err != nil || plan.ReferralProgram == nil || plan.ReferralProgram.Codes[0] != "referral_program_exists" {
		t.Fatalf("plan over an operator's programme: %v %+v", err, plan.ReferralProgram)
	}
}
